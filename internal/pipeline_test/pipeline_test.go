// Package pipeline_test contains integration tests for the full sync→download→tag
// pipeline using a mock Deezer API server. No real network calls are made.
package pipeline_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"deeznt/internal/config"
	"deeznt/internal/deezer"
	"deeznt/internal/downloader"
	"deeznt/internal/store"
)

// ── Mock Deezer server ────────────────────────────────────────────────────────

// mockDeezer is a minimal GW API mock. Register method handlers via Handle().
type mockDeezer struct {
	mu       sync.Mutex
	handlers map[string]func(args map[string]any) any
	srv      *httptest.Server
}

func newMockDeezer(t *testing.T) *mockDeezer {
	t.Helper()
	m := &mockDeezer{handlers: map[string]func(args map[string]any) any{}}
	m.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method := r.URL.Query().Get("method")
		var args map[string]any
		_ = json.NewDecoder(r.Body).Decode(&args)

		m.mu.Lock()
		h, ok := m.handlers[method]
		m.mu.Unlock()

		var result any
		if ok {
			result = h(args)
		} else {
			result = map[string]any{}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"error": []any{}, "results": result})
	}))
	t.Cleanup(m.srv.Close)
	return m
}

func (m *mockDeezer) Handle(method string, h func(args map[string]any) any) {
	m.mu.Lock()
	m.handlers[method] = h
	m.mu.Unlock()
}

// mockMediaServer serves fake encrypted audio and get_url responses.
func newMockMediaServer(t *testing.T, audioData []byte) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v1/get_url") || r.URL.Path == "/v1/get_url" {
			// Return a URL pointing back to this server for the audio.
			baseURL := "http://" + r.Host
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]any{{
					"media": []map[string]any{{
						"sources": []map[string]any{{"url": baseURL + "/audio"}},
					}},
				}},
			})
			return
		}
		// Serve fake (unencrypted, all-zeros) audio.
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(audioData)))
		_, _ = w.Write(audioData)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// ── Test fixtures ─────────────────────────────────────────────────────────────

// fakeEncryptedFLAC returns bytes that will survive DecryptTrack (which XORs
// 2048-byte blocks) without crashing — all zeros is fine for tests.
func fakeAudio() []byte {
	return make([]byte, 6144) // exactly one BF_CBC_STRIPE block
}

func trackJSON(sngID, albID, status int, title, artName, albTitle string, fallback *deezer.GWTrack) map[string]any {
	t := map[string]any{
		"SNG_ID":                fmt.Sprintf("%d", sngID),
		"SNG_TITLE":             title,
		"ART_ID":                "1",
		"ART_NAME":              artName,
		"ALB_ID":                fmt.Sprintf("%d", albID),
		"ALB_TITLE":             albTitle,
		"ALB_PICTURE":           "abc123",
		"TRACK_NUMBER":          "1",
		"DISK_NUMBER":           "1",
		"DURATION":              "180",
		"MD5_ORIGIN":            "aabbccdd00112233aabbccdd00112233",
		"MEDIA_VERSION":         "1",
		"ISRC":                  "US1234567890",
		"GAIN":                  "-10.0",
		"EXPLICIT_LYRICS":       "0",
		"LYRICS_ID":             0,
		"GENRE_ID":              "132",
		"PHYSICAL_RELEASE_DATE": "2020-01-01",
		"DIGITAL_RELEASE_DATE":  "2020-01-01",
		"COPYRIGHT":             "© 2020 Test",
		"STATUS":                status,
		"TRACK_TOKEN":           "fake-token",
		"TRACK_TOKEN_EXPIRE":    9999999999,
		"SNG_CONTRIBUTORS": map[string]any{
			"main_artist": []string{artName},
		},
		"ARTISTS": []map[string]any{{
			"ART_ID":      "1",
			"ART_NAME":    artName,
			"ART_PICTURE": "artist_pic_hash",
			"ROLE_ID":     "0",
		}},
		"FILESIZE_MP3_128": "1000000",
		"FILESIZE_FLAC":    "5000000",
	}
	if fallback != nil {
		raw, _ := json.Marshal(fallback)
		var fb map[string]any
		_ = json.Unmarshal(raw, &fb)
		t["FALLBACK"] = fb
	}
	return t
}

