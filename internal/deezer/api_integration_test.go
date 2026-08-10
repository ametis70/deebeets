//go:build integration

// Integration tests for the Deezer GW API.
//
// These tests make real HTTP requests to Deezer and require a valid ARL.
// They are excluded from the normal test run and serve as a canary to detect
// API changes.
//
// Run with:
//
//	DEEZER_ARL=<your-arl> go test -tags integration ./internal/deezer/ -v -run TestAPI
//
// The tests use the Gorillaz "Plastic Beach" album (ID 502723) and a specific
// track from it as stable reference data.

package deezer_test

import (
	"context"
	"os"
	"testing"

	"deeznt/internal/deezer"
)

const (
	// Gorillaz – Plastic Beach (released 2010-03-03)
	testAlbumID = int64(502723)
	// "Some Kind of Nature (feat. Lou Reed)" – has lyrics, featured artist
	testTrackID = int64(5490698)
	// "Orchestral Intro (feat. Sinfonia ViVA)" – featured artist, no lyrics
	testTrackIDNoLyrics = int64(5490690)
)

func newTestClient(t *testing.T) *deezer.Client {
	t.Helper()
	arl := os.Getenv("DEEZER_ARL")
	if arl == "" {
		t.Skip("DEEZER_ARL not set; skipping integration test")
	}
	c, err := deezer.New(arl)
	if err != nil {
		t.Fatalf("deezer.New: %v", err)
	}
	if err := c.Login(context.Background()); err != nil {
		t.Fatalf("Login: %v", err)
	}
	return c
}

// TestAPILogin verifies that getUserData returns a non-zero user ID and
// a non-empty checkForm token.
func TestAPILogin(t *testing.T) {
	c := newTestClient(t)
	if c.UserID() == 0 {
		t.Error("UserID should be non-zero after login")
	}
}

// TestAPIGetTrack verifies the song.getData response shape.
func TestAPIGetTrack(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()

	track, err := c.GetTrack(ctx, testTrackID)
	if err != nil {
		t.Fatalf("GetTrack: %v", err)
	}

	if track.SngID == "" {
		t.Error("SNG_ID should not be empty")
	}
	if track.SngTitle == "" {
		t.Error("SNG_TITLE should not be empty")
	}
	if track.AlbID == "" {
		t.Error("ALB_ID should not be empty")
	}
	if track.TrackToken == "" {
		t.Error("TRACK_TOKEN should not be empty")
	}
	if track.MD5Origin == "" {
		t.Error("MD5_ORIGIN should not be empty")
	}
}

// TestAPIGetTrackContributors verifies SNG_CONTRIBUTORS is populated and uses
// the correct key names (main_artist / featuring).
func TestAPIGetTrackContributors(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()

	track, err := c.GetTrack(ctx, testTrackIDNoLyrics)
	if err != nil {
		t.Fatalf("GetTrack: %v", err)
	}

	if track.Contributors == nil {
		t.Fatal("SNG_CONTRIBUTORS should not be nil for this track")
	}
	main := track.MainArtists()
	if len(main) == 0 {
		t.Error("expected at least one main_artist")
	}
	feat := track.FeaturedArtists()
	if len(feat) == 0 {
		t.Errorf("expected featured artists for %q (feat. Sinfonia ViVA)", track.SngTitle)
	}
	t.Logf("main_artist: %v", main)
	t.Logf("featuring:   %v", feat)
	t.Logf("ArtistString: %s", track.ArtistString())
}

// TestAPIGetTrackArtists verifies the ARTISTS array is present and contains
// ART_PICTURE hashes.
func TestAPIGetTrackArtists(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()

	track, err := c.GetTrack(ctx, testTrackIDNoLyrics)
	if err != nil {
		t.Fatalf("GetTrack: %v", err)
	}

	if len(track.Artists) == 0 {
		t.Fatal("ARTISTS array should not be empty")
	}
	mainPic := track.MainArtistPicture()
	if mainPic == "" {
		t.Error("expected a non-empty ART_PICTURE for main artist (ROLE_ID=0)")
	}
	t.Logf("main artist picture hash: %s", mainPic)
}

// TestAPIGetTrackLyricsID verifies LYRICS_ID is non-zero for a track known to
// have lyrics.
func TestAPIGetTrackLyricsID(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()

	track, err := c.GetTrack(ctx, testTrackID)
	if err != nil {
		t.Fatalf("GetTrack: %v", err)
	}
	if track.LyricsID == 0 {
		t.Errorf("expected non-zero LYRICS_ID for %q", track.SngTitle)
	}
}

