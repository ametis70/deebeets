package downloader

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"deeznt/internal/deezer"
	"deeznt/internal/store"
)

// SyncResult summarises a sync pass.
type SyncResult struct {
	Sources int // sources seen (albums/artists/playlists)
	Total   int // tracks seen
	New     int // newly inserted track rows
}

// Sync pulls the selected favorite types from Deezer, upserts sources as
// first-class entities, expands each source to tracks, and fetches/caches
// full track metadata (song.getData + lyrics + album.getData) for each track.
// All metadata is stored in the DB so download/tag stages need no API calls
// except re-fetching the track token.
func (p *Pipeline) Sync(ctx context.Context, sel deezer.Selection, refresh bool) (SyncResult, error) {
	blocked := func(kind string, id int64) (bool, error) {
		return p.store.IsBlocked(ctx, kind, id)
	}
	var res SyncResult

	// 1. Sync loved tracks directly (no source entity).
	if sel.Tracks {
		ids, err := p.dz.FavoriteTrackIDs(ctx)
		if err != nil {
			return res, fmt.Errorf("favorite tracks: %w", err)
		}
		const batch = 100
		for i := 0; i < len(ids); i += batch {
			end := min(i+batch, len(ids))
			if err := p.syncTrackIDs(ctx, ids[i:end], "track", "", blocked, refresh, &res); err != nil {
				return res, err
			}
		}
	}

	// 2. Upsert sources then expand each to tracks.
	sourceSel := deezer.Selection{
		Albums:    sel.Albums,
		Artists:   sel.Artists,
		Playlists: sel.Playlists,
	}
	if sourceSel.Any() {
		sources, err := p.dz.FavoriteSources(ctx, sourceSel, blocked)
		if err != nil {
			return res, fmt.Errorf("favorite sources: %w", err)
		}

		for _, src := range sources {
			if _, err := p.store.UpsertSource(ctx, src.Kind, src.ID, src.Name, src.Artist); err != nil {
				return res, err
			}
			res.Sources++

			_ = p.store.SetSourceState(ctx, src.Kind, src.ID, store.SourceStateSyncing, "")

			favs, err := p.dz.TracksForSource(ctx, src, blocked)
			if err != nil {
				_ = p.store.SetSourceState(ctx, src.Kind, src.ID, store.SourceStateFailed, err.Error())
				return res, fmt.Errorf("expand %s %d: %w", src.Kind, src.ID, err)
			}

			sngIDs := make([]int64, 0, len(favs))
			for _, fi := range favs {
				sngIDs = append(sngIDs, fi.SngID)
			}

			if err := p.syncTrackIDsWithFavs(ctx, sngIDs, favs, refresh, &res); err != nil {
				_ = p.store.SetSourceState(ctx, src.Kind, src.ID, store.SourceStateFailed, err.Error())
				return res, err
			}

			_ = p.store.SetSourceState(ctx, src.Kind, src.ID, store.SourceStateSynced, "")
			_ = p.store.SetSourceTrackCount(ctx, src.Kind, src.ID, len(favs))
		}
	}

	return res, nil
}

// syncTrackIDs fetches song.getData for each ID and upserts with metadata.
func (p *Pipeline) syncTrackIDs(ctx context.Context, ids []int64, sourceType, sourceID string, blocked deezer.BlockedFunc, refresh bool, res *SyncResult) error {
	tracks, err := p.dz.GetTracksData(ctx, ids)
	if err != nil {
		return err
	}
	favs := make([]deezer.FavItem, 0, len(tracks))
	for j := range tracks {
		t := &tracks[j]
		if t.SngID == "" {
			continue
		}
		if skip, _ := blocked(deezer.KindTrack, t.ID()); skip {
			continue
		}
		sid := sourceID
		if sid == "" {
			sid = t.SngID
		}
		favs = append(favs, trackGWToFav(t, sourceType, sid))
	}
	return p.syncTrackIDsWithFavs(ctx, ids, favs, refresh, res)
}

