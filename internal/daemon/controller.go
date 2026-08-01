package daemon

import (
	"context"
	"fmt"

	"deebeets/internal/control"
	"deebeets/internal/deezer"
	"deebeets/internal/downloader"
	"deebeets/internal/store"
)

// Status implements control.Controller.
func (d *Daemon) Status(ctx context.Context) (control.StatusResponse, error) {
	counts, err := d.store.CountByState(ctx)
	if err != nil {
		return control.StatusResponse{}, err
	}
	status, _ := d.store.GetMeta(ctx, downloader.MetaDownloadStatus)
	lastSync, _ := d.store.GetMeta(ctx, "last_sync")

	d.syncMu.Lock()
	syncing := d.syncing
	d.syncMu.Unlock()

	return control.StatusResponse{
		DownloadRunning: d.pipe.DownloadRunning(),
		DownloadStatus:  status,
		Counts:          counts,
		LastSync:        lastSync,
		Syncing:         syncing,
	}, nil
}

// Sync kicks off a background favorites sync (single-flight). It returns
// immediately so the CLI need not stay connected for large libraries.
// When FixtureAlbums is set in config, it enqueues those albums instead of
// fetching real Deezer favorites, exercising the full pipeline on a fixed set.
func (d *Daemon) Sync(ctx context.Context, sel control.Selection) (control.SyncStarted, error) {
	d.syncMu.Lock()
	if d.syncing {
		d.syncMu.Unlock()
		return control.SyncStarted{Started: false, Message: "sync already in progress"}, nil
	}
	d.syncing = true
	d.syncMu.Unlock()

	if len(d.cfg.FixtureAlbums) > 0 {
		go func() {
			n, err := d.pipe.EnqueueIDs(d.ctx, deezer.KindAlbum, d.cfg.FixtureAlbums)
			d.syncMu.Lock()
			d.syncing = false
			d.syncMu.Unlock()
			if err != nil {
				d.log.Error("fixture sync failed", "err", err)
				return
			}
			_ = d.store.SetMeta(d.ctx, "last_sync",
				fmt.Sprintf("%s (fixture: %d tracks from %d albums)", nowStamp(), n, len(d.cfg.FixtureAlbums)))
			d.log.Info("fixture sync complete", "albums", len(d.cfg.FixtureAlbums), "tracks", n)
		}()
		return control.SyncStarted{Started: true, Message: "fixture sync started"}, nil
	}

	dsel := deezer.Selection(sel)
	if !dsel.Any() {
		// Default to the configured favorite types.
		f := d.cfg.Download.Favorites
		dsel = deezer.Selection{Albums: f.Albums, Artists: f.Artists, Playlists: f.Playlists, Tracks: f.Tracks}
	}
	if !dsel.Any() {
		d.syncMu.Lock()
		d.syncing = false
		d.syncMu.Unlock()
		return control.SyncStarted{}, fmt.Errorf("no favorite types selected")
	}

	go func() {
		res, err := d.pipe.Sync(d.ctx, dsel)
		d.syncMu.Lock()
		d.syncing = false
		d.syncMu.Unlock()
		if err != nil {
			d.log.Error("sync failed", "err", err)
			return
		}
		_ = d.store.SetMeta(d.ctx, "last_sync",
			fmt.Sprintf("%s (new %d / seen %d)", nowStamp(), res.New, res.Total))
		d.log.Info("sync complete", "new", res.New, "seen", res.Total)
	}()

	return control.SyncStarted{Started: true, Message: "sync started"}, nil
}

// Download enqueues specific ids for downloading.
func (d *Daemon) Download(ctx context.Context, kind string, ids []int64) (int, error) {
	if kind == "" {
		kind = store.KindTrack
	}
	return d.pipe.EnqueueIDs(d.ctx, kind, ids)
}

// Redownload forces re-download by mode ("all" or "missing").
func (d *Daemon) Redownload(ctx context.Context, mode string, ids []int64) (int, error) {
	switch mode {
	case "all":
		return d.pipe.ForceAll(d.ctx, ids)
	case "missing":
		return d.pipe.ForceMissing(d.ctx)
	default:
		return 0, fmt.Errorf("redownload mode must be 'all' or 'missing', got %q", mode)
	}
}

// StartDownload starts the download stage.
func (d *Daemon) StartDownload(ctx context.Context) error {
	d.pipe.StartDownload(d.ctx)
	return nil
}

// StopDownload stops the download stage.
func (d *Daemon) StopDownload(ctx context.Context) error {
	d.pipe.StopDownload()
	return nil
}

// BlocklistAdd blocklists ids of a kind.
func (d *Daemon) BlocklistAdd(ctx context.Context, kind string, ids []int64, reason string) error {
	for _, id := range ids {
		if err := d.store.AddBlock(ctx, kind, id, reason); err != nil {
			return err
		}
	}
	return nil
}

// BlocklistRemove removes ids of a kind from the blocklist.
func (d *Daemon) BlocklistRemove(ctx context.Context, kind string, ids []int64) error {
	for _, id := range ids {
		if err := d.store.RemoveBlock(ctx, kind, id); err != nil {
			return err
		}
	}
	return nil
}

// BlocklistList lists blocklist entries.
func (d *Daemon) BlocklistList(ctx context.Context) ([]store.Block, error) {
	return d.store.ListBlocks(ctx)
}

// BeetsImport triggers a manual import of a path.
func (d *Daemon) BeetsImport(ctx context.Context, path string) error {
	if path == "" {
		return fmt.Errorf("import path required")
	}
	return d.imp.Import(d.ctx, path, nil)
}

// Items lists items in the given states.
func (d *Daemon) Items(ctx context.Context, states []string, limit int) ([]store.Item, error) {
	items, err := d.store.List(ctx, states, limit)
	if err != nil {
		return nil, err
	}
	out := make([]store.Item, 0, len(items))
	for _, it := range items {
		out = append(out, *it)
	}
	return out, nil
}
