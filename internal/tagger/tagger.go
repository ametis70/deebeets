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
	Title       string
	Artist      string
	AlbumArtist string
	Album       string
	TrackNumber int
	TotalTracks int
	DiscNumber  int
	TotalDiscs  int
	Year        int
	Date        string
	Genre       string
	Composer    string
	ISRC        string
	Barcode     string
	Copyright   string
	BPM         int
	ReplayGain  string
	Comment     string
	Lyrics      string
	Cover       []byte // front cover image bytes (JPEG)
	CoverMIME   string // e.g. "image/jpeg"
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
// format ("FLAC" -> FLAC/Vorbis, everything else -> MP3/ID3v2).
func Write(path, format string, md Metadata, fields FieldSet) error {
	switch ExtForFormat(format) {
	case "flac":
		return writeFLAC(path, md, fields)
	case "mp3":
		return writeMP3(path, md, fields)
	default:
		// Unsupported container for baseline tagging; leave the file untouched.
		return nil
	}
}

func numPair(a, b int) string {
	if b > 0 {
		return fmt.Sprintf("%d/%d", a, b)
	}
	return fmt.Sprintf("%d", a)
}