// syncTrackIDsWithFavs upserts tracks and fetches/caches their metadata.
func (p *Pipeline) syncTrackIDsWithFavs(ctx context.Context, ids []int64, favs []deezer.FavItem, refresh bool, res *SyncResult) error {
	// Fetch full song.getData for each track in parallel (bounded concurrency).
	type metaResult struct {
		fi         deezer.FavItem
		trackData  string
		lyricsData string
		err        error
	}

	sem := make(chan struct{}, p.cfg.Download.Concurrency)
	results := make([]metaResult, len(favs))
	var wg sync.WaitGroup

	for i, fi := range favs {
		// Skip if already cached and not refreshing.
		if !refresh {
			existing, err := p.store.Get(ctx, fi.SngID)
			if err == nil && existing != nil && existing.TrackData != "" {
				results[i] = metaResult{fi: fi, trackData: existing.TrackData, lyricsData: existing.LyricsData}
				continue
			}
		}

		wg.Add(1)
		i, fi := i, fi
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()

			// Fetch full track data.
			track, trackData, err := p.dz.GetTrackWithRaw(ctx, fi.SngID)
			if err != nil {
				results[i] = metaResult{fi: fi, err: fmt.Errorf("song.getData %d: %w", fi.SngID, err)}
				return
			}

			// Fetch lyrics if available.
			var lyricsData string
			if track.LyricsID != 0 {
				if ld, err := p.dz.GetLyricsRaw(ctx, fi.SngID); err == nil {
					lyricsData = ld
				}
				// Lyrics errors are non-fatal — we still have the track.
			}

			// Cache album.getData (shared across all tracks on the same album).
			if albID := track.AlbumID(); albID > 0 {
				if cached, _ := p.store.GetAlbumCache(ctx, albID); cached == "" || refresh {
					if albumData, err := p.dz.GetAlbumRaw(ctx, albID); err == nil {
						_ = p.store.UpsertAlbumCache(ctx, albID, albumData)
					}
				}
			}

			results[i] = metaResult{fi: fi, trackData: trackData, lyricsData: lyricsData}
		}()
	}
	wg.Wait()

	// Upsert all tracks.
	for _, r := range results {
		if r.fi.SngID == 0 {
			continue // skipped (already cached)
		}
		if r.err != nil {
			p.log.Warn("failed to fetch track metadata at sync time", "sng_id", r.fi.SngID, "err", r.err)
			// Still upsert with empty metadata — download will re-fetch.
		}
		d := toDiscoveredWithMeta(r.fi, r.trackData, r.lyricsData)
		inserted, err := p.store.Upsert(ctx, d)
		if err != nil {
			return err
		}
		res.Total++
		if inserted {
			res.New++
		}
	}
	return nil
}

// EnqueueIDs adds specific ids (of the given kind) to the queue as `queued`.
// For non-track kinds it also upserts a source entity.
func (p *Pipeline) EnqueueIDs(ctx context.Context, kind string, ids []int64) (int, error) {
	blocked := func(k string, id int64) (bool, error) {
		return p.store.IsBlocked(ctx, k, id)
	}
	var queued int
	for _, id := range ids {
		if skip, err := p.store.IsBlocked(ctx, kind, id); err != nil {
			return queued, err
		} else if skip {
			continue
		}

		var favs []deezer.FavItem
		if kind == deezer.KindTrack {
			track, trackData, err := p.dz.GetTrackWithRaw(ctx, id)
			if err != nil {
				return queued, err
			}
			if track.SngID != "" {
				fi := trackGWToFav(track, kind, fmt.Sprintf("%d", id))
				lyricsData := ""
				if track.LyricsID != 0 {
					lyricsData, _ = p.dz.GetLyricsRaw(ctx, id)
				}
				if albID := track.AlbumID(); albID > 0 {
					if cached, _ := p.store.GetAlbumCache(ctx, albID); cached == "" {
						if albumData, err := p.dz.GetAlbumRaw(ctx, albID); err == nil {
							_ = p.store.UpsertAlbumCache(ctx, albID, albumData)
						}
					}
				}
				if _, err := p.store.Upsert(ctx, toDiscoveredWithMeta(fi, trackData, lyricsData)); err != nil {
					return queued, err
				}
				favs = []deezer.FavItem{fi}
			}
		} else {
			src := deezer.SourceItem{Kind: kind, ID: id}
			switch kind {
			case deezer.KindAlbum:
				alb, err := p.dz.GetAlbum(ctx, id)
				if err == nil {
					src.Name = alb.AlbTitle
					src.Artist = alb.ArtName
				}
			}
			if _, err := p.store.UpsertSource(ctx, kind, id, src.Name, src.Artist); err != nil {
				return queued, err
			}
			var err error
			favs, err = p.dz.TracksForSource(ctx, src, blocked)
			if err != nil {
				return queued, err
			}
			// Fetch metadata for each track.
			var syncRes SyncResult
			sngIDs := make([]int64, 0, len(favs))
			for _, fi := range favs {
				sngIDs = append(sngIDs, fi.SngID)
			}
			if err := p.syncTrackIDsWithFavs(ctx, sngIDs, favs, false, &syncRes); err != nil {
				return queued, err
			}
			_ = p.store.SetSourceTrackCount(ctx, kind, id, len(favs))
			_ = p.store.SetSourceState(ctx, kind, id, store.SourceStateSynced, "")
		}

		for _, fi := range favs {
			if err := p.store.SetState(ctx, fi.SngID, store.StateQueued); err != nil {
				return queued, err
			}
			queued++
		}
	}
	return queued, nil
}

