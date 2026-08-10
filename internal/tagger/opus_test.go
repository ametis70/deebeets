package tagger

import (
	"testing"
)

func TestBuildVorbisComments_MultiValueArtists(t *testing.T) {
	md := Metadata{
		Artist:       "Gorillaz feat. Sinfonia ViVa",
		Artists:      []string{"Gorillaz", "Sinfonia ViVa"},
		AlbumArtist:  "Gorillaz",
		AlbumArtists: []string{"Gorillaz"},
		Title:        "Orchestral Intro",
		Album:        "Plastic Beach",
		TrackNumber:  1,
		TotalTracks:  16,
	}
	f := DefaultFieldSet()
	tags := buildVorbisComments(md, f)

	// Collect all values per key.
	got := make(map[string][]string)
	for _, t := range tags {
		got[t.Key] = append(got[t.Key], t.Val)
	}

	// ARTIST should appear exactly once with the display string.
	if len(got["ARTIST"]) != 1 || got["ARTIST"][0] != "Gorillaz feat. Sinfonia ViVa" {
		t.Errorf("ARTIST = %v, want single display string", got["ARTIST"])
	}

	// ARTISTS should appear as two separate entries, not joined with semicolon.
	if len(got["ARTISTS"]) != 2 {
		t.Errorf("ARTISTS = %v (len %d), want 2 separate entries", got["ARTISTS"], len(got["ARTISTS"]))
	}
	for _, a := range got["ARTISTS"] {
		if a == "Gorillaz;Sinfonia ViVa" {
			t.Error("ARTISTS must not be semicolon-joined; got single merged entry")
		}
	}

	// ALBUMARTIST singular.
	if len(got["ALBUMARTIST"]) != 1 || got["ALBUMARTIST"][0] != "Gorillaz" {
		t.Errorf("ALBUMARTIST = %v", got["ALBUMARTIST"])
	}

	// ALBUMARTISTS single entry.
	if len(got["ALBUMARTISTS"]) != 1 || got["ALBUMARTISTS"][0] != "Gorillaz" {
		t.Errorf("ALBUMARTISTS = %v", got["ALBUMARTISTS"])
	}

	// TRACKNUMBER and TRACKTOTAL present.
	if len(got["TRACKNUMBER"]) != 1 || got["TRACKNUMBER"][0] != "1" {
		t.Errorf("TRACKNUMBER = %v", got["TRACKNUMBER"])
	}
	if len(got["TRACKTOTAL"]) != 1 || got["TRACKTOTAL"][0] != "16" {
		t.Errorf("TRACKTOTAL = %v", got["TRACKTOTAL"])
	}
}

func TestBuildVorbisComments_NoFeaturedArtists(t *testing.T) {
	md := Metadata{
		Artist:       "Radiohead",
		Artists:      []string{"Radiohead"},
		AlbumArtist:  "Radiohead",
		AlbumArtists: []string{"Radiohead"},
		Title:        "Creep",
	}
	f := DefaultFieldSet()
	tags := buildVorbisComments(md, f)

	got := make(map[string][]string)
	for _, t := range tags {
		got[t.Key] = append(got[t.Key], t.Val)
	}

	if len(got["ARTISTS"]) != 1 || got["ARTISTS"][0] != "Radiohead" {
		t.Errorf("ARTISTS = %v, want single entry", got["ARTISTS"])
	}
}

func TestBuildVorbisComments_SyncedLyrics(t *testing.T) {
	md := Metadata{
		Title:        "Song",
		Lyrics:       "plain lyrics",
		SyncedLyrics: "[00:01.00]line one\n[00:02.00]line two\n",
	}
	f := DefaultFieldSet()
	tags := buildVorbisComments(md, f)

	got := make(map[string][]string)
	for _, tag := range tags {
		got[tag.Key] = append(got[tag.Key], tag.Val)
	}

	if len(got["LYRICS"]) != 1 {
		t.Errorf("LYRICS missing, got %v", got["LYRICS"])
	}
	if len(got["SYNCEDLYRICS"]) != 1 || got["SYNCEDLYRICS"][0] == "" {
		t.Errorf("SYNCEDLYRICS missing or empty, got %v", got["SYNCEDLYRICS"])
	}
}

func TestBuildVorbisComments_FieldSetFiltering(t *testing.T) {
	md := Metadata{
		Title:  "T",
		Artist: "A",
		Album:  "Alb",
		Genre:  "Rock",
		Label:  "Label",
	}
	// Only allow title and artist — genre and label should be absent.
	f := NewFieldSet([]string{"title", "artist"})
	tags := buildVorbisComments(md, f)

	got := make(map[string][]string)
	for _, t := range tags {
		got[t.Key] = append(got[t.Key], t.Val)
	}

	if len(got["TITLE"]) == 0 {
		t.Error("TITLE should be present")
	}
	if len(got["ARTIST"]) == 0 {
		t.Error("ARTIST should be present")
	}
	if len(got["GENRE"]) > 0 {
		t.Error("GENRE should be absent when not in field set")
	}
	if len(got["LABEL"]) > 0 {
		t.Error("LABEL should be absent when not in field set")
	}
}
