// Package downloader implements the two-stage pipeline: a one-shot download
// run that drains the queue with batch-level retries, followed by an optional
// one-shot convert run that transcodes downloaded files via ffmpeg.
package downloader

import (
	"context"
	"encoding/json"
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

// DownloadResult summarises a completed download run.
type DownloadResult struct {
	Downloaded int // tracks successfully downloaded
	Failed     int // tracks permanently failed
}

// RunDownloads drains the download queue in batches with batch-level retries.
// It runs to completion (or until ctx is cancelled) and returns when the queue
// is empty and all retry passes are exhausted.
func (p *Pipeline) RunDownloads(ctx context.Context) (DownloadResult, error) {
	var res DownloadResult
	// Wait out any persisted rate-limit backoff before starting.
	if err := p.waitRateLimit(ctx); err != nil {
		return res, err
	}

	batchPass := 0
	for {
		if ctx.Err() != nil {
			return res, ctx.Err()
		}

		// Check the in-memory rate gate (hard stop or cooldown).
		if wait, hard := p.gate.blockedFor(); hard {
			p.log.Warn("rate limit hard stop: aborting download run")
			return res, nil
		} else if wait > 0 {
			p.log.Info("rate limited: waiting", "duration", wait)
			p.persistRateLimitUntil(ctx, time.Now().Add(wait))
			if !sleepCtx(ctx, wait) {
				return res, ctx.Err()
			}
			p.persistRateLimitUntil(ctx, time.Time{})
			continue
		}

		batch := p.claimBatch(ctx)
		if len(batch) == 0 {
			// Queue empty — attempt batch retry pass if eligible.
			failed, err := p.store.ClaimFailedBatch(ctx)
			if err != nil {
				return res, err
			}
			if len(failed) == 0 {
				return res, nil
			}

			batchPass++
			if batchPass > p.cfg.Download.Retry.MaxAttempts {
				ids := itemIDs(failed)
				p.log.Warn("batch retry exhausted: marking permanently failed", "count", len(ids))
				for _, it := range failed {
					_ = p.store.MarkFailedDownload(ctx, it.SngID, it.Error)
				}
				res.Failed += len(ids)
				return res, nil
			}

			ids := itemIDs(failed)
			p.log.Info("requeueing failed batch for retry", "pass", batchPass, "count", len(ids))
			if err := p.store.RequeueFailedBatch(ctx, ids); err != nil {
				return res, err
			}
			if !sleepCtx(ctx, p.cfg.Download.Retry.Backoff) {
				return res, ctx.Err()
			}
			continue
		}

		// Download the batch in parallel.
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
				err := p.downloadOne(ctx, item)
				results[idx] = result{item: item, err: err}
			}(i, it)
		}
		wg.Wait()

		var failedIDs []int64
		var failedErrs []string

		for _, r := range results {
			if r.err == nil {
				res.Downloaded++
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
			_ = p.store.MarkInFailedBatch(ctx, []int64{id}, failedErrs[i])
		}
		res.Failed += len(failedIDs)

		if d := p.cfg.Download.InterBatchDelay; d > 0 {
			if !sleepCtx(ctx, d) {
				return res, ctx.Err()
			}
		}
	}
}

// TagResult summarises a completed tag run.
type TagResult struct {
	Tagged int
	Failed int
}

// RunTag processes all items in state=downloaded: builds metadata from the
// cached JSON in the DB, writes tags to the audio file, writes .lrc and
// cover/artist images, then marks the item as tagged.
// It is parallelised up to cfg.Tag.Concurrency.
func (p *Pipeline) RunTag(ctx context.Context) (TagResult, error) {
	var res TagResult

	for {
		if ctx.Err() != nil {
			return res, ctx.Err()
		}

		items, err := p.store.List(ctx, []string{store.StateDownloaded}, p.cfg.Tag.Concurrency)
		if err != nil {
			return res, err
		}
		if len(items) == 0 {
			return res, nil
		}

		// Claim items for tagging (one at a time up to concurrency).
		var batch []*store.Item
		for range items {
			it2, ok, err := p.store.ClaimTag(ctx)
			if err != nil || !ok {
				continue
			}
			batch = append(batch, it2)
		}
		if len(batch) == 0 {
			return res, nil
		}

		type tagResult struct {
			item *store.Item
			err  error
		}
		tagResults := make([]tagResult, len(batch))
		var wg sync.WaitGroup
		for i, it := range batch {
			wg.Add(1)
			i, it := i, it
			go func() {
				defer wg.Done()
				tagResults[i] = tagResult{item: it, err: p.tagOne(ctx, it)}
			}()
		}
		wg.Wait()

		for _, r := range tagResults {
			if r.err != nil {
				p.log.Error("tagging failed", "sng_id", r.item.SngID, "err", r.err)
				_ = p.store.MarkFailedTag(ctx, r.item.SngID, r.err.Error())
				res.Failed++
				continue
			}
			_ = p.store.MarkTagged(ctx, r.item.SngID)
			res.Tagged++

			// If no converter, go straight to converted (terminal).
			if p.conv == nil {
				_ = p.store.MarkConverted(ctx, r.item.SngID)
			}
		}
	}
}