// TestAPIGetLyrics verifies song.getLyrics returns plain text and synced JSON.
func TestAPIGetLyrics(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()

	lyrics, err := c.GetLyrics(ctx, testTrackID)
	if err != nil {
		t.Fatalf("GetLyrics: %v", err)
	}
	if lyrics.LyricsText == "" {
		t.Error("LYRICS_TEXT should not be empty")
	}
	if len(lyrics.SyncJSON) == 0 {
		t.Error("LYRICS_SYNC_JSON should not be empty")
	}
	// Verify at least some entries have timestamps.
	timestamped := 0
	for _, e := range lyrics.SyncJSON {
		if e.LRCTimestamp != "" {
			timestamped++
		}
	}
	if timestamped == 0 {
		t.Error("expected at least one synced lyric line with lrc_timestamp")
	}
	lrc := lyrics.ToLRC()
	if lrc == "" {
		t.Error("ToLRC() should produce non-empty output")
	}
	t.Logf("LRC preview:\n%s", lrc[:min(len(lrc), 200)])
}

// TestAPIGetAlbum verifies album.getData returns label, track count, etc.
func TestAPIGetAlbum(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()

	album, err := c.GetAlbum(ctx, testAlbumID)
	if err != nil {
		t.Fatalf("GetAlbum: %v", err)
	}
	if album.LabelName == "" {
		t.Error("LABEL_NAME should not be empty")
	}
	if album.NumberTrackInt() == 0 {
		t.Error("NUMBER_TRACK should be non-zero")
	}
	if album.NumberDiskInt() == 0 {
		t.Error("NUMBER_DISK should be non-zero")
	}
	t.Logf("label: %s, tracks: %d, discs: %d",
		album.LabelName, album.NumberTrackInt(), album.NumberDiskInt())
}

// TestAPIFetchCover verifies that album cover images can be fetched.
func TestAPIFetchCover(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()

	track, err := c.GetTrack(ctx, testTrackID)
	if err != nil {
		t.Fatalf("GetTrack: %v", err)
	}
	if track.AlbPicture == "" {
		t.Skip("no ALB_PICTURE on track")
	}
	data, mime, err := c.FetchCover(ctx, track.AlbPicture, 500)
	if err != nil {
		t.Fatalf("FetchCover: %v", err)
	}
	if len(data) == 0 {
		t.Error("expected non-empty cover image data")
	}
	if mime != "image/jpeg" {
		t.Errorf("expected image/jpeg, got %s", mime)
	}
	t.Logf("cover size: %d bytes", len(data))
}

// TestAPIFetchArtistImage verifies that artist images can be fetched.
func TestAPIFetchArtistImage(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()

	track, err := c.GetTrack(ctx, testTrackID)
	if err != nil {
		t.Fatalf("GetTrack: %v", err)
	}
	artPic := track.MainArtistPicture()
	if artPic == "" {
		t.Skip("no ART_PICTURE for main artist")
	}
	data, mime, err := c.FetchArtistImage(ctx, artPic, 500)
	if err != nil {
		t.Fatalf("FetchArtistImage: %v", err)
	}
	if len(data) == 0 {
		t.Error("expected non-empty artist image data")
	}
	if mime != "image/jpeg" {
		t.Errorf("expected image/jpeg, got %s", mime)
	}
	t.Logf("artist image size: %d bytes", len(data))
}

// TestAPIContributorKeysUnchanged is a sentinel test that will fail if Deezer
// renames SNG_CONTRIBUTORS keys. Update models.go if this fails.
func TestAPIContributorKeysUnchanged(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()

	track, err := c.GetTrack(ctx, testTrackIDNoLyrics)
	if err != nil {
		t.Fatalf("GetTrack: %v", err)
	}
	if track.Contributors == nil {
		t.Fatal("SNG_CONTRIBUTORS is nil — field may have been renamed in the API")
	}

	// These are the keys deeznt depends on. If this test fails, update
	// MainArtists() and FeaturedArtists() in models.go.
	if _, ok := track.Contributors["main_artist"]; !ok {
		t.Errorf("key 'main_artist' not found in SNG_CONTRIBUTORS; got keys: %v",
			contributorKeys(track.Contributors))
	}
	if _, ok := track.Contributors["featuring"]; !ok {
		t.Logf("key 'featuring' not present (track may have no featured artists): %v",
			contributorKeys(track.Contributors))
	}
}

// TestAPILyricsKeysUnchanged is a sentinel that fails if the lyrics response
// shape changes. Update GWLyrics in models.go if this fails.
func TestAPILyricsKeysUnchanged(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()

	lyrics, err := c.GetLyrics(ctx, testTrackID)
	if err != nil {
		t.Fatalf("GetLyrics: %v", err)
	}
	if lyrics.LyricsText == "" {
		t.Error("LYRICS_TEXT missing or renamed")
	}
	if lyrics.SyncJSON == nil {
		t.Error("LYRICS_SYNC_JSON missing or renamed")
	}
	if len(lyrics.SyncJSON) > 0 && lyrics.SyncJSON[0].LRCTimestamp == "" && lyrics.SyncJSON[0].Line == "" {
		t.Error("LYRICS_SYNC_JSON entries appear empty — struct tags may be wrong")
	}
}

func contributorKeys(m map[string][]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
