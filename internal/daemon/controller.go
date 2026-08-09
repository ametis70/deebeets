package daemon

import (
	"context"
	"fmt"

	"deebeets/internal/control"
	"deebeets/internal/store"
)

// Status implements control.Controller.
func (d *Daemon) Status(ctx context.Context) (control.StatusResponse, error) {
	counts, err := d.store.CountByState(ctx)
	if err != nil {
		return control.StatusResponse{}, err
	}
	stage, _ := d.store.GetMeta(ctx, metaCurrentStage)
	lastSync, _ := d.store.GetMeta(ctx, metaLastSync)
	if stage == "" {
		stage = store.StageIdle
	}
	return control.StatusResponse{
		Stage:    stage,
		Counts:   counts,
		LastSync: lastSync,
	}, nil
}

// SyncStart triggers an immediate sync. Errors if downloads or import are active.
func (d *Daemon) SyncStart(ctx context.Context, sel control.Selection) error {
	stage, _ := d.store.GetMeta(ctx, metaCurrentStage)
	if stage == store.StageDownloading || stage == store.StageImporting {
		return fmt.Errorf("cannot start sync while %s is active", stage)
	}
	// Override selection from request if any flags were given.
	if sel.Albums || sel.Artists || sel.Playlists || sel.Tracks {
		// Store the selection override for the sync — send via orchCh.
		// For now we trigger with the default selection; CLI can set flags.
		// A full selection-passing mechanism would require a richer orchCmd;
		// keep it simple and use the config defaults for auto-sync triggers.
	}
	select {
	case d.orchCh <- orchSync:
		return nil
	default:
		return fmt.Errorf("orchestrator busy")
	}
}

// SyncStop cancels an in-progress sync. Errors if downloads are active.
func (d *Daemon) SyncStop(ctx context.Context) error {
	stage, _ := d.store.GetMeta(ctx, metaCurrentStage)
	if stage == store.StageDownloading || stage == store.StageImporting {
		return fmt.Errorf("cannot stop sync while %s is active", stage)
	}
	// Sync runs synchronously inside the orchestrator; sending orchSyncStop
	// interrupts the wait interval so the next sync won't start.
	select {
	case d.orchCh <- orchSyncStop:
	default:
	}
	return nil
}

// DownloadStart enqueues ids (optional) then triggers the download run.
func (d *Daemon) DownloadStart(ctx context.Context, kind string, ids []int64) error {
	if len(ids) > 0 {
		if err := d.connectDeezer(ctx); err != nil {
			return fmt.Errorf("not logged in: %w", err)
		}
		if kind == "" {
			kind = store.KindTrack
		}
		if _, err := d.pipe.EnqueueIDs(d.ctx, kind, ids); err != nil {
			return err
		}
	}
	select {
	case d.orchCh <- orchDownload:
		return nil
	default:
		return fmt.Errorf("orchestrator busy")
	}
}

// DownloadStop aborts the active download run after the current batch.
func (d *Daemon) DownloadStop(ctx context.Context) error {
	select {
	case <-d.stopDLCh:
		// Already closed / stopped.
	default:
		close(d.stopDLCh)
	}
	return nil
}

// Redownload forces re-download by mode ("all", "missing", or "failed").
func (d *Daemon) Redownload(ctx context.Context, mode string, ids []int64) (int, error) {
	var n int
	var err error
	switch mode {
	case "all", "missing":
		if d.pipe == nil {
			return 0, fmt.Errorf("not logged in")
		}
		if mode == "all" {
			n, err = d.pipe.ForceAll(d.ctx, ids)
		} else {
			n, err = d.pipe.ForceMissing(d.ctx)
		}
	case "failed":
		n, err = d.store.RequeueAllFailed(d.ctx)
	default:
		return 0, fmt.Errorf("redownload mode must be 'all', 'missing', or 'failed', got %q", mode)
	}
	if err != nil {
		return 0, err
	}
	// Trigger a download run for the newly requeued items.
	select {
	case d.orchCh <- orchDownload:
	default:
	}
	return n, nil
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

// BeetsImport triggers a manual full-library import run.
func (d *Daemon) BeetsImport(ctx context.Context) error {
	if d.pipe != nil {
		return d.pipe.RunImport(d.ctx)
	}
	// Pipeline not initialised (no login yet) — run import directly via the runner.
	return d.imp.Import(d.ctx, d.cfg.Paths.MusicDir)
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
