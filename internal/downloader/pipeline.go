// Package downloader implements the two-stage pipeline: a one-shot download
// run that drains the queue with batch-level retries, followed by an optional
// one-shot import run that calls beet import against the full music library.
package downloader

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"deebeets/internal/config"
	"deebeets/internal/deezer"
	"deebeets/internal/store"
	"deebeets/internal/tagger"
)

// Importer runs the post-download import step (beets + post-hooks) against the
// full music library directory.
type Importer interface {
	Import(ctx context.Context, musicDir string) error
}

// Daemon status meta keys/values.
const (
	MetaRateLimitUntil = "rate_limit_until"
)

// Pipeline orchestrates the download and import runs over the store.
type Pipeline struct {
	store    *store.Store
	dz       *deezer.Client
	cfg      *config.Config
	fields   tagger.FieldSet
	importer Importer
	log      *slog.Logger

	musicDir      string
	incompleteDir string

	gate *rateGate

	importRunning atomic.Bool
}

// New builds a Pipeline. importer may be nil when no import work is configured.
func New(st *store.Store, dz *deezer.Client, cfg *config.Config, importer Importer, log *slog.Logger) *Pipeline {
	return &Pipeline{
		store:         st,
		dz:            dz,
		cfg:           cfg,
		fields:        tagger.NewFieldSet(cfg.Tags.Fields),
		importer:      importer,
		log:           log,
		musicDir:      cfg.Paths.MusicDir,
		incompleteDir: filepath.Join(cfg.Paths.MusicDir, ".deebeets-incomplete"),
		gate:          newRateGate(cfg.RateLimit.Cooldown, cfg.RateLimit.Window, cfg.RateLimit.MaxHits),
	}
}

// RunDownloads drains the download queue in batches with batch-level retries.
// It runs to completion (or until ctx is cancelled) and returns when the queue
// is empty and all retry passes are exhausted.
func (p *Pipeline) RunDownloads(ctx context.Context) error {
	// Wait out any persisted rate-limit backoff before starting.
	if err := p.waitRateLimit(ctx); err != nil {
		return err
	}

	batchPass := 0
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		// Check the in-memory rate gate (hard stop or cooldown).
		if wait, hard := p.gate.blockedFor(); hard {
			p.log.Warn("rate limit hard stop: aborting download run")
			return nil
		} else if wait > 0 {
			p.log.Info("rate limited: waiting", "duration", wait)
			p.persistRateLimitUntil(ctx, time.Now().Add(wait))
			if !sleepCtx(ctx, wait) {
				return ctx.Err()
			}
			p.persistRateLimitUntil(ctx, time.Time{})
			continue
		}

		batch := p.claimBatch(ctx)
		if len(batch) == 0 {
			// Queue empty — attempt batch retry pass if eligible.
			failed, err := p.store.ClaimFailedBatch(ctx)
			if err != nil {
				return err
			}
			if len(failed) == 0 {
				// Nothing left to do.
				return nil
			}

			batchPass++
			if batchPass > p.cfg.Download.Retry.MaxAttempts {
				// Permanently fail everything still in the failed batch.
				ids := itemIDs(failed)
				p.log.Warn("batch retry exhausted: marking permanently failed", "count", len(ids))
				for _, it := range failed {
					_ = p.store.MarkFailed(ctx, it.SngID, store.StageDownload, it.Error)
				}
				return nil
			}

			ids := itemIDs(failed)
			p.log.Info("requeueing failed batch for retry", "pass", batchPass, "count", len(ids))
			if err := p.store.RequeueFailedBatch(ctx, ids); err != nil {
				return err
			}
			// Sleep the configured backoff before the retry pass.
			if !sleepCtx(ctx, p.cfg.Download.Retry.Backoff) {
				return ctx.Err()
			}
			continue
		}

		// Download the batch in parallel; collect per-item results.
		type result struct {
			item *store.Item
			err  error
		}
		results := make([]result, len(batch))
		var wg sync.WaitGroup
		for i, it := range batch {
			wg.Add(1)
			go func(idx int, item *store.Item) {
				defer wg.Done()
				results[idx] = result{item: item, err: p.downloadOne(ctx, item)}
			}(i, it)
		}
		wg.Wait()

		// Process results: successes, rate limits, and failures.
		var failedIDs []int64
		var failedErrs []string
		for _, r := range results {
			if r.err == nil {
				continue
			}
			if deezer.IsRateLimited(r.err) {
				// Return to queue; the gate will be hit next iteration.
				_ = p.store.SetState(ctx, r.item.SngID, store.StateQueued)
				wait, hard := p.gate.hit()
				if hard {
					p.log.Warn("rate limit hard stop hit", "sng_id", r.item.SngID)
				} else {
					p.log.Warn("rate limited", "sng_id", r.item.SngID, "backoff", wait)
					p.persistRateLimitUntil(ctx, time.Now().Add(wait))
				}
				continue
			}
			p.log.Error("download failed", "sng_id", r.item.SngID, "err", r.err)
			failedIDs = append(failedIDs, r.item.SngID)
			failedErrs = append(failedErrs, r.err.Error())
		}

		// Mark all failures as in-failed-batch in one pass.
		for i, id := range failedIDs {
			_ = p.store.MarkInFailedBatch(ctx, []int64{id}, store.StageDownload, failedErrs[i])
		}

		if d := p.cfg.Download.InterBatchDelay; d > 0 {
			if !sleepCtx(ctx, d) {
				return ctx.Err()
			}
		}
	}
}