// tagOne writes tags, cover, artist image, and .lrc for one item using
// metadata loaded entirely from the DB cache.
func (p *Pipeline) tagOne(ctx context.Context, item *store.Item) error {
	// Parse ALB_ID from track_data and load album cache.
	var albumData string
	albID := extractAlbIDFromTrackData(item.TrackData)
	if albID > 0 {
		albumData, _ = p.store.GetAlbumCache(ctx, albID)
	}

	md, err := buildMetadataFromCache(item, albumData)
	if err != nil {
		return err
	}

	// Fetch cover image from CDN if not already on disk.
	albumDir := filepath.Dir(filepath.Join(p.musicDir, item.FilePath))
	if p.fields["cover"] {
		albPicture := extractAlbPictureFromTrackData(item.TrackData)
		if albPicture != "" {
			if len(md.Cover) == 0 {
				data, mime, err := p.dz.FetchCover(ctx, albPicture, 500)
				if err == nil && len(data) > 0 {
					md.Cover = data
					md.CoverMIME = mime
				}
			}
		}

		// Fetch artist image for folder.jpg in artist dir.
		artPicture := extractArtPictureFromTrackData(item.TrackData)
		artistDir := filepath.Dir(albumDir)
		folderPath := filepath.Join(artistDir, "folder.jpg")
		if artPicture != "" {
			if _, err := os.Stat(folderPath); os.IsNotExist(err) {
				data, _, err := p.dz.FetchArtistImage(ctx, artPicture, 500)
				if err == nil && len(data) > 0 {
					_ = os.WriteFile(folderPath, data, 0o644)
				}
			}
		}
	}

	// Write tags to the audio file.
	fullPath := filepath.Join(p.musicDir, item.FilePath)
	if err := tagger.Write(fullPath, item.Format, md, p.fields); err != nil {
		return fmt.Errorf("write tags: %w", err)
	}

	// Write cover.jpg.
	if err := tagger.WriteCoverFile(albumDir, md.Cover); err != nil {
		p.log.Warn("failed to write cover.jpg", "path", albumDir, "err", err)
	}

	// Write .lrc file.
	if md.SyncedLyrics != "" {
		lrcPath := fullPath[:len(fullPath)-len(filepath.Ext(fullPath))] + ".lrc"
		_ = os.WriteFile(lrcPath, []byte(md.SyncedLyrics), 0o644)
	}

	return nil
}

// ConvertResult summarises a completed convert run.
type ConvertResult struct {
	Converted int
	Failed    int
}

// RunConvert converts files in state=downloaded (pending conversion) plus any
// finished items that are missing their converted copy. Marks downloaded items
// as finished after successful conversion. Guards against concurrent runs.
func (p *Pipeline) RunConvert(ctx context.Context) (ConvertResult, error) {
	var res ConvertResult
	if p.conv == nil {
		return res, nil
	}
	if !p.convertRunning.CompareAndSwap(false, true) {
		p.log.Warn("convert already running; skipping concurrent trigger")
		return res, nil
	}
	defer p.convertRunning.Store(false)

	// Collect items that need conversion: state=tagged (not yet converted)
	// and state=converted where the converted file is missing.
	tagged, err := p.store.List(ctx, []string{store.StateTagged}, 0)
	if err != nil {
		return res, err
	}
	finished, err := p.store.FinishedItems(ctx)
	if err != nil {
		return res, err
	}

	var jobs []converter.ConvertJob
	for _, it := range append(tagged, finished...) {
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
			// Already converted — if still tagged, mark converted.
			if it.State == store.StateTagged {
				_ = p.store.MarkConverted(ctx, it.SngID)
			}
			continue
		}
		jobs = append(jobs, converter.ConvertJob{SngID: it.SngID, SourcePath: srcPath, CopyTagsFromSource: true})
	}

	if len(jobs) == 0 {
		p.log.Info("convert: nothing to do")
		return res, nil
	}
	p.log.Info("starting convert run", "count", len(jobs))
	converted, failed := p.conv.RunAll(ctx, jobs)
	for _, id := range converted {
		_ = p.store.MarkConverted(ctx, id)
	}
	for id, errMsg := range failed {
		p.log.Warn("conversion failed", "sng_id", id, "err", errMsg)
		_ = p.store.MarkFailedConvert(ctx, id, errMsg)
	}
	res.Converted = len(converted)
	res.Failed = len(failed)
	return res, nil
}

// ForceReconvert prepares items for reconversion.
// mode="all": deletes existing converted files and resets items to state=tagged.
// mode="failed": retries only state=failed_convert items.
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
			_ = os.Remove(outPath)
			// Reset to tagged so RunConvert picks it up.
			if err := p.store.SetState(ctx, it.SngID, store.StateTagged); err != nil {
				return n, err
			}
			n++
		}
		return n, nil

	case "failed":
		n, err := p.store.RequeueAllFailedConverts(ctx)
		return n, err

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

// extractAlbIDFromTrackData parses ALB_ID from cached track JSON.
func extractAlbIDFromTrackData(trackData string) int64 {
	if trackData == "" {
		return 0
	}
	var v struct {
		AlbID string `json:"ALB_ID"`
	}
	if err := json.Unmarshal([]byte(trackData), &v); err != nil {
		return 0
	}
	var id int64
	_, _ = fmt.Sscanf(v.AlbID, "%d", &id)
	return id
}

// extractAlbPictureFromTrackData parses ALB_PICTURE hash from cached track JSON.
func extractAlbPictureFromTrackData(trackData string) string {
	if trackData == "" {
		return ""
	}
	var v struct {
		AlbPicture string `json:"ALB_PICTURE"`
	}
	_ = json.Unmarshal([]byte(trackData), &v)
	return v.AlbPicture
}

// extractArtPictureFromTrackData extracts the main artist ART_PICTURE from ARTISTS array.
func extractArtPictureFromTrackData(trackData string) string {
	if trackData == "" {
		return ""
	}
	var v struct {
		Artists []struct {
			ArtPicture string `json:"ART_PICTURE"`
			RoleID     string `json:"ROLE_ID"`
		} `json:"ARTISTS"`
	}
	if err := json.Unmarshal([]byte(trackData), &v); err != nil {
		return ""
	}
	for _, a := range v.Artists {
		if a.RoleID == "0" {
			return a.ArtPicture
		}
	}
	return ""
}
