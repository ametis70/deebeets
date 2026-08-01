package tagger

import (
	"bytes"
	"path/filepath"
	"strings"
	"text/template"
)

// NameData is the data model exposed to the naming template.
type NameData struct {
	AlbumArtist string
	Album       string
	Artist      string
	Title       string
	Year        int
	Track       int
	TotalTracks int
	Disc        int
	TotalDiscs  int
	MultiDisc   bool
	Ext         string
}

// RenderPath executes the naming template and returns a filesystem-safe path
// relative to the music directory. The template uses "/" as the directory
// separator. String fields are sanitized *before* substitution so that a "/"
// inside a value (e.g. "AC/DC") becomes "_" instead of a spurious directory.
func RenderPath(tmplText string, d NameData) (string, error) {
	d.AlbumArtist = sanitizeSegment(d.AlbumArtist)
	d.Album = sanitizeSegment(d.Album)
	d.Artist = sanitizeSegment(d.Artist)
	d.Title = sanitizeSegment(d.Title)

	t, err := template.New("path").Option("missingkey=zero").Parse(tmplText)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, d); err != nil {
		return "", err
	}
	segments := strings.Split(buf.String(), "/")
	clean := make([]string, 0, len(segments))
	for _, seg := range segments {
		seg = strings.TrimRight(strings.TrimSpace(seg), ". ")
		if seg == "" {
			continue
		}
		clean = append(clean, seg)
	}
	return filepath.Join(clean...), nil
}

// sanitizeSegment strips characters that are illegal or awkward in path
// components across common filesystems.
func sanitizeSegment(s string) string {
	s = strings.TrimSpace(s)
	replacer := strings.NewReplacer(
		"/", "_", "\\", "_", ":", "_", "*", "_", "?", "_",
		"\"", "_", "<", "_", ">", "_", "|", "_", "\x00", "",
	)
	s = replacer.Replace(s)
	// Trailing dots/spaces are problematic on Windows and confusing elsewhere.
	s = strings.TrimRight(s, ". ")
	return s
}
