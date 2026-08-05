package downloader

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"deebeets/internal/deezer"
	"deebeets/internal/store"
	"deebeets/internal/tagger"
)

// downloadOne attempts a single download of the given item. On success the item
// is moved to state=downloaded. On failure the error is returned to the caller
// (RunDownloads handles rate-limit vs regular failure classification).
func (p *Pipeline) downloadOne(ctx context.Context, item *store.Item) error {
	err := p.attemptDownload(ctx, item)
	if err == nil {
		p.logCompletion(ctx, item)
		return nil
	}
	return err
}

// attemptDownload performs one download attempt end to end.
func (p *Pipeline) attemptDownload(ctx context.Context, item *store.Item) error {
	p.log.Info("downloading", "sng_id", item.SngID, "title", item.Title, "artist", item.Artist)

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

	tmp, err := p.streamToTemp(ctx, item.SngID, resolved.URL)
	if err != nil {
		return fmt.Errorf("stream: %w", err)
	}

	// Tag the temp file before moving it into place.
	md := p.buildMetadata(ctx, track, resolved.Format)
	if err := tagger.Write(tmp, resolved.Format, md, p.fields); err != nil {
		p.log.Warn("tagging failed", "sng_id", item.SngID, "err", err)
	}

	albumDir := filepath.Dir(finalPath)
	if err := os.MkdirAll(albumDir, 0o755); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("mkdir: %w", err)
	}
	if err := os.Rename(tmp, finalPath); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("move into place: %w", err)
	}

	if len(md.Cover) > 0 {
		coverPath := filepath.Join(albumDir, "cover.jpg")
		if _, err := os.Stat(coverPath); os.IsNotExist(err) {
			if err := os.WriteFile(coverPath, md.Cover, 0o644); err != nil {
				p.log.Warn("failed to write cover.jpg", "path", coverPath, "err", err)
			}
		}
	}

	p.gate.clear()
	if err := p.store.MarkDownloaded(ctx, item.SngID, resolved.Format, rel); err != nil {
		return err
	}
	p.log.Info("downloaded", "sng_id", item.SngID, "title", item.Title,
		"format", resolved.Format, "path", rel)
	return nil
}

// streamToTemp downloads and decrypts a track into a temp file. If the context
// is cancelled mid-stream, the partial file is deleted before returning.
func (p *Pipeline) streamToTemp(ctx context.Context, sngID int64, url string) (string, error) {
	if err := os.MkdirAll(p.incompleteDir, 0o755); err != nil {
		return "", err
	}
	tmp := filepath.Join(p.incompleteDir, fmt.Sprintf("%d.part", sngID))

	resp, err := p.dz.Download(ctx, url, 0)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected status %d", resp.StatusCode)
	}

	f, err := os.Create(tmp)
	if err != nil {
		return "", err
	}

	_, derr := deezer.DecryptTrack(f, resp.Body, sngID)
	if cerr := f.Close(); cerr != nil && derr == nil {
		derr = cerr
	}
	if derr != nil {
		_ = os.Remove(tmp)
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", derr
	}
	return tmp, nil
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

func (p *Pipeline) logCompletion(ctx context.Context, item *store.Item) {
	if item.GroupKey == "" {
		return
	}
	terminal, total, err := p.store.GroupProgress(ctx, item.GroupKey)
	if err != nil || terminal < total {
		return
	}
	p.log.Info("album complete", "album", item.Album, "artist", item.AlbumArtist, "tracks", total)

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

// discardBody drains and discards an http response body.
func discardBody(r *http.Response) {
	if r != nil {
		_, _ = io.Copy(io.Discard, r.Body)
	}
}