func albumJSON(albID int, title, artName string, tracks []map[string]any) map[string]any {
	return map[string]any{
		"ALB_ID":                fmt.Sprintf("%d", albID),
		"ALB_TITLE":             title,
		"ART_NAME":              artName,
		"LABEL_NAME":            "Test Label",
		"NUMBER_TRACK":          fmt.Sprintf("%d", len(tracks)),
		"NUMBER_DISK":           "1",
		"PHYSICAL_RELEASE_DATE": "2020-01-01",
		"DIGITAL_RELEASE_DATE":  "2020-01-01",
		"GENRE_ID":              "132",
		"COPYRIGHT":             "© 2020 Test",
	}
}

// ── Pipeline harness ──────────────────────────────────────────────────────────

type testHarness struct {
	t       *testing.T
	dir     string
	st      *store.Store
	cfg     *config.Config
	dz      *deezer.Client
	pipe    *downloader.Pipeline
	mock    *mockDeezer
	mediaSrv *httptest.Server
}

func newHarness(t *testing.T) *testHarness {
	t.Helper()
	dir := t.TempDir()

	st, err := store.Open(filepath.Join(dir, "deeznt.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	cfg.Paths.MusicDir = filepath.Join(dir, "music")
	_ = os.MkdirAll(cfg.Paths.MusicDir, 0o755)
	cfg.Download.Concurrency = 2
	cfg.Tag.Auto = false     // manual control in tests
	cfg.Convert.Enabled = false // no ffmpeg in unit tests

	mock := newMockDeezer(t)
	mediaSrv := newMockMediaServer(t, fakeAudio())

	// Bootstrap login handler.
	mock.Handle("deezer.getUserData", func(_ map[string]any) any {
		return map[string]any{
			"USER":      map[string]any{"USER_ID": float64(12345), "OPTIONS": map[string]any{"license_token": "fake-license", "web_lossless": true, "web_hq": true, "mobile_hq": false, "mobile_lossless": false, "license_country": "US"}},
			"checkForm": "fake-token",
		}
	})
	// Media get_url.
	mock.Handle("media.get_url", func(_ map[string]any) any {
		return map[string]any{"data": []map[string]any{{"media": []map[string]any{{"sources": []map[string]any{{"url": mediaSrv.URL + "/audio"}}}}}}}
	})

	dz, err := deezer.NewWithBaseURLs("fake-arl", mock.srv.URL, mediaSrv.URL+"/v1/get_url")
	if err != nil {
		t.Fatalf("new deezer client: %v", err)
	}
	if err := dz.Login(context.Background()); err != nil {
		t.Fatalf("login: %v", err)
	}

	pipe := downloader.New(st, dz, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))

	return &testHarness{t: t, dir: dir, st: st, cfg: cfg, dz: dz, pipe: pipe, mock: mock, mediaSrv: mediaSrv}
}

// registerAlbumHandlers sets up mock responses for a full album with n tracks.
func (h *testHarness) registerAlbum(albID int, title, artName string, trackIDs []int, status int, replacementAlbID int, replacementTrackIDs []int) {
	// Build track list for the album.
	var tracks []map[string]any
	for i, sngID := range trackIDs {
		var fb *deezer.GWTrack
		if status == 3 && i < len(replacementTrackIDs) {
			fb = &deezer.GWTrack{
				SngID:    fmt.Sprintf("%d", replacementTrackIDs[i]),
				AlbID:    fmt.Sprintf("%d", replacementAlbID),
				AlbTitle: title + " (Reissue)",
				ArtName:  artName,
				SngTitle: fmt.Sprintf("Track %d", i+1),
				Status:   1,
				TrackToken: "fake-token-rep",
				MD5Origin: "aabbccdd00112233aabbccdd00112233",
				MediaVersion: "1",
			}
		}
		t := trackJSON(sngID, albID, status, fmt.Sprintf("Track %d", i+1), artName, title, fb)
		t["TRACK_NUMBER"] = fmt.Sprintf("%d", i+1)
		tracks = append(tracks, t)
	}

	// song.getListByAlbum
	h.mock.Handle("song.getListByAlbum", func(args map[string]any) any {
		return map[string]any{"data": tracks, "total": len(tracks), "count": len(tracks)}
	})

	// album.getData
	h.mock.Handle("album.getData", func(_ map[string]any) any {
		return albumJSON(albID, title, artName, tracks)
	})

	// song.getData — return the track matching the requested SNG_ID.
	h.mock.Handle("song.getData", func(args map[string]any) any {
		sngIDRaw, _ := args["SNG_ID"].(float64)
		sngID := int(sngIDRaw)
		for _, t := range tracks {
			if t["SNG_ID"] == fmt.Sprintf("%d", sngID) {
				return t
			}
		}
		// Check replacement tracks too.
		if replacementAlbID > 0 {
			for i, repID := range replacementTrackIDs {
				if repID == sngID {
					return trackJSON(repID, replacementAlbID, 1,
						fmt.Sprintf("Track %d", i+1), artName, title+" (Reissue)", nil)
				}
			}
		}
		return map[string]any{"SNG_ID": fmt.Sprintf("%d", sngID), "STATUS": 1, "TRACK_TOKEN": "fake-token"}
	})

	// deezer.pageProfile albums tab.
	h.mock.Handle("deezer.pageProfile", func(_ map[string]any) any {
		return map[string]any{"TAB": map[string]any{"albums": map[string]any{"data": []map[string]any{{"ALB_ID": fmt.Sprintf("%d", albID), "ALB_TITLE": title, "ART_NAME": artName}}}}}
	})

	// song.getFavoriteIds — empty for these tests.
	h.mock.Handle("song.getFavoriteIds", func(_ map[string]any) any {
		return map[string]any{"data": []any{}, "total": 0}
	})
}

// syncAlbums runs a sync for albums.
func (h *testHarness) syncAlbums(t *testing.T) downloader.SyncResult {
	t.Helper()
	res, err := h.pipe.Sync(context.Background(), deezer.Selection{Albums: true}, false)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	return res
}

// downloadAll drains the download queue.
func (h *testHarness) downloadAll(t *testing.T) downloader.DownloadResult {
	t.Helper()
	res, err := h.pipe.RunDownloads(context.Background())
	if err != nil {
		t.Fatalf("RunDownloads: %v", err)
	}
	return res
}

// ── Tests ─────────────────────────────────────────────────────────────────────

func TestDownloadSingleTrack(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	// Register a single-track album.
	h.registerAlbum(100, "Solo Album", "Artist One", []int{1001}, 1, 0, nil)

	// Manually enqueue the track (simulate sync of a single track).
	h.mock.Handle("song.getData", func(_ map[string]any) any {
		return trackJSON(1001, 100, 1, "My Song", "Artist One", "Solo Album", nil)
	})
	n, err := h.pipe.EnqueueIDs(ctx, deezer.KindTrack, []int64{1001})
	if err != nil {
		t.Fatalf("EnqueueIDs: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 track queued, got %d", n)
	}

	// Verify item is in queued state.
	it, _ := h.st.Get(ctx, 1001)
	if it == nil || it.State != store.StateQueued {
		t.Fatalf("expected queued, got %v", it)
	}

	// Download.
	res := h.downloadAll(t)
	if res.Downloaded != 1 || res.Failed != 0 {
		t.Fatalf("download result: %+v", res)
	}

	// Verify state after download.
	it, _ = h.st.Get(ctx, 1001)
	if it == nil {
		t.Fatal("item not found after download")
	}
	if it.State != store.StateDownloaded {
		t.Fatalf("expected downloaded, got %q", it.State)
	}
	if it.FilePath == "" {
		t.Fatal("FilePath should be set after download")
	}
	if it.Format != "FLAC" {
		t.Fatalf("expected FLAC, got %q", it.Format)
	}

	// Verify file exists on disk.
	full := filepath.Join(h.cfg.Paths.MusicDir, it.FilePath)
	if _, err := os.Stat(full); err != nil {
		t.Fatalf("downloaded file not on disk: %v", err)
	}
}

func TestDownloadAlbum(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	trackIDs := []int{2001, 2002, 2003}
	h.registerAlbum(200, "Full Album", "Band Name", trackIDs, 1, 0, nil)

	// Sync.
	res := h.syncAlbums(t)
	if res.Sources != 1 {
		t.Fatalf("expected 1 source, got %d", res.Sources)
	}
	if res.Total != 3 || res.New != 3 {
		t.Fatalf("sync result: %+v", res)
	}

	// Verify source created as PRESENT.
	src, err := h.st.GetSource(ctx, store.SourceKindAlbum, 200)
	if err != nil || src == nil {
		t.Fatalf("source not found: %v", err)
	}
	if src.DeezerStatus != store.DeezerStatusPresent {
		t.Errorf("source deezer_status = %q, want PRESENT", src.DeezerStatus)
	}
	if src.TrackCount != 3 {
		t.Errorf("source track_count = %d, want 3", src.TrackCount)
	}

	// Verify all tracks are queued and PRESENT.
	for _, id := range trackIDs {
		it, _ := h.st.Get(ctx, int64(id))
		if it == nil {
			t.Fatalf("track %d not found", id)
		}
		if it.State != store.StateWaiting && it.State != store.StateQueued {
			t.Errorf("track %d state = %q, want waiting or queued", id, it.State)
		}
		if it.DeezerStatus != store.DeezerStatusPresent {
			t.Errorf("track %d deezer_status = %q, want PRESENT", id, it.DeezerStatus)
		}
		if it.TrackData == "" {
			t.Errorf("track %d has no track_data", id)
		}
	}

	// Verify album_cache populated.
	cached, _ := h.st.GetAlbumCache(ctx, 200)
	if cached == "" {
		t.Error("album_cache not populated for alb_id=200")
	}

	// Download all.
	dlRes := h.downloadAll(t)
	if dlRes.Downloaded != 3 || dlRes.Failed != 0 {
		t.Fatalf("download result: %+v", dlRes)
	}

	// All tracks should be downloaded.
	for _, id := range trackIDs {
		it, _ := h.st.Get(ctx, int64(id))
		if it.State != store.StateDownloaded {
			t.Errorf("track %d state = %q after download", id, it.State)
		}
		full := filepath.Join(h.cfg.Paths.MusicDir, it.FilePath)
		if _, err := os.Stat(full); err != nil {
			t.Errorf("track %d file not on disk: %v", id, err)
		}
	}

	// Status counts should show 3 downloaded (PRESENT only).
	counts, _ := h.st.CountByState(ctx)
	if counts[store.StateDownloaded] != 3 {
		t.Errorf("CountByState downloaded = %d, want 3; full counts: %v", counts[store.StateDownloaded], counts)
	}
}

func TestDownloadReplacedAlbum(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	// Old album 300 has all tracks replaced, pointing to new album 301.
	oldIDs := []int{3001, 3002}
	newIDs := []int{4001, 4002}
	h.registerAlbum(300, "Old Album", "Artist", oldIDs, 3, 301, newIDs)

	// Also register new album 301 for album.getData calls on replacement tracks.
	h.mock.Handle("album.getData", func(args map[string]any) any {
		albIDRaw, _ := args["ALB_ID"].(float64)
		if int(albIDRaw) == 301 {
			return albumJSON(301, "New Album", "Artist", nil)
		}
		return albumJSON(300, "Old Album", "Artist", nil)
	})

	// Sync.
	syncRes := h.syncAlbums(t)
	if syncRes.Sources != 1 {
		t.Fatalf("expected 1 source, got %d", syncRes.Sources)
	}
	// Should have 4 total: 2 original (REPLACED) + 2 replacement (PRESENT).
	if syncRes.Total != 4 {
		t.Errorf("expected 4 total tracks (2 original + 2 replacement), got %d", syncRes.Total)
	}
	if syncRes.New != 4 {
		t.Errorf("expected 4 new, got %d", syncRes.New)
	}

	// Original tracks should be REPLACED.
	for i, id := range oldIDs {
		it, _ := h.st.Get(ctx, int64(id))
		if it == nil {
			t.Fatalf("original track %d not found", id)
		}
		if it.DeezerStatus != store.DeezerStatusReplaced {
			t.Errorf("original track %d deezer_status = %q, want REPLACED", id, it.DeezerStatus)
		}
		if it.ReplacementID != int64(newIDs[i]) {
			t.Errorf("original track %d replacement_id = %d, want %d", id, it.ReplacementID, newIDs[i])
		}
	}

	// Replacement tracks should be PRESENT and queued.
	for _, id := range newIDs {
		it, _ := h.st.Get(ctx, int64(id))
		if it == nil {
			t.Fatalf("replacement track %d not found", id)
		}
		if it.DeezerStatus != store.DeezerStatusPresent {
			t.Errorf("replacement track %d deezer_status = %q, want PRESENT", id, it.DeezerStatus)
		}
		if it.State != store.StateWaiting && it.State != store.StateQueued {
			t.Errorf("replacement track %d state = %q, want waiting or queued", id, it.State)
		}
	}

	// Source album should be REPLACED with replacement_id=301.
	src, _ := h.st.GetSource(ctx, store.SourceKindAlbum, 300)
	if src == nil {
		t.Fatal("source not found")
	}
	if src.DeezerStatus != store.DeezerStatusReplaced {
		t.Errorf("source deezer_status = %q, want REPLACED", src.DeezerStatus)
	}
	if src.ReplacementID != 301 {
		t.Errorf("source replacement_id = %d, want 301", src.ReplacementID)
	}

	// Download — should only download replacement tracks (PRESENT).
	dlRes := h.downloadAll(t)
	if dlRes.Downloaded != 2 {
		t.Errorf("expected 2 downloads (replacements only), got %d", dlRes.Downloaded)
	}
	if dlRes.Failed != 0 {
		t.Errorf("expected 0 failures, got %d", dlRes.Failed)
	}

	// Original REPLACED tracks should NOT have been downloaded.
	for _, id := range oldIDs {
		it, _ := h.st.Get(ctx, int64(id))
		if it.FilePath != "" {
			t.Errorf("original REPLACED track %d should not have been downloaded (has file_path)", id)
		}
	}

	// Status counts should only reflect PRESENT tracks (2 downloaded).
	counts, _ := h.st.CountByState(ctx)
	if counts[store.StateDownloaded] != 2 {
		t.Errorf("CountByState downloaded = %d, want 2 (PRESENT only); full: %v", counts[store.StateDownloaded], counts)
	}

	// DeezerStatus counts show both.
	dsCounts, _ := h.st.CountByDeezerStatus(ctx)
	if dsCounts[store.DeezerStatusPresent] != 2 {
		t.Errorf("DeezerStatus PRESENT = %d, want 2", dsCounts[store.DeezerStatusPresent])
	}
	if dsCounts[store.DeezerStatusReplaced] != 2 {
		t.Errorf("DeezerStatus REPLACED = %d, want 2", dsCounts[store.DeezerStatusReplaced])
	}
}

func TestReplacedAlbumStateInheritance(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	oldIDs := []int{5001}
	newIDs := []int{6001}
	h.registerAlbum(500, "Old", "Artist", oldIDs, 3, 600, newIDs)
	h.mock.Handle("album.getData", func(_ map[string]any) any {
		return albumJSON(600, "New", "Artist", nil)
	})

	// First sync — both tracks upserted, replacement queued, original REPLACED.
	h.syncAlbums(t)

	// Download the replacement.
	h.downloadAll(t)

	// Mark replacement as converted (simulating full pipeline completion).
	_ = h.st.MarkTagged(ctx, int64(newIDs[0]))
	_ = h.st.MarkConverted(ctx, int64(newIDs[0]))

	// Re-sync — original REPLACED item should now mirror the replacement's state.
	h.syncAlbums(t)

	orig, _ := h.st.Get(ctx, int64(oldIDs[0]))
	rep, _ := h.st.Get(ctx, int64(newIDs[0]))

	if rep.State != store.StateConverted {
		t.Fatalf("replacement state = %q, want converted", rep.State)
	}
	if orig.State != store.StateConverted {
		t.Errorf("original REPLACED state = %q, want converted (should mirror replacement)", orig.State)
	}
	if orig.FilePath != rep.FilePath {
		t.Errorf("original file_path %q != replacement %q", orig.FilePath, rep.FilePath)
	}
}

func TestSyncIdempotent(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	h.registerAlbum(700, "Album", "Artist", []int{7001, 7002}, 1, 0, nil)

	res1 := h.syncAlbums(t)
	if res1.New != 2 {
		t.Fatalf("first sync: expected 2 new, got %d", res1.New)
	}

	res2 := h.syncAlbums(t)
	if res2.New != 0 {
		t.Fatalf("second sync: expected 0 new (idempotent), got %d", res2.New)
	}

	// Counts should be the same.
	items, _ := h.st.List(ctx, []string{store.StateWaiting}, 0)
	if len(items) != 2 {
		t.Errorf("expected 2 waiting items, got %d", len(items))
	}
}

func TestCountByStateExcludesReplaced(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	// 2 tracks: 1 PRESENT (queued), 1 REPLACED (queued).
	_, _ = h.st.Upsert(ctx, store.Discovered{SngID: 8001, Title: "A", DeezerStatus: store.DeezerStatusPresent})
	_, _ = h.st.Upsert(ctx, store.Discovered{SngID: 8002, Title: "B", DeezerStatus: store.DeezerStatusReplaced, ReplacementID: 8001})
	_ = h.st.SetState(ctx, 8001, store.StateQueued)
	_ = h.st.SetState(ctx, 8002, store.StateQueued)

	counts, err := h.st.CountByState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Should only count PRESENT item.
	if counts[store.StateQueued] != 1 {
		t.Errorf("CountByState queued = %d, want 1 (REPLACED excluded); full: %v", counts[store.StateQueued], counts)
	}
}

func TestClaimDownloadSkipsReplaced(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	// Insert one REPLACED and one PRESENT track, both queued.
	_, _ = h.st.Upsert(ctx, store.Discovered{SngID: 9001, Title: "Replaced", DeezerStatus: store.DeezerStatusReplaced})
	_, _ = h.st.Upsert(ctx, store.Discovered{SngID: 9002, Title: "Present", DeezerStatus: store.DeezerStatusPresent})
	_ = h.st.SetState(ctx, 9001, store.StateQueued)
	_ = h.st.SetState(ctx, 9002, store.StateQueued)

	claimed, ok, err := h.st.ClaimDownload(ctx)
	if err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}
	if claimed.SngID != 9002 {
		t.Errorf("claimed sng_id = %d, want 9002 (PRESENT); REPLACED should be skipped", claimed.SngID)
	}

	// Second claim should return nothing (REPLACED is the only remaining queued item).
	claimed2, ok2, _ := h.st.ClaimDownload(ctx)
	if ok2 {
		t.Errorf("expected no more items to claim, got sng_id=%d", claimed2.SngID)
	}
}

// helpers

type nopCloser struct{ io.Reader }

func (nopCloser) Close() error { return nil }
