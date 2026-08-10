// Package downloader implements the two-stage pipeline: a one-shot download
// run that drains the queue with batch-level retries, followed by an optional
// one-shot convert run that transcodes downloaded files via ffmpeg.
package downloader

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"deeznt/internal/config"
	"deeznt/internal/converter"
	"deeznt/internal/deezer"
	"deeznt/internal/store"
	"deeznt/internal/tagger"
)

// Daemon status meta keys/values.
const (
	MetaRateLimitUntil = "rate_limit_until"
)

// Pipeline orchestrates the download and convert runs over the store.
type Pipeline struct {
	store    *store.Store
	dz       *deezer.Client
	cfg      *config.Config
	fields   tagger.FieldSet
	conv     *converter.Runner // nil if convert disabled
	log      *slog.Logger

	musicDir      string
	incompleteDir string

	gate *rateGate

	convertRunning  atomic.Bool
	convertingCount atomic.Int32 // background batch conversions in flight
}

// New builds a Pipeline.
func New(st *store.Store, dz *deezer.Client, cfg *config.Config, log *slog.Logger) *Pipeline {
	var conv *converter.Runner
	if cfg.Convert.Enabled {
		conv = converter.New(cfg.Convert, cfg.Paths.MusicDir, log)
	}
	return &Pipeline{
		store:         st,
		dz:            dz,
		cfg:           cfg,
		fields:        tagger.NewFieldSet(cfg.Tags.Fields),
		conv:          conv,
		log:           log,
		musicDir:      cfg.Paths.MusicDir,
		incompleteDir: filepath.Join(cfg.Paths.MusicDir, ".deeznt-incomplete"),
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
				return nil
			}

			batchPass++
			if batchPass > p.cfg.Download.Retry.MaxAttempts {
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
			if !sleepCtx(ctx, p.cfg.Download.Retry.Backoff) {
				return ctx.Err()
			}
			continue
		}

		// Download the batch in parallel; collect per-item results.
		type result struct {
			item *store.Item
			err  error
			job  *converter.ConvertJob // non-nil on success, for conversion
		}
		results := make([]result, len(batch))
		var wg sync.WaitGroup
		for i, it := range batch {
			wg.Add(1)
			go func(idx int, item *store.Item) {
				defer wg.Done()
				job, err := p.downloadOne(ctx, item)
				results[idx] = result{item: item, err: err, job: job}
			}(i, it)
		}
		wg.Wait()

		// Process results.
		var failedIDs []int64
		var failedErrs []string
		var convertJobs []converter.ConvertJob

		for _, r := range results {
			if r.err == nil {
				if r.job != nil {
					convertJobs = append(convertJobs, *r.job)
				}
				continue
			}
			if deezer.IsRateLimited(r.err) {
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

		for i, id := range failedIDs {
			_ = p.store.MarkInFailedBatch(ctx, []int64{id}, store.StageDownload, failedErrs[i])
		}

		// Convert successful downloads in the background so the next download
		// batch starts immediately. The converter's own concurrency limit
		// (convert.concurrency) bounds how many ffmpeg processes run at once.
		if p.conv != nil && len(convertJobs) > 0 {
			jobs := convertJobs
			p.convertingCount.Add(int32(len(jobs)))
			go func() {
				defer p.convertingCount.Add(-int32(len(jobs)))
				converted, failed := p.conv.RunAll(ctx, jobs)
				for _, id := range converted {
					_ = p.store.MarkFinished(ctx, id)
				}
				for id, errMsg := range failed {
					p.log.Warn("conversion failed", "sng_id", id, "err", errMsg)
					_ = p.store.MarkFailed(ctx, id, store.StageConvert, errMsg)
				}
			}()
		}

		if d := p.cfg.Download.InterBatchDelay; d > 0 {
			if !sleepCtx(ctx, d) {
				return ctx.Err()
			}
		}
	}
}

// RunConvert converts files in state=downloaded (pending conversion) plus any
// finished items that are missing their converted copy. Marks downloaded items
// as finished after successful conversion. Guards against concurrent runs.
func (p *Pipeline) RunConvert(ctx context.Context) error {
	if p.conv == nil {
		return nil
	}
	if !p.convertRunning.CompareAndSwap(false, true) {
		p.log.Warn("convert already running; skipping concurrent trigger")
		return nil
	}
	defer p.convertRunning.Store(false)

	// Collect items that need conversion: state=downloaded (not yet converted)
	// and state=finished where the converted file is missing.
	downloaded, err := p.store.List(ctx, []string{store.StateDownloaded}, 0)
	if err != nil {
		return err
	}
	finished, err := p.store.FinishedItems(ctx)
	if err != nil {
		return err
	}

	var jobs []converter.ConvertJob
	for _, it := range append(downloaded, finished...) {
		if it.FilePath == "" {
			continue
		}
		srcPath := filepath.Join(p.musicDir, it.FilePath)
		if _, err := os.Stat(srcPath); err != nil {
			continue
		}
		outPath, err := p.conv.OutputPath(srcPath)
		if err != nil || outPath == "" {
			continue
		}
		if _, err := os.Stat(outPath); err == nil {
			// Already converted — if still downloaded, mark finished.
			if it.State == store.StateDownloaded {
				_ = p.store.MarkFinished(ctx, it.SngID)
			}
			continue
		}
		jobs = append(jobs, converter.ConvertJob{SngID: it.SngID, SourcePath: srcPath, CopyTagsFromSource: true})
	}

	if len(jobs) == 0 {
		p.log.Info("convert: nothing to do")
		return nil
	}
	p.log.Info("starting convert run", "count", len(jobs))
	converted, failed := p.conv.RunAll(ctx, jobs)
	for _, id := range converted {
		_ = p.store.MarkFinished(ctx, id)
	}
	for id, errMsg := range failed {
		p.log.Warn("conversion failed", "sng_id", id, "err", errMsg)
		_ = p.store.MarkFailed(ctx, id, store.StageConvert, errMsg)
	}
	return nil
}

// ForceReconvert prepares items for reconversion.
// mode="all": deletes existing converted files for all finished items and resets
//             them to state=downloaded so RunConvert will pick them up.
// mode="failed": retries only items currently stuck at state=downloaded
//                (conversion previously failed or was skipped).
// Returns the number of items queued for reconversion.
func (p *Pipeline) ForceReconvert(ctx context.Context, mode string) (int, error) {
	if p.conv == nil {
		return 0, fmt.Errorf("convert is not enabled")
	}

	switch mode {
	case "all":
		items, err := p.store.FinishedItems(ctx)
		if err != nil {
			return 0, err
		}
		var n int
		for _, it := range items {
			if it.FilePath == "" {
				continue
			}
			srcPath := filepath.Join(p.musicDir, it.FilePath)
			outPath, err := p.conv.OutputPath(srcPath)
			if err != nil || outPath == "" {
				continue
			}
			// Delete the existing converted file so RunConvert re-creates it.
			_ = os.Remove(outPath)
			// Reset to downloaded so the item is picked up by RunConvert.
			if err := p.store.SetState(ctx, it.SngID, store.StateDownloaded); err != nil {
				return n, err
			}
			n++
		}
		return n, nil

	case "failed":
		// Items at state=downloaded are pending or failed conversion.
		items, err := p.store.List(ctx, []string{store.StateDownloaded}, 0)
		if err != nil {
			return 0, err
		}
		return len(items), nil // already in the right state, RunConvert will retry

	default:
		return 0, fmt.Errorf("reconvert mode must be 'all' or 'failed', got %q", mode)
	}
}

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

// waitRateLimit checks the persisted rate_limit_until meta key and sleeps if needed.
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

// ConvertingCount returns the number of files currently being converted.
func (p *Pipeline) ConvertingCount() int {
	return int(p.convertingCount.Load()) + func() int {
		if p.convertRunning.Load() {
			return 1
		}
		return 0
	}()
}

// CleanIncomplete deletes any orphaned partial files in the incomplete dir.
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
