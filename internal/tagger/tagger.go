// Package tagger writes baseline tags to downloaded audio files and renders
// their on-disk paths. beets remains the canonical tagger after import; these
// tags just make files valid and Navidrome-readable beforehand.
package tagger

import (
	"fmt"
	"strings"
)

// Metadata holds everything the tagger may write. Zero-valued fields are simply
// skipped, so callers fill in whatever Deezer provided.
type Metadata struct {
	Title        string
	Artist       string   // display name: "Main feat. Feat1 / Feat2"
	Artists      []string // multi-value: one entry per artist (ARTISTS tag)
	AlbumArtist  string   // display name: "Main1 / Main2"
	AlbumArtists []string // multi-value: one entry per album artist (ALBUMARTISTS tag)
	Album        string
	TrackNumber  int
	TotalTracks  int
	DiscNumber   int
	TotalDiscs   int
	Year         int
	Date         string
	Genre        string
	Label        string
	Composer     string
	ISRC         string
	Barcode      string
	Copyright    string
	BPM          int
	ReplayGain   string
	Comment      string
	Lyrics       string // plain unsynchronised lyrics
	SyncedLyrics string // LRC format synchronised lyrics (written as .lrc file, not embedded)
	Cover        []byte // front cover image bytes (JPEG)
	CoverMIME    string // e.g. "image/jpeg"
}

// FieldSet is the set of enabled tag names from config.
type FieldSet map[string]bool

// NewFieldSet builds a FieldSet from a config list.
func NewFieldSet(fields []string) FieldSet {
	fs := make(FieldSet, len(fields))
	for _, f := range fields {
		fs[strings.ToLower(strings.TrimSpace(f))] = true
	}
	return fs
}

func (fs FieldSet) on(name string) bool { return fs[name] }

// DefaultFieldSet returns the full set of fields used when converting files.
func DefaultFieldSet() FieldSet {
	return NewFieldSet([]string{
		"title", "artist", "albumartist", "album",
		"tracknumber", "totaltracks", "discnumber", "disctotal",
		"date", "genre", "label", "composer", "isrc", "barcode",
		"copyright", "bpm", "replaygain", "comment", "lyrics",
	})
}

// ExtForFormat returns the file extension (no dot) for a Deezer format name.
func ExtForFormat(format string) string {
	switch format {
	case "FLAC":
		return "flac"
	case "AAC_64":
		return "m4a"
	default: // MP3_128 / MP3_256 / MP3_320 / MP3_64
		return "mp3"
	}
}

// Write applies the enabled tags to the file at path, choosing the container by
// format ("FLAC" -> FLAC/Vorbis, "OGG_OPUS" -> Ogg Opus via ffmpeg, everything else -> MP3/ID3v2).
func Write(path, format string, md Metadata, fields FieldSet) error {
	switch format {
	case "FLAC":
		return writeFLAC(path, md, fields)
	case "OGG_OPUS":
		return writeOggOpus(path, md, fields)
	case "MP3_128", "MP3_256", "MP3_320", "MP3_64":
		return writeMP3(path, md, fields)
	default:
		return nil
	}
}

func numPair(a, b int) string {
	if b > 0 {
		return fmt.Sprintf("%d/%d", a, b)
	}
	return fmt.Sprintf("%d", a)
}