// RunImport runs beet import against the full music directory. It is a no-op
// if no importer is configured. It guards against concurrent runs.
func (p *Pipeline) RunImport(ctx context.Context) error {
	if p.importer == nil {
		return nil
	}
	if !p.importRunning.CompareAndSwap(false, true) {
		p.log.Warn("import already running; skipping concurrent trigger")
		return nil
	}
	defer p.importRunning.Store(false)

	p.log.Info("starting import", "music_dir", p.musicDir)
	if err := p.importer.Import(ctx, p.musicDir); err != nil {
		p.log.Error("import failed", "err", err)
		return err
	}
	p.log.Info("import complete")
	return p.store.MarkAllDownloadedFinished(ctx)
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

// waitRateLimit checks the persisted rate_limit_until meta key and sleeps if
// a prior run was rate limited and hasn't expired yet.
func (p *Pipeline) waitRateLimit(ctx context.Context) error {
	raw, _ := p.store.GetMeta(ctx, MetaRateLimitUntil)
	if raw == "" {
		return nil
	}
	var until time.Time
	if err := until.UnmarshalText([]byte(raw)); err != nil {
		return nil
	}
	if d := time.Until(until); d > 0 {
		p.log.Info("waiting out persisted rate limit backoff", "duration", d)
		if !sleepCtx(ctx, d) {
			return ctx.Err()
		}
	}
	_ = p.store.SetMeta(ctx, MetaRateLimitUntil, "")
	return nil
}

func (p *Pipeline) persistRateLimitUntil(ctx context.Context, t time.Time) {
	if t.IsZero() {
		_ = p.store.SetMeta(ctx, MetaRateLimitUntil, "")
		return
	}
	b, _ := t.MarshalText()
	_ = p.store.SetMeta(ctx, MetaRateLimitUntil, string(b))
}

// CleanIncomplete deletes any orphaned partial files in the incomplete dir.
// Called on startup after RecoverInterrupted.
func (p *Pipeline) CleanIncomplete() {
	entries, err := os.ReadDir(p.incompleteDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() {
			_ = os.Remove(filepath.Join(p.incompleteDir, e.Name()))
		}
	}
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

func itemIDs(items []*store.Item) []int64 {
	ids := make([]int64, len(items))
	for i, it := range items {
		ids[i] = it.SngID
	}
	return ids
}
