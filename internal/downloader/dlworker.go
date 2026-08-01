package downloader

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"deebeets/internal/deezer"
	"deebeets/internal/store"
	"deebeets/internal/tagger"
)

// downloadOne downloads a single track, applying the immediate-retry policy and
// the rate-limit gate. Failures that survive retries are recorded as failed.
func (p *Pipeline) downloadOne(ctx context.Context, item *store.Item) {
	err := p.attemptDownload(ctx, item)
	if err == nil {
		p.logCompletion(ctx, item)
		return
	}
	if deezer.IsRateLimited(err) {
		p.onRateLimited(ctx, item)
		return
	}

	// Immediate retries with backoff. Attempts already counts the initial claim.
	attempt := item.Attempts
	for shouldRetryImmediate(p.cfg.Retry.Mode, attempt, p.cfg.Retry.MaxAttempts) {
		if !sleepCtx(ctx, p.cfg.Retry.Backoff) {
			_ = p.store.SetState(ctx, item.SngID, store.StateQueued) // stopped mid-retry
			return
		}
		attempt++
		p.log.Info("retrying download", "sng_id", item.SngID, "attempt", attempt, "cause", err)
		if err = p.attemptDownload(ctx, item); err == nil {
			p.logCompletion(ctx, item)
			return
		}
		if deezer.IsRateLimited(err) {
			p.onRateLimited(ctx, item)
			return
		}
	}

	p.log.Error("download failed", "sng_id", item.SngID, "err", err)
	_ = p.store.MarkFailed(ctx, item.SngID, store.StageDownload, err.Error())
}

// onRateLimited records a throttle event and returns the item to the queue so it
// resumes once the cooldown elapses.
func (p *Pipeline) onRateLimited(ctx context.Context, item *store.Item) {
	wait, hard := p.gate.hit()
	p.log.Warn("rate limited", "sng_id", item.SngID, "backoff", wait, "hard_stop", hard)
	_ = p.store.SetState(ctx, item.SngID, store.StateQueued)
}

// attemptDownload performs one download attempt end to end.
func (p *Pipeline) attemptDownload(ctx context.Context, item *store.Item) error {
	p.log.Info("downloading", "sng_id", item.SngID, "title", item.Title, "artist", item.Artist)

	// Fetch fresh track data: TRACK_TOKENs expire, so re-resolve at download time.
	track, err := p.dz.GetTrack(ctx, item.SngID)
	if err != nil {
		return fmt.Errorf("get track: %w", err)
	}
	resolved, err := p.dz.ResolveDownload(ctx, track, p.cfg.Deezer.FormatPriority)
	if err != nil {
		return fmt.Errorf("resolve: %w", err)
	}

	ext := tagger.ExtForFormat(resolved.Format)
	rel, err := tagger.RenderPath(p.cfg.Tags.NamingTemplate, buildNameData(track, ext))
	if err != nil {
		return fmt.Errorf("render path: %w", err)
	}
	finalPath := filepath.Join(p.musicDir, rel)

	tmp, err := p.streamToTemp(ctx, item, resolved.URL)
	if err != nil {
		return fmt.Errorf("stream: %w", err)
	}

	// Tag the temp file before moving it into place.
	md := p.buildMetadata(ctx, track, resolved.Format)
	if err := tagger.Write(tmp, resolved.Format, md, p.fields); err != nil {
		p.log.Warn("tagging failed", "sng_id", item.SngID, "err", err)
	}

	if err := os.MkdirAll(filepath.Dir(finalPath), 0o755); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	if err := os.Rename(tmp, finalPath); err != nil {
		return fmt.Errorf("move into place: %w", err)
	}

	p.gate.clear()
	if err := p.store.MarkDownloaded(ctx, item.SngID, resolved.Format, rel); err != nil {
		return err
	}
	p.log.Info("downloaded", "sng_id", item.SngID, "title", item.Title, "artist", item.Artist,
		"format", resolved.Format, "path", rel)
	if !p.importActive {
		return p.store.MarkFinished(ctx, item.SngID)
	}
	return nil
}

func buildNameData(t *deezer.GWTrack, ext string) tagger.NameData {
	disc := t.DiscNumberInt()
	return tagger.NameData{
		AlbumArtist: t.ArtName,
		Album:       t.AlbTitle,
		Artist:      t.ArtName,
		Title:       t.SngTitle,
		Year:        t.ReleaseYear(),
		Track:       t.TrackNumberInt(),
		Disc:        disc,
		MultiDisc:   disc > 1,
		Ext:         ext,
	}
}

// logCompletion checks whether the album or higher-level source (artist/playlist)
// that item belongs to is fully processed and logs a summary if so.
// SQLite's single-writer serialisation means only one goroutine will ever see
// terminal==total for a given group, so there are no duplicate log lines.
func (p *Pipeline) logCompletion(ctx context.Context, item *store.Item) {
	if item.GroupKey == "" {
		return
	}
	terminal, total, err := p.store.GroupProgress(ctx, item.GroupKey)
	if err != nil || terminal < total {
		return
	}
	p.log.Info("album complete", "album", item.Album, "artist", item.AlbumArtist, "tracks", total)

	// For artist/playlist sources, check if the whole source is done too.
	if item.SourceType != "artist" && item.SourceType != "playlist" {
		return
	}
	sterminal, stotal, err := p.store.SourceProgress(ctx, item.SourceType, item.SourceID)
	if err != nil || sterminal < stotal {
		return
	}
	p.log.Info(item.SourceType+" complete", "source_id", item.SourceID, "tracks", stotal)
}

func (p *Pipeline) buildMetadata(ctx context.Context, t *deezer.GWTrack, format string) tagger.Metadata {
	md := tagger.Metadata{
		Title:       t.SngTitle,
		Artist:      t.ArtName,
		AlbumArtist: t.ArtName,
		Album:       t.AlbTitle,
		TrackNumber: t.TrackNumberInt(),
		DiscNumber:  t.DiscNumberInt(),
		Year:        t.ReleaseYear(),
		Date:        t.ReleaseDate(),
		ISRC:        t.ISRC,
		Copyright:   t.Copyright,
	}
	if p.fields["cover"] && t.AlbPicture != "" {
		if data, mime, err := p.dz.FetchCover(ctx, t.AlbPicture, 500); err == nil && len(data) > 0 {
			md.Cover = data
			md.CoverMIME = mime
		}
	}
	return md
}
