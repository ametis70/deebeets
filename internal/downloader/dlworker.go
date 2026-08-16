package downloader

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"deeznt/internal/deezer"
	"deeznt/internal/store"
	"deeznt/internal/tagger"
)

// downloadOne streams one track to disk. Returns an error on failure.
// Metadata tagging is handled by the separate tag stage.
func (p *Pipeline) downloadOne(ctx context.Context, item *store.Item) error {
	err := p.attemptDownload(ctx, item)
	if err == nil {
		p.logCompletion(ctx, item)
	}
	return err
}

// attemptDownload: re-fetch song.getData for a fresh token, resolve CDN URL,
// stream the audio file to disk. Tags are NOT written — that is the tag stage.
func (p *Pipeline) attemptDownload(ctx context.Context, item *store.Item) error {
	p.log.Info("downloading", "sng_id", item.SngID, "title", item.Title, "artist", item.Artist)

	// Always call song.getData to get a fresh TRACK_TOKEN (tokens expire ~1h).
	// Update the cached track_data in the DB as a side effect.
	track, trackData, err := p.dz.GetTrackWithRaw(ctx, item.SngID)
	if err != nil {
		return fmt.Errorf("get track: %w", err)
	}
	if trackData != "" {
		_, _ = p.store.UpdateTrackData(ctx, item.SngID, trackData)
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

	albumDir := filepath.Dir(finalPath)
	if err := os.MkdirAll(albumDir, 0o755); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("mkdir: %w", err)
	}
	if err := os.Rename(tmp, finalPath); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("move into place: %w", err)
	}

	p.gate.clear()
	if err := p.store.MarkDownloaded(ctx, item.SngID, resolved.Format, rel); err != nil {
		return err
	}
	p.log.Info("downloaded", "sng_id", item.SngID, "title", item.Title,
		"format", resolved.Format, "path", rel)
	return nil
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
	defer resp.Body.Close() //nolint:errcheck

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

// buildMetadataFromCache constructs tagger.Metadata from cached JSON in the DB item.
func buildMetadataFromCache(item *store.Item, albumData string) (tagger.Metadata, error) {
	if item.TrackData == "" {
		return tagger.Metadata{}, fmt.Errorf("no cached track_data for sng_id %d", item.SngID)
	}

	var track deezer.GWTrack
	if err := json.Unmarshal([]byte(item.TrackData), &track); err != nil {
		return tagger.Metadata{}, fmt.Errorf("parse track_data: %w", err)
	}

	md := tagger.Metadata{
		Title:        track.SngTitle,
		Artist:       track.ArtistString(),
		Artists:      track.AllArtists(),
		AlbumArtist:  track.AlbumArtistString(),
		AlbumArtists: track.MainArtists(),
		Album:        track.AlbTitle,
		TrackNumber:  track.TrackNumberInt(),
		DiscNumber:   track.DiscNumberInt(),
		Year:         track.ReleaseYear(),
		Date:         track.ReleaseDate(),
		Genre:        track.GenreName(),
		Composer:     joinSlice(track.Contributors["author"], " / "),
		ISRC:         track.ISRC,
		Copyright:    track.Copyright,
		ReplayGain:   track.ReplayGainString(),
	}

	if albumData != "" {
		var album deezer.GWAlbum
		if err := json.Unmarshal([]byte(albumData), &album); err == nil {
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
	}

	if item.LyricsData != "" {
		var lyrics deezer.GWLyrics
		if err := json.Unmarshal([]byte(item.LyricsData), &lyrics); err == nil {
			lrc := lyrics.ToLRC()
			if lrc != "" {
				md.Lyrics = lrc
			} else {
				md.Lyrics = lyrics.LyricsText
			}
			md.SyncedLyrics = lrc
		}
	}

	return md, nil
}

func joinSlice(ss []string, sep string) string {
	result := ""
	for i, s := range ss {
		if i > 0 {
			result += sep
		}
		result += s
	}
	return result
}
