package downloader

import (
	"context"
	"os"
	"path/filepath"

	"deebeets/internal/deezer"
	"deebeets/internal/store"
)

// SyncResult summarises a sync pass.
type SyncResult struct {
	Total int // tracks seen
	New   int // newly inserted rows
}

// Sync pulls the selected favorite types from Deezer and upserts them into the
// queue as `waiting`. It never downloads and never deletes existing rows, so it
// is safe to run repeatedly. Blocklisted ids are skipped during enumeration.
func (p *Pipeline) Sync(ctx context.Context, sel deezer.Selection) (SyncResult, error) {
	blocked := func(kind string, id int64) (bool, error) {
		return p.store.IsBlocked(ctx, kind, id)
	}
	var res SyncResult
	err := p.dz.EnumerateFavorites(ctx, sel, blocked, func(fi deezer.FavItem) error {
		inserted, err := p.store.Upsert(ctx, toDiscovered(fi))
		if err != nil {
			return err
		}
		res.Total++
		if inserted {
			res.New++
		}
		return nil
	})
	return res, err
}

// EnqueueIDs adds specific ids (of the given kind: track|album|artist|playlist)
// to the queue as `queued`, ready for the download stage. Returns the number of
// tracks queued.
func (p *Pipeline) EnqueueIDs(ctx context.Context, kind string, ids []int64) (int, error) {
	var queued int
	for _, id := range ids {
		if blocked, err := p.store.IsBlocked(ctx, kind, id); err != nil {
			return queued, err
		} else if blocked {
			continue
		}
		favs, err := p.dz.TracksByKind(ctx, kind, id)
		if err != nil {
			return queued, err
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
// When ids is empty every finished/downloaded item is requeued. Use for quality
// upgrades or suspected corruption.
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

// ForceMissing requeues only finished/downloaded items whose file is absent from
// disk, preserving all other state. Use to restore deleted files without
// touching the rest of the library.
func (p *Pipeline) ForceMissing(ctx context.Context) (int, error) {
	missing, err := p.MissingFiles(ctx)
	if err != nil {
		return 0, err
	}
	var ids []int64
	for _, it := range missing {
		ids = append(ids, it.SngID)
	}
	// Keep file_path so the same location is reused.
	return p.store.Requeue(ctx, ids, false)
}

// MissingFiles returns finished/downloaded items whose file no longer exists on
// disk. It never modifies the database — SQLite stays the source of truth.
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
