// Package daemon wires the store, Deezer client, pipeline and control server
// into a long-running process, and implements the control.Controller API.
package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"os/signal"
	"sync"
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

// Daemon owns the pipeline and control server for the process lifetime.
type Daemon struct {
	cfg   *config.Config
	log   *slog.Logger
	store *store.Store
	dz    *deezer.Client
	pipe  *downloader.Pipeline
	imp   *beets.Runner
	srv   *control.Server

	ctx    context.Context
	cancel context.CancelFunc

	syncMu  sync.Mutex
	syncing bool
}

// New builds a Daemon from config.
func New(cfg *config.Config, log *slog.Logger) (*Daemon, error) {
	st, err := store.Open(cfg.Paths.DBPath)
	if err != nil {
		return nil, err
	}

	arl, err := credentials.LoadARL(context.Background(), cfg.Deezer.ARL, cfg.Paths.DBPath, st)
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
	imp := beets.NewRunner(cfg.Beets, cfg.PostHooks, log)
	pipe := downloader.New(st, dz, cfg, imp, log)

	d := &Daemon{cfg: cfg, log: log, store: st, dz: dz, pipe: pipe, imp: imp}
	d.srv = control.NewServer(d, cfg.Paths.SocketPath)
	return d, nil
}

// Run logs in, recovers interrupted work, starts the pipeline and control
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

	if n, err := d.store.RecoverInterrupted(ctx); err != nil {
		return err
	} else if n > 0 {
		d.log.Info("recovered interrupted items", "count", n)
	}

	// Import stage runs for the whole lifetime; download stage starts draining.
	d.pipe.StartImport(ctx)
	d.pipe.StartDownload(ctx)

	if err := d.srv.Listen(); err != nil {
		return fmt.Errorf("control socket: %w", err)
	}
	d.log.Info("control socket listening", "path", d.cfg.Paths.SocketPath)

	srvErr := make(chan error, 1)
	go func() { srvErr <- d.srv.Serve() }()

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
	d.pipe.StopDownload()
	d.pipe.WaitImport()
	return d.store.Close()
}

// Close releases resources for a daemon that never ran (error path).
func (d *Daemon) Close() {
	if d.store != nil {
		_ = d.store.Close()
	}
}

func nowStamp() string {
	return time.Now().Format(time.RFC3339)
}
