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

	"deebeets/internal/beets"
	"deebeets/internal/config"
	"deebeets/internal/control"
	"deebeets/internal/credentials"
	"deebeets/internal/deezer"
	"deebeets/internal/downloader"
	"deebeets/internal/store"
)

// compile-time check: the beets Runner satisfies the pipeline's Importer.
var _ downloader.Importer = (*beets.Runner)(nil)

// orchCmd is a signal sent to the orchestrator loop.
type orchCmd int

const (
	orchSync     orchCmd = iota // run sync immediately
	orchDownload                // run download immediately (with optional pre-enqueued ids)
	orchStop                    // stop the active download run
	orchSyncStop                // stop an in-progress sync
)

const metaCurrentStage = "current_stage"
const metaLastSync = "last_sync"

// Daemon owns the pipeline and control server for the process lifetime.
type Daemon struct {
	cfg   *config.Config
	log   *slog.Logger
	store *store.Store
	dz    *deezer.Client
	pipe  *downloader.Pipeline
	srv   *control.Server

	orchCh     chan orchCmd
	stopDLCh   chan struct{} // closed to signal download abort
	ctx        context.Context
	cancel     context.CancelFunc
}

// New builds a Daemon from config.
func New(cfg *config.Config, log *slog.Logger) (*Daemon, error) {
	st, err := store.Open(cfg.Paths.DBPath)
	if err != nil {
		return nil, err
	}

	arl, err := credentials.LoadARL(context.Background(), cfg.Deezer.ARL, st)
	if err != nil {
		st.Close()
		return nil, fmt.Errorf("load credentials: %w", err)
	}
	if arl == "" {
		st.Close()
		return nil, fmt.Errorf("no ARL configured — run `deebeets login`, set deezer.arl in config, or export DEEBEETS_ARL")
	}

	dz, err := deezer.New(arl)
	if err != nil {
		st.Close()
		return nil, err
	}

	runner := beets.NewRunner(cfg.Beets, cfg.PostHooks, log)
	pipe := downloader.New(st, dz, cfg, runner, log)

	d := &Daemon{
		cfg:      cfg,
		log:      log,
		store:    st,
		dz:       dz,
		pipe:     pipe,
		orchCh:   make(chan orchCmd, 4),
		stopDLCh: make(chan struct{}),
	}
	d.srv = control.NewServer(d, cfg.Paths.SocketPath)
	return d, nil
}

// Run logs in, recovers interrupted work, starts the orchestrator and control
// server, and blocks until a termination signal or ctx cancellation.
func (d *Daemon) Run(parent context.Context) error {
	ctx, cancel := signal.NotifyContext(parent, syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	d.ctx, d.cancel = ctx, cancel

	if err := d.dz.Login(ctx); err != nil {
		return fmt.Errorf("deezer login: %w", err)
	}
	d.log.Info("logged in to deezer", "user_id", d.dz.UserID(),
		"lossless", d.dz.CanStreamLossless(), "hq", d.dz.CanStreamHQ())

	// Recover any downloads interrupted by a previous crash.
	if n, err := d.store.RecoverInterrupted(ctx); err != nil {
		return err
	} else if n > 0 {
		d.log.Info("recovered interrupted downloads", "count", n)
	}
	d.pipe.CleanIncomplete()

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

// orchestrate is the single goroutine that sequences sync → download → import.
// All stage transitions are persisted to meta so a crash can resume cleanly.
func (d *Daemon) orchestrate(ctx context.Context) {
	// Resume from a prior interrupted stage if needed.
	stage, _ := d.store.GetMeta(ctx, metaCurrentStage)
	switch stage {
	case store.StageDownloading:
		d.log.Info("resuming interrupted download run")
		d.runDownload(ctx)
		return
	case store.StageImporting:
		d.log.Info("resuming interrupted import run")
		d.runImport(ctx)
	}

	// Normal loop: wait → sync → download → import → repeat.
	for {
		if ctx.Err() != nil {
			return
		}
		_ = d.store.SetMeta(ctx, metaCurrentStage, store.StageIdle)

		if d.cfg.Sync.Interval <= 0 {
			// No auto-sync: block until a manual command or shutdown.
			select {
			case cmd := <-d.orchCh:
				d.handleOrchCmd(ctx, cmd)
			case <-ctx.Done():
				return
			}
			continue
		}

		// Wait for the poll interval or an incoming command.
		timer := time.NewTimer(d.cfg.Sync.Interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
			// Interval elapsed: run sync.
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
		if d.cfg.Import.Auto {
			d.runImport(ctx)
		}
	case orchStop:
		// Handled inside runDownload via stopDLCh; nothing to do here.
	case orchSyncStop:
		// Sync is synchronous in runSync; stop is a no-op after it returns.
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
	if d.cfg.Import.Auto {
		d.runImport(ctx)
	}
}

// runSync executes the sync stage with retries. Returns true on success.
func (d *Daemon) runSync(ctx context.Context) bool {
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
			res, err = d.pipe.Sync(ctx, sel)
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
	_ = d.store.SetMeta(ctx, metaCurrentStage, store.StageDownloading)

	// Create a cancellable child context so DownloadStop can abort mid-batch.
	dlCtx, dlCancel := context.WithCancel(ctx)
	defer dlCancel()

	// Replace stopDLCh with a fresh one for this run.
	stopCh := make(chan struct{})
	d.stopDLCh = stopCh
	go func() {
		select {
		case <-stopCh:
			dlCancel()
		case <-dlCtx.Done():
		}
	}()

	if err := d.pipe.RunDownloads(dlCtx); err != nil && dlCtx.Err() == nil {
		d.log.Error("download run error", "err", err)
	}
	_ = d.store.SetMeta(ctx, metaCurrentStage, store.StageIdle)
}

// runImport runs a full-library import.
func (d *Daemon) runImport(ctx context.Context) {
	_ = d.store.SetMeta(ctx, metaCurrentStage, store.StageImporting)
	if err := d.pipe.RunImport(ctx); err != nil {
		d.log.Error("import run error", "err", err)
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
