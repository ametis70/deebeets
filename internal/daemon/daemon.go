// Package daemon wires the store, Deezer client, pipeline and control server
// into a long-running process, and implements the control.Controller API.
package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"os/signal"
	"syscall"
	"time"

	"deeznt/internal/config"
	"deeznt/internal/control"
	"deeznt/internal/credentials"
	"deeznt/internal/deezer"
	"deeznt/internal/downloader"
	"deeznt/internal/notify"
	"deeznt/internal/store"
)

// orchCmd is a signal sent to the orchestrator loop.
type orchCmd int

const (
	orchSync        orchCmd = iota // run sync immediately
	orchDownload                   // run download immediately
	orchTag                        // run tag immediately
	orchConvert                    // run convert immediately
	orchStop                       // stop the active download run
	orchSyncStop                   // stop an in-progress sync
	orchTagStop                    // stop the active tag run
	orchConvertStop                // stop the active convert run
)

const metaCurrentStage = "current_stage"
const metaLastSync = "last_sync"

// Daemon owns the pipeline and control server for the process lifetime.
type Daemon struct {
	cfg      *config.Config
	log      *slog.Logger
	store    *store.Store
	dz       *deezer.Client      // nil until connectDeezer succeeds
	pipe     *downloader.Pipeline // nil until connectDeezer succeeds
	notifier *notify.Notifier
	srv      *control.Server

	orchCh         chan orchCmd
	stopDLCh       chan struct{} // closed to signal download abort
	stopTagCh      chan struct{} // closed to signal tag abort
	stopConvertCh  chan struct{} // closed to signal convert abort
	syncRefresh    bool
	ctx         context.Context
	cancel      context.CancelFunc
}

// New builds a Daemon from config.
func New(cfg *config.Config, log *slog.Logger) (*Daemon, error) {
	st, err := store.Open(cfg.Paths.DBPath)
	if err != nil {
		return nil, err
	}

	d := &Daemon{
		cfg:       cfg,
		log:       log,
		store:     st,
		notifier:  notify.New(cfg.Notifications, log),
		orchCh:        make(chan orchCmd, 4),
		stopDLCh:      make(chan struct{}),
		stopTagCh:     make(chan struct{}),
		stopConvertCh: make(chan struct{}),
	}
	d.srv = control.NewServer(d, cfg.Paths.SocketPath)
	return d, nil
}

