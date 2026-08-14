package daemon

import (
	"context"
	"fmt"

	"deeznt/internal/control"
	"deeznt/internal/store"
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

	var convertingCount int
	if d.pipe != nil {
		convertingCount = d.pipe.ConvertingCount()
	}

	failedByStage, _ := d.store.CountFailedByStage(ctx)
	_ = failedByStage // counts now visible directly in state names

	albumAvailability, _ := d.store.CountSourcesByDeezerStatus(ctx, store.SourceKindAlbum)

	return control.StatusResponse{
		Stage:             stage,
		Syncing:           stage == store.StageSyncing,
		Downloading:       stage == store.StageDownloading,
		Tagging:           stage == store.StageTagging,
		Converting:        convertingCount > 0 || stage == store.StageConverting || stage == store.StageImporting,
		ConvertingCount:   convertingCount,
		Counts:            counts,
		AlbumAvailability: albumAvailability,
		LastSync:          lastSync,
	}, nil
}

// SyncStart triggers an immediate sync. Errors if downloads are active.
func (d *Daemon) SyncStart(ctx context.Context, sel control.Selection, refresh bool) error {
	stage, _ := d.store.GetMeta(ctx, metaCurrentStage)
	if stage == store.StageDownloading || stage == store.StageImporting || stage == store.StageTagging || stage == store.StageConverting {
		return fmt.Errorf("cannot start sync while %s is active", stage)
	}
	d.syncRefresh = refresh
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
	default:
		close(d.stopDLCh)
	}
	return nil
}

// TagStart triggers a manual tag run.
func (d *Daemon) TagStart(ctx context.Context) error {
	select {
	case d.orchCh <- orchTag:
		return nil
	default:
		return fmt.Errorf("orchestrator busy")
	}
}

// TagStop aborts the active tag run.
func (d *Daemon) TagStop(ctx context.Context) error {
	select {
	case <-d.stopTagCh:
	default:
		close(d.stopTagCh)
	}
	return nil
}

// Retag requeues items for retagging.
func (d *Daemon) Retag(ctx context.Context, mode string) (int, error) {
	if d.pipe == nil {
		return 0, fmt.Errorf("not logged in")
	}
	var n int
	var err error
	switch mode {
	case "all":
		n, err = d.store.RequeueForRetag(d.ctx)
	case "failed":
		n, err = d.store.RequeueAllFailedTags(d.ctx)
	default:
		return 0, fmt.Errorf("retag mode must be 'all' or 'failed', got %q", mode)
	}
	if err != nil {
		return 0, err
	}
	select {
	case d.orchCh <- orchTag:
	default:
	}
	return n, nil
}

// ConvertStart triggers a manual convert run.
func (d *Daemon) ConvertStart(ctx context.Context) error {
	select {
	case d.orchCh <- orchConvert:
		return nil
	default:
		return fmt.Errorf("orchestrator busy")
	}
}

// ConvertStop aborts the active convert run.
func (d *Daemon) ConvertStop(ctx context.Context) error {
	select {
	case <-d.stopConvertCh:
	default:
		close(d.stopConvertCh)
	}
	return nil
}

// Reconvert forces reconversion by mode ("all" or "failed").
func (d *Daemon) Reconvert(ctx context.Context, mode string) (int, error) {
	if d.pipe == nil {
		return 0, fmt.Errorf("not logged in")
	}
	n, err := d.pipe.ForceReconvert(d.ctx, mode)
	if err != nil {
		return 0, err
	}
	// Trigger a convert run for the newly queued items.
	select {
	case d.orchCh <- orchConvert:
	default:
	}
	return n, nil
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

// Items lists items in the given states, optionally filtered by deezer_status.
func (d *Daemon) Items(ctx context.Context, states []string, deezerStatus string, limit int) ([]store.Item, error) {
	var ptrs []*store.Item
	var err error
	if deezerStatus != "" {
		ptrs, err = d.store.ListByDeezerStatus(ctx, deezerStatus, states, limit)
	} else {
		ptrs, err = d.store.List(ctx, states, limit)
	}
	if err != nil {
		return nil, err
	}
	out := make([]store.Item, 0, len(ptrs))
	for _, it := range ptrs {
		out = append(out, *it)
	}
	return out, nil
}

// Sources lists sources of the given kinds.
func (d *Daemon) Sources(ctx context.Context, kinds []string) ([]store.Source, error) {
	srcs, err := d.store.ListSources(ctx, kinds)
	if err != nil {
		return nil, err
	}
	out := make([]store.Source, 0, len(srcs))
	for _, s := range srcs {
		out = append(out, *s)
	}
	return out, nil
}
