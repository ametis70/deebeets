package downloader

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"deeznt/internal/converter"
	"deeznt/internal/deezer"
	"deeznt/internal/store"
	"deeznt/internal/tagger"
)

// downloadOne attempts a single download. Returns a ConvertJob (non-nil on
// success with converter enabled) and any error.
func (p *Pipeline) downloadOne(ctx context.Context, item *store.Item) (*converter.ConvertJob, error) {
	job, err := p.attemptDownload(ctx, item)
	if err == nil {
		p.logCompletion(ctx, item)
	}
	return job, err
}

// attemptDownload performs one download attempt end to end. Returns a
// ConvertJob with the source path and metadata if conversion is enabled.
func (p *Pipeline) attemptDownload(ctx context.Context, item *store.Item) (*converter.ConvertJob, error) {
	p.log.Info("downloading", "sng_id", item.SngID, "title", item.Title, "artist", item.Artist)

	track, err := p.dz.GetTrack(ctx, item.SngID)
	if err != nil {
		return nil, fmt.Errorf("get track: %w", err)
	}
	resolved, err := p.dz.ResolveDownload(ctx, track, p.cfg.Deezer.FormatPriority)
	if err != nil {
		return nil, fmt.Errorf("resolve: %w", err)
	}

	ext := tagger.ExtForFormat(resolved.Format)
	rel, err := tagger.RenderPath(p.cfg.Tags.NamingTemplate, buildNameData(track, ext))
	if err != nil {
		return nil, fmt.Errorf("render path: %w", err)
	}
	finalPath := filepath.Join(p.musicDir, rel)

	tmp, err := p.streamToTemp(ctx, item.SngID, resolved.URL)
	if err != nil {
		return nil, fmt.Errorf("stream: %w", err)
	}

	// Fetch album metadata (total tracks/discs, label, genre).
	var album *deezer.GWAlbum
	if albID := track.AlbumID(); albID > 0 {
		if a, err := p.dz.GetAlbum(ctx, albID); err == nil {
			album = a
		} else {
			p.log.Warn("failed to fetch album metadata", "alb_id", albID, "err", err)
		}
	}

	// Fetch lyrics (plain + synced).
	var lyrics *deezer.GWLyrics
	if track.LyricsID != 0 && p.fields["lyrics"] {
		if l, err := p.dz.GetLyrics(ctx, item.SngID); err == nil {
			lyrics = l
		} else {
			p.log.Warn("failed to fetch lyrics", "sng_id", item.SngID, "err", err)
		}
	}

	md := p.buildMetadata(ctx, track, album, lyrics)

	if err := tagger.Write(tmp, resolved.Format, md, p.fields); err != nil {
		p.log.Warn("tagging failed", "sng_id", item.SngID, "err", err)
	}

	albumDir := filepath.Dir(finalPath)
	if err := os.MkdirAll(albumDir, 0o755); err != nil {
		_ = os.Remove(tmp)
		return nil, fmt.Errorf("mkdir: %w", err)
	}
	if err := os.Rename(tmp, finalPath); err != nil {
		_ = os.Remove(tmp)
		return nil, fmt.Errorf("move into place: %w", err)
	}

	// Write cover.jpg alongside the tracks if not already present.
	if err := tagger.WriteCoverFile(albumDir, md.Cover); err != nil {
		p.log.Warn("failed to write cover.jpg", "path", albumDir, "err", err)
	}

	// Write folder.jpg in the artist directory for Navidrome artist image.
	if p.fields["cover"] {
		artistDir := filepath.Dir(albumDir)
		folderPath := filepath.Join(artistDir, "folder.jpg")
		if _, err := os.Stat(folderPath); os.IsNotExist(err) {
			if artPic := track.MainArtistPicture(); artPic != "" {
				if data, _, err := p.dz.FetchArtistImage(ctx, artPic, 500); err == nil && len(data) > 0 {
					if err := os.WriteFile(folderPath, data, 0o644); err != nil {
						p.log.Warn("failed to write artist folder.jpg", "path", folderPath, "err", err)
					}
				}
			}
		}
	}

	// Write synced lyrics as a .lrc file alongside the audio file.
	if md.SyncedLyrics != "" {
		lrcPath := strings.TrimSuffix(finalPath, filepath.Ext(finalPath)) + ".lrc"
		if err := os.WriteFile(lrcPath, []byte(md.SyncedLyrics), 0o644); err != nil {
			p.log.Warn("failed to write .lrc file", "path", lrcPath, "err", err)
		}
	}

	p.gate.clear()
	if err := p.store.MarkDownloaded(ctx, item.SngID, resolved.Format, rel); err != nil {
		return nil, err
	}
	p.log.Info("downloaded", "sng_id", item.SngID, "title", item.Title,
		"format", resolved.Format, "path", rel)

	// If conversion is disabled, go straight to finished.
	if p.conv == nil {
		if err := p.store.MarkFinished(ctx, item.SngID); err != nil {
			return nil, err
		}
		return nil, nil
	}

	// Return a convert job so the pipeline can convert and then mark finished.
	return &converter.ConvertJob{SngID: item.SngID, SourcePath: finalPath, Metadata: md}, nil
}

// streamToTemp downloads and decrypts a track into a temp file.
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
		AlbumArtist: t.AlbumArtistString(),
		Album:       t.AlbTitle,
		Artist:      t.ArtistString(),
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

func (p *Pipeline) buildMetadata(ctx context.Context, t *deezer.GWTrack, album *deezer.GWAlbum, lyrics *deezer.GWLyrics) tagger.Metadata {
	md := tagger.Metadata{
		Title:        t.SngTitle,
		Artist:       t.ArtistString(),
		Artists:      t.AllArtists(),
		AlbumArtist:  t.AlbumArtistString(),
		AlbumArtists: t.MainArtists(),
		Album:        t.AlbTitle,
		TrackNumber:  t.TrackNumberInt(),
		DiscNumber:   t.DiscNumberInt(),
		Year:         t.ReleaseYear(),
		Date:         t.ReleaseDate(),
		Genre:        t.GenreName(),
		Composer:     strings.Join(t.Contributors["author"], " / "),
		ISRC:         t.ISRC,
		Copyright:    t.Copyright,
		ReplayGain:   t.ReplayGainString(),
	}

	if album != nil {
		md.TotalTracks = album.NumberTrackInt()
		md.TotalDiscs = album.NumberDiskInt()
		md.Label = album.LabelName
		if md.Copyright == "" {
			md.Copyright = album.Copyright
		}
		if md.Genre == "" {
			md.Genre = deezer.GenreName(album.GenreID)
		}
	}

	if lyrics != nil {
		lrc := lyrics.ToLRC()
		if lrc != "" {
			// Prefer LRC in the LYRICS tag — Navidrome parses timestamps from it
			// to produce synced lyrics. Plain text fallback if no sync data.
			md.Lyrics = lrc
		} else {
			md.Lyrics = lyrics.LyricsText
		}
		md.SyncedLyrics = lrc
	}

	if p.fields["cover"] && t.AlbPicture != "" {
		if data, mime, err := p.dz.FetchCover(ctx, t.AlbPicture, 500); err == nil && len(data) > 0 {
			md.Cover = data
			md.CoverMIME = mime
		}
	}
	return md
}