// ForceAll requeues items for a full re-download, clearing recorded file paths.
func (p *Pipeline) ForceAll(ctx context.Context, ids []int64) (int, error) {
	if len(ids) == 0 {
		items, err := p.store.FinishedItems(ctx)
		if err != nil {
			return 0, err
		}
		for _, it := range items {
			ids = append(ids, it.SngID)
		}
	}
	return p.store.Requeue(ctx, ids, true)
}

// ForceMissing requeues only finished items whose file is absent from disk.
func (p *Pipeline) ForceMissing(ctx context.Context) (int, error) {
	missing, err := p.MissingFiles(ctx)
	if err != nil {
		return 0, err
	}
	var ids []int64
	for _, it := range missing {
		ids = append(ids, it.SngID)
	}
	return p.store.Requeue(ctx, ids, false)
}

// MissingFiles returns finished items whose file no longer exists on disk.
func (p *Pipeline) MissingFiles(ctx context.Context) ([]*store.Item, error) {
	items, err := p.store.FinishedItems(ctx)
	if err != nil {
		return nil, err
	}
	var out []*store.Item
	for _, it := range items {
		if it.FilePath == "" {
			continue
		}
		abs := filepath.Join(p.musicDir, it.FilePath)
		if _, err := os.Stat(abs); os.IsNotExist(err) {
			out = append(out, it)
		}
	}
	return out, nil
}

func toDiscovered(fi deezer.FavItem) store.Discovered {
	return store.Discovered{
		SngID:       fi.SngID,
		Title:       fi.Title,
		Artist:      fi.Artist,
		Album:       fi.Album,
		AlbumArtist: fi.AlbumArtist,
		GroupKey:    fi.GroupKey,
		SourceType:  fi.SourceType,
		SourceID:    fi.SourceID,
	}
}

func toDiscoveredWithMeta(fi deezer.FavItem, trackData, lyricsData string) store.Discovered {
	d := toDiscovered(fi)
	d.TrackData = trackData
	d.LyricsData = lyricsData
	return d
}

func trackToDiscovered(t *deezer.GWTrack, sourceType, sourceID string) store.Discovered {
	return store.Discovered{
		SngID:       t.ID(),
		Title:       t.SngTitle,
		Artist:      t.ArtName,
		Album:       t.AlbTitle,
		AlbumArtist: t.ArtName,
		GroupKey:    t.AlbID,
		SourceType:  sourceType,
		SourceID:    sourceID,
	}
}

func trackGWToFav(t *deezer.GWTrack, sourceType, sourceID string) deezer.FavItem {
	return deezer.FavItem{
		SngID:       t.ID(),
		Title:       t.SngTitle,
		Artist:      t.ArtName,
		Album:       t.AlbTitle,
		AlbumArtist: t.ArtName,
		GroupKey:    t.AlbID,
		SourceType:  sourceType,
		SourceID:    sourceID,
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
