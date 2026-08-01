// Package downloader implements the two-stage pipeline: a batched, resumable
// download stage (controllable via Start/Stop) that feeds an independent import
// stage running beets and post-hooks.
package downloader

import (
	"context"
	"log/slog"
	"path/filepath"
	"sync"
	"time"

	"deebeets/internal/config"
	"deebeets/internal/deezer"
	"deebeets/internal/store"
	"deebeets/internal/tagger"
)

// Importer runs the post-download import step (beets + post-hooks) for one
// release. albumDir is absolute; files are the absolute paths just downloaded.
type Importer interface {
	Import(ctx context.Context, albumDir string, files []string) error
}

// Daemon status meta keys/values.
const (
	MetaDownloadStatus = "download_status"
	StatusRunning      = "running"
	StatusStopped      = "stopped"
	StatusRateLimited  = "rate_limited"
	StatusHardStopped  = "rate_limit_hard_stop"
)

// Pipeline orchestrates the download and import stages over the store.
type Pipeline struct {
	store    *store.Store
	dz       *deezer.Client
	cfg      *config.Config
	fields   tagger.FieldSet
	importer Importer
	log      *slog.Logger

	musicDir      string
	incompleteDir string
	importActive  bool // whether beets/posthooks work exists

	gate *rateGate

	mu        sync.Mutex
	dlRunning bool
	dlCancel  context.CancelFunc
	dlWG      sync.WaitGroup

	importWG sync.WaitGroup
}

// New builds a Pipeline. importer may be nil when no import work is configured.
func New(st *store.Store, dz *deezer.Client, cfg *config.Config, importer Importer, log *slog.Logger) *Pipeline {
	importActive := importer != nil && (cfg.Beets.Enabled || len(cfg.PostHooks) > 0)
	return &Pipeline{
		store:         st,
		dz:            dz,
		cfg:           cfg,
		fields:        tagger.NewFieldSet(cfg.Tags.Fields),
		importer:      importer,
		log:           log,
		musicDir:      cfg.Paths.MusicDir,
		incompleteDir: filepath.Join(cfg.Paths.MusicDir, ".deebeets-incomplete"),
		importActive:  importActive,
		gate: newRateGate(cfg.RateLimit.Cooldown, cfg.RateLimit.Window, cfg.RateLimit.MaxHits),
	}
}

// ImportActive reports whether downloaded items should flow to the import stage.
func (p *Pipeline) ImportActive() bool { return p.importActive }

// DownloadRunning reports whether the download stage is currently active.
func (p *Pipeline) DownloadRunning() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.dlRunning
}

// StartDownload begins draining the download queue in batches. It is idempotent.
func (p *Pipeline) StartDownload(parent context.Context) {
	p.mu.Lock()
	if p.dlRunning {
		p.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(parent)
	p.dlRunning = true
	p.dlCancel = cancel
	p.dlWG.Add(1)
	p.mu.Unlock()

	_ = p.store.SetMeta(ctx, MetaDownloadStatus, StatusRunning)
	go func() {
		defer p.dlWG.Done()
		p.downloadStage(ctx)
	}()
}

// StopDownload stops the download stage, waits for in-flight work to unwind, and
// reverts any interrupted rows so they resume on the next start.
func (p *Pipeline) StopDownload() {
	p.mu.Lock()
	if !p.dlRunning {
		p.mu.Unlock()
		return
	}
	cancel := p.dlCancel
	p.dlRunning = false
	p.mu.Unlock()

	cancel()
	p.dlWG.Wait()
	_, _ = p.store.RecoverInterrupted(context.Background())
	_ = p.store.SetMeta(context.Background(), MetaDownloadStatus, StatusStopped)
}

// downloadStage runs batches until the context is cancelled. Each batch claims
// up to Concurrency tracks, runs them in parallel, waits for all to finish, then
// optionally pauses before the next batch.
func (p *Pipeline) downloadStage(ctx context.Context) {
	const idlePoll = 3 * time.Second
	for {
		if ctx.Err() != nil {
			return
		}

		// Respect the rate-limit gate before dispatching a new batch.
		if wait, hard := p.gate.blockedFor(); hard {
			_ = p.store.SetMeta(ctx, MetaDownloadStatus, StatusHardStopped)
			p.log.Warn("rate limit hard stop: pausing download stage to protect the account")
			return
		} else if wait > 0 {
			_ = p.store.SetMeta(ctx, MetaDownloadStatus, StatusRateLimited)
			if !sleepCtx(ctx, wait) {
				return
			}
			_ = p.store.SetMeta(ctx, MetaDownloadStatus, StatusRunning)
			continue
		}

		batch := p.claimBatch(ctx)
		if len(batch) == 0 {
			// Nothing queued: optionally requeue deferred failures once, else idle.
			if p.requeueDeferred(ctx) {
				continue
			}
			if !sleepCtx(ctx, idlePoll) {
				return
			}
			continue
		}

		var wg sync.WaitGroup
		for _, it := range batch {
			wg.Add(1)
			go func(item *store.Item) {
				defer wg.Done()
				p.downloadOne(ctx, item)
			}(it)
		}
		wg.Wait()

		if d := p.cfg.Download.InterBatchDelay; d > 0 {
			if !sleepCtx(ctx, d) {
				return
			}
		}
	}
}

// claimBatch claims up to Concurrency tracks for this batch.
func (p *Pipeline) claimBatch(ctx context.Context) []*store.Item {
	n := p.cfg.Download.Concurrency
	batch := make([]*store.Item, 0, n)
	for i := 0; i < n; i++ {
		it, ok, err := p.store.ClaimDownload(ctx)
		if err != nil {
			p.log.Error("claim download", "err", err)
			break
		}
		if !ok {
			break
		}
		batch = append(batch, it)
	}
	return batch
}

// requeueDeferred re-queues download-stage failures for a final pass when the
// retry mode allows it. Returns true if anything was requeued.
func (p *Pipeline) requeueDeferred(ctx context.Context) bool {
	if !deferredEnabled(p.cfg.Retry.Mode) {
		return false
	}
	failed, err := p.store.List(ctx, []string{store.StateFailed}, 0)
	if err != nil {
		return false
	}
	var ids []int64
	for _, it := range failed {
		if it.Stage == store.StageDownload && it.Attempts <= p.cfg.Retry.MaxAttempts {
			ids = append(ids, it.SngID)
		}
	}
	if len(ids) == 0 {
		return false
	}
	n, err := p.store.Requeue(ctx, ids, false)
	if err != nil {
		p.log.Error("requeue deferred", "err", err)
		return false
	}
	p.log.Info("requeued deferred failures for a final pass", "count", n)
	return n > 0
}

// sleepCtx sleeps for d or until ctx is cancelled. Returns false if cancelled.
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