// Run starts the control socket and orchestrator, blocking until shutdown.
// The daemon starts even with no ARL configured; sync/download will fail until
// `deeznt login` is run.
func (d *Daemon) Run(parent context.Context) error {
	ctx, cancel := signal.NotifyContext(parent, syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	d.ctx, d.cancel = ctx, cancel

	// Best-effort: connect to Deezer if credentials are already stored.
	if err := d.connectDeezer(ctx); err != nil {
		d.log.Warn("deezer not connected (run `deeznt login`)", "err", err)
	}

	if n, err := d.store.RecoverInterrupted(ctx); err != nil {
		return err
	} else if n > 0 {
		d.log.Info("recovered interrupted downloads", "count", n)
	}
	if d.pipe != nil {
		d.pipe.CleanIncomplete()
	}

	if err := d.srv.Listen(); err != nil {
		return fmt.Errorf("control socket: %w", err)
	}
	d.log.Info("control socket listening", "path", d.cfg.Paths.SocketPath)

	srvErr := make(chan error, 1)
	go func() { srvErr <- d.srv.Serve() }()
	go d.orchestrate(ctx)

	select {
	case <-ctx.Done():
		d.log.Info("shutting down")
	case err := <-srvErr:
		if err != nil {
			d.log.Error("control server error", "err", err)
		}
	}

	return d.shutdown()
}

func (d *Daemon) shutdown() error {
	shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = d.srv.Close(shutCtx)
	return d.store.Close()
}

// Close releases resources for a daemon that never ran (error path).
func (d *Daemon) Close() {
	if d.store != nil {
		_ = d.store.Close()
	}
}

// connectDeezer loads the ARL, creates the Deezer client, logs in, and
// initialises the download pipeline. Idempotent: a no-op if already connected.
func (d *Daemon) connectDeezer(ctx context.Context) error {
	if d.pipe != nil {
		return nil
	}
	arl, err := credentials.LoadARL(ctx, d.cfg.Deezer.ARL, d.store)
	if err != nil {
		return fmt.Errorf("load credentials: %w", err)
	}
	if arl == "" {
		return fmt.Errorf("no ARL configured — run `deeznt login`")
	}
	dz, err := deezer.New(arl)
	if err != nil {
		return err
	}
	if err := dz.Login(ctx); err != nil {
		return fmt.Errorf("deezer login: %w", err)
	}
	d.log.Info("logged in to deezer", "user_id", dz.UserID(),
		"lossless", dz.CanStreamLossless(), "hq", dz.CanStreamHQ())
	d.dz = dz
	d.pipe = downloader.New(d.store, dz, d.cfg, d.log)
	return nil
}

// orchestrate sequences sync → download → convert → repeat.
func (d *Daemon) orchestrate(ctx context.Context) {
	// Resume from a prior interrupted stage.
	stage, _ := d.store.GetMeta(ctx, metaCurrentStage)
	switch stage {
	case store.StageDownloading:
		d.log.Info("resuming interrupted download run")
		d.runDownload(ctx)
		if d.cfg.Tag.Auto {
			d.runTag(ctx)
		}
		if d.cfg.Convert.Auto {
			go d.runConvert(ctx)
		}
	case store.StageTagging:
		d.log.Info("resuming interrupted tag run")
		d.runTag(ctx)
		if d.cfg.Convert.Auto {
			go d.runConvert(ctx)
		}
	case store.StageConverting, store.StageImporting:
		d.log.Info("resuming interrupted convert run")
		go d.runConvert(ctx)
	}

	for {
		if ctx.Err() != nil {
			return
		}
		_ = d.store.SetMeta(ctx, metaCurrentStage, store.StageIdle)

		if d.cfg.Sync.Interval <= 0 {
			select {
			case cmd := <-d.orchCh:
				d.handleOrchCmd(ctx, cmd)
			case <-ctx.Done():
				return
			}
			continue
		}

		timer := time.NewTimer(d.cfg.Sync.Interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		case cmd := <-d.orchCh:
			timer.Stop()
			d.handleOrchCmd(ctx, cmd)
			continue
		}
		timer.Stop()

		d.runSyncThenDownload(ctx)
	}
}

func (d *Daemon) handleOrchCmd(ctx context.Context, cmd orchCmd) {
	switch cmd {
	case orchSync:
		d.runSyncThenDownload(ctx)
	case orchDownload:
		d.runDownload(ctx)
		if d.cfg.Tag.Auto {
			d.runTag(ctx)
		}
		if d.cfg.Convert.Auto {
			go d.runConvert(ctx)
		}
	case orchTag:
		d.runTag(ctx)
		if d.cfg.Convert.Auto {
			go d.runConvert(ctx)
		}
	case orchConvert:
		go d.runConvert(ctx)
	case orchStop, orchSyncStop, orchTagStop, orchConvertStop:
		// handled via stop channels / no-op
	}
}

func (d *Daemon) runSyncThenDownload(ctx context.Context) {
	if !d.runSync(ctx) {
		return
	}
	if !d.cfg.Download.Auto {
		return
	}
	d.runDownload(ctx)
	if d.cfg.Tag.Auto {
		d.runTag(ctx)
	}
	if d.cfg.Convert.Auto {
		go d.runConvert(ctx)
	}
}

// runSync executes the sync stage with retries. Returns true on success.
func (d *Daemon) runSync(ctx context.Context) bool {
	if err := d.connectDeezer(ctx); err != nil {
		d.log.Error("cannot sync: not logged in", "err", err)
		_ = d.store.SetMeta(ctx, metaCurrentStage, store.StageIdle)
		return false
	}
	_ = d.store.SetMeta(ctx, metaCurrentStage, store.StageSyncing)

	sel := d.syncSelection()
	var lastErr error
	for attempt := 1; attempt <= d.cfg.Sync.Retry.MaxAttempts; attempt++ {
		if ctx.Err() != nil {
			return false
		}

		var res downloader.SyncResult
		var err error

		if len(d.cfg.FixtureAlbums) > 0 {
			n, ferr := d.pipe.EnqueueIDs(ctx, deezer.KindAlbum, d.cfg.FixtureAlbums)
			res.New, res.Total = n, n
			err = ferr
		} else {
			res, err = d.pipe.Sync(ctx, sel, d.syncRefresh)
		}

		if err == nil {
			stamp := fmt.Sprintf("%s (new %d / seen %d)", nowStamp(), res.New, res.Total)
			_ = d.store.SetMeta(ctx, metaLastSync, stamp)
			d.log.Info("sync complete", "new", res.New, "seen", res.Total)
			return true
		}

		lastErr = err
		d.log.Warn("sync attempt failed", "attempt", attempt,
			"max", d.cfg.Sync.Retry.MaxAttempts, "err", err)
		if attempt < d.cfg.Sync.Retry.MaxAttempts {
			if !sleepCtx(ctx, d.cfg.Sync.Retry.Backoff) {
				return false
			}
		}
	}

	d.log.Error("sync failed after all retries", "err", lastErr)
	_ = d.store.SetMeta(ctx, metaCurrentStage, store.StageIdle)
	return false
}

// runDownload runs the download stage to completion.
func (d *Daemon) runDownload(ctx context.Context) {
	if err := d.connectDeezer(ctx); err != nil {
		d.log.Error("cannot download: not logged in", "err", err)
		_ = d.store.SetMeta(ctx, metaCurrentStage, store.StageIdle)
		return
	}
	_ = d.store.SetMeta(ctx, metaCurrentStage, store.StageDownloading)

	dlCtx, dlCancel := context.WithCancel(ctx)
	defer dlCancel()

	stopCh := make(chan struct{})
	d.stopDLCh = stopCh
	go func() {
		select {
		case <-stopCh:
			dlCancel()
		case <-dlCtx.Done():
		}
	}()

	// Notify: downloads starting with total queued count.
	counts, _ := d.store.CountByState(dlCtx)
	queued := counts[store.StateQueued] + counts[store.StateWaiting]
	d.notifier.Send(notify.EventDownloadsStarted, map[string]any{
		"queued": queued,
	})

	res, err := d.pipe.RunDownloads(dlCtx)
	if err != nil && dlCtx.Err() == nil {
		d.log.Error("download run error", "err", err)
	}

	// Notify: downloads finished / failed.
	if res.Downloaded > 0 || res.Failed == 0 {
		d.notifier.Send(notify.EventDownloadsFinished, map[string]any{
			"downloaded": res.Downloaded,
			"failed":     res.Failed,
		})
	}
	if res.Failed > 0 {
		d.notifier.Send(notify.EventDownloadsFailed, map[string]any{
			"failed": res.Failed,
		})
	}

	_ = d.store.SetMeta(ctx, metaCurrentStage, store.StageIdle)
}

// runTag runs the tag stage to completion.
func (d *Daemon) runTag(ctx context.Context) {
	if d.pipe == nil {
		return
	}
	_ = d.store.SetMeta(ctx, metaCurrentStage, store.StageTagging)

	tagCtx, tagCancel := context.WithCancel(ctx)
	defer tagCancel()

	stopCh := make(chan struct{})
	d.stopTagCh = stopCh
	go func() {
		select {
		case <-stopCh:
			tagCancel()
		case <-tagCtx.Done():
		}
	}()

	res, err := d.pipe.RunTag(tagCtx)
	if err != nil && tagCtx.Err() == nil {
		d.log.Error("tag run error", "err", err)
	}
	d.log.Info("tag run complete", "tagged", res.Tagged, "failed", res.Failed)
	_ = d.store.SetMeta(ctx, metaCurrentStage, store.StageIdle)
}

// runConvert runs a full convert pass over tagged files.
func (d *Daemon) runConvert(ctx context.Context) {
	if d.pipe == nil || !d.cfg.Convert.Enabled {
		return
	}
	_ = d.store.SetMeta(ctx, metaCurrentStage, store.StageImporting)

	convCtx, convCancel := context.WithCancel(ctx)
	defer convCancel()

	stopCh := make(chan struct{})
	d.stopConvertCh = stopCh
	go func() {
		select {
		case <-stopCh:
			convCancel()
		case <-convCtx.Done():
		}
	}()

	res, err := d.pipe.RunConvert(convCtx)
	if err != nil {
		d.log.Error("convert run error", "err", err)
	}
	if res.Converted > 0 {
		d.notifier.Send(notify.EventConvertsFinished, map[string]any{
			"converted": res.Converted,
			"failed":    res.Failed,
		})
	}
	if res.Failed > 0 {
		d.notifier.Send(notify.EventConvertsFailed, map[string]any{
			"failed": res.Failed,
		})
	}
	_ = d.store.SetMeta(ctx, metaCurrentStage, store.StageIdle)
}

func (d *Daemon) syncSelection() deezer.Selection {
	f := d.cfg.Download.Favorites
	return deezer.Selection{
		Albums:    f.Albums,
		Artists:   f.Artists,
		Playlists: f.Playlists,
		Tracks:    f.Tracks,
	}
}

func nowStamp() string {
	return time.Now().Format(time.RFC3339)
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
