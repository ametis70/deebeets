package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load defaults: %v", err)
	}
	if cfg.Download.Concurrency != 3 {
		t.Errorf("concurrency = %d, want 3", cfg.Download.Concurrency)
	}
	if got := cfg.Deezer.FormatPriority; len(got) != 3 || got[0] != "FLAC" {
		t.Errorf("format_priority = %v, want FLAC first", got)
	}
	if !cfg.Download.Favorites.Tracks {
		t.Errorf("favorites.tracks should default true")
	}
	if cfg.Download.Retry.Backoff != 5*time.Second {
		t.Errorf("download.retry.backoff = %v, want 5s", cfg.Download.Retry.Backoff)
	}
	if cfg.Sync.Retry.Backoff != 10*time.Second {
		t.Errorf("sync.retry.backoff = %v, want 10s", cfg.Sync.Retry.Backoff)
	}
}

func TestLoadFileAndEnvOverride(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	body := `
[deezer]
format_priority = ["MP3_320", "MP3_128"]
[paths]
music_dir = "/tmp/music"
db_path = "/tmp/x.db"
socket_path = "/tmp/x.sock"
[download]
concurrency = 7
inter_batch_delay = "2s"
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("DEEZNT_DOWNLOAD_CONCURRENCY", "9")
	t.Setenv("DEEZNT_ARL", "secret-arl")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Paths.MusicDir != "/tmp/music" {
		t.Errorf("music_dir = %q", cfg.Paths.MusicDir)
	}
	if cfg.Download.InterBatchDelay != 2*time.Second {
		t.Errorf("inter_batch_delay = %v, want 2s", cfg.Download.InterBatchDelay)
	}
	if cfg.Download.Concurrency != 9 {
		t.Errorf("env override concurrency = %d, want 9", cfg.Download.Concurrency)
	}
	if cfg.Deezer.ARL != "secret-arl" {
		t.Errorf("DEEZNT_ARL override = %q", cfg.Deezer.ARL)
	}
}

func TestValidate(t *testing.T) {
	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	// Confirm validation passes for a valid config.
	if err := cfg.Validate(); err != nil {
		t.Errorf("valid config failed validation: %v", err)
	}

	cfg2, _ := Load("")
	cfg2.Deezer.FormatPriority = []string{"OGG_BAD"}
	if err := cfg2.Validate(); err == nil {
		t.Error("expected error for unknown format")
	}
}
