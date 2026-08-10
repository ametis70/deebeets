package downloader

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"deeznt/internal/deezer"
	"deeznt/internal/store"
)

// SyncResult summarises a sync pass.
type SyncResult struct {
	Sources int // sources seen (albums/artists/playlists)
	Total   int // tracks seen
	New     int // newly inserted track rows
}

// Sync pulls the selected favorite types from Deezer, upserts sources
// (albums/artists/playlists) as first-class entities, then expands each source
// to its track list. Re-syncing re-fetches track lists so new tracks are picked
// up. Loved tracks are synced directly without a source entity.
func (p *Pipeline) Sync(ctx context.Context, sel deezer.Selection) (SyncResult, error) {
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
			tracks, err := p.dz.GetTracksData(ctx, ids[i:end])
			if err != nil {
				return res, err
			}
			for j := range tracks {
				t := &tracks[j]
				if t.SngID == "" {
					continue
				}
				if skip, _ := blocked(deezer.KindTrack, t.ID()); skip {
					continue
				}
				inserted, err := p.store.Upsert(ctx, trackToDiscovered(t, "track", t.SngID))
				if err != nil {
					return res, err
				}
				res.Total++
				if inserted {
					res.New++
				}
			}
		}
	}

	// 2. Upsert sources (albums/artists/playlists) then expand each to tracks.
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

			for _, fi := range favs {
				inserted, err := p.store.Upsert(ctx, toDiscovered(fi))
				if err != nil {
					return res, err
				}
				res.Total++
				if inserted {
					res.New++
				}
			}

			_ = p.store.SetSourceState(ctx, src.Kind, src.ID, store.SourceStateSynced, "")
			_ = p.store.SetSourceTrackCount(ctx, src.Kind, src.ID, len(favs))
		}
	}

	return res, nil
}

// EnqueueIDs adds specific ids (of the given kind: track|album|artist|playlist)
// to the queue as `queued`. For non-track kinds it also upserts a source entity.
// Returns the number of tracks queued.
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
			t, err := p.dz.GetTrack(ctx, id)
			if err != nil {
				return queued, err
			}
			if t.SngID != "" {
				favs = []deezer.FavItem{trackGWToFav(t, kind, fmt.Sprintf("%d", id))}
			}
		} else {
			src := deezer.SourceItem{Kind: kind, ID: id}
			// Fetch name from Deezer for the source entity.
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
			_ = p.store.SetSourceTrackCount(ctx, kind, id, len(favs))
			_ = p.store.SetSourceState(ctx, kind, id, store.SourceStateSynced, "")
		}

		for _, fi := range favs {
			if _, err := p.store.Upsert(ctx, toDiscovered(fi)); err != nil {
				return queued, err
			}
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

// ForceMissing requeues only finished/downloaded items whose file is absent from disk.
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

// MissingFiles returns finished/downloaded items whose file no longer exists on disk.
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
