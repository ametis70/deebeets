package tagger

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/go-flac/flacpicture/v2"
	"github.com/go-flac/flacvorbis/v2"
	"github.com/go-flac/go-flac/v2"
)

// buildVorbisComments constructs the Vorbis comment tag map for FLAC and Opus.
// Multi-value tags result in multiple entries for the same key.
func buildVorbisComments(md Metadata, f FieldSet) []vorbisTag {
	var tags []vorbisTag

	add := func(field, key, val string) {
		if val != "" && f.on(field) {
			tags = append(tags, vorbisTag{key, val})
		}
	}

	add("title", "TITLE", md.Title)
	add("artist", "ARTIST", md.Artist)
	if f.on("artist") {
		for _, a := range md.Artists {
			if a != "" {
				tags = append(tags, vorbisTag{"ARTISTS", a})
			}
		}
	}
	add("albumartist", "ALBUMARTIST", md.AlbumArtist)
	if f.on("albumartist") {
		for _, a := range md.AlbumArtists {
			if a != "" {
				tags = append(tags, vorbisTag{"ALBUMARTISTS", a})
			}
		}
	}
	add("album", "ALBUM", md.Album)
	add("genre", "GENRE", md.Genre)
	add("label", "LABEL", md.Label)
	add("composer", "COMPOSER", md.Composer)
	add("isrc", "ISRC", md.ISRC)
	add("barcode", "BARCODE", md.Barcode)
	add("copyright", "COPYRIGHT", md.Copyright)
	add("replaygain", "REPLAYGAIN_TRACK_GAIN", md.ReplayGain)
	add("comment", "COMMENT", md.Comment)
	add("lyrics", "LYRICS", md.Lyrics)
	if f.on("lyrics") && md.SyncedLyrics != "" {
		tags = append(tags, vorbisTag{"SYNCEDLYRICS", md.SyncedLyrics})
	}
	if md.Date != "" {
		add("date", "DATE", md.Date)
	} else if md.Year > 0 {
		add("date", "DATE", fmt.Sprintf("%d", md.Year))
	}
	if f.on("tracknumber") && md.TrackNumber > 0 {
		tags = append(tags, vorbisTag{"TRACKNUMBER", fmt.Sprintf("%d", md.TrackNumber)})
	}
	if f.on("totaltracks") && md.TotalTracks > 0 {
		tags = append(tags, vorbisTag{"TRACKTOTAL", fmt.Sprintf("%d", md.TotalTracks)})
	}
	if f.on("discnumber") && md.DiscNumber > 0 {
		tags = append(tags, vorbisTag{"DISCNUMBER", fmt.Sprintf("%d", md.DiscNumber)})
	}
	if f.on("disctotal") && md.TotalDiscs > 0 {
		tags = append(tags, vorbisTag{"DISCTOTAL", fmt.Sprintf("%d", md.TotalDiscs)})
	}
	if f.on("bpm") && md.BPM > 0 {
		tags = append(tags, vorbisTag{"BPM", fmt.Sprintf("%d", md.BPM)})
	}
	return tags
}

type vorbisTag struct{ Key, Val string }

// writeOggOpus rewrites tags on an Ogg Opus file using opustags, which
// correctly writes each Vorbis comment as a separate field — including
// multi-value tags like ARTISTS and ALBUMARTISTS. ffmpeg joins repeated
// -metadata values with ";" which breaks multi-value support.
// --raw is required to handle UTF-8 characters (e.g. ©) when the container
// locale is POSIX/ASCII.
func writeOggOpus(path string, md Metadata, f FieldSet) error {
	tags := buildVorbisComments(md, f)
	if len(tags) == 0 {
		return nil
	}

	// opustags --raw -i -D clears all existing tags, then -s KEY=VALUE sets each one.
	// Repeated -s flags produce separate Vorbis comment entries (true multi-value).
	argv := []string{"opustags", "--raw", "-i", "-D"}
	for _, t := range tags {
		argv = append(argv, "-s", t.Key+"="+t.Val)
	}
	argv = append(argv, path)

	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = append(os.Environ(), "LANG=C.UTF-8", "LC_ALL=C.UTF-8")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("opus tag write: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// writeFLAC writes Vorbis comments (and an embedded picture) to a FLAC file.
func writeFLAC(path string, md Metadata, f FieldSet) error {
	file, err := flac.ParseFile(path)
	if err != nil {
		return fmt.Errorf("parse flac: %w", err)
	}

	cmt := flacvorbis.New()
	for _, t := range buildVorbisComments(md, f) {
		_ = cmt.Add(t.Key, t.Val)
	}

	replaceComments(file, cmt)

	if f.on("cover") && len(md.Cover) > 0 {
		pic, err := flacpicture.NewFromImageData(
			flacpicture.PictureTypeFrontCover, "Front cover", md.Cover, coverMIME(md))
		if err == nil {
			block := pic.Marshal()
			file.Meta = append(file.Meta, &block)
		}
	}

	return file.Save(path)
}

// replaceComments removes any existing Vorbis comment block and appends the new one.
func replaceComments(file *flac.File, cmt *flacvorbis.MetaDataBlockVorbisComment) {
	kept := file.Meta[:0]
	for _, m := range file.Meta {
		if m.Type != flac.VorbisComment {
			kept = append(kept, m)
		}
	}
	block := cmt.Marshal()
	file.Meta = append(kept, &block)
}

// ExtForOutputFormat returns the file extension for a converter output format name.
func ExtForOutputFormat(format string) string {
	switch strings.ToLower(format) {
	case "opus":
		return "opus"
	case "mp3":
		return "mp3"
	case "flac":
		return "flac"
	case "ogg":
		return "ogg"
	case "aac", "m4a":
		return "m4a"
	default:
		return format
	}
}

// WriteOggOpus is exported for use by the converter package.
func WriteOggOpus(path string, md Metadata, f FieldSet) error {
	return writeOggOpus(path, md, f)
}

// coverMIME returns the MIME type for the cover image.
func coverMIME(md Metadata) string {
	if md.CoverMIME != "" {
		return md.CoverMIME
	}
	return "image/jpeg"
}

// writeCoverFile writes the cover image to a file alongside the audio.
func WriteCoverFile(dir string, cover []byte) error {
	if len(cover) == 0 {
		return nil
	}
	coverPath := filepath.Join(dir, "cover.jpg")
	if _, err := os.Stat(coverPath); os.IsNotExist(err) {
		return os.WriteFile(coverPath, cover, 0o644)
	}
	return nil
}
