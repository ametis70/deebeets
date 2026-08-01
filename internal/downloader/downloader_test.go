package downloader

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"deebeets/internal/config"
	"deebeets/internal/store"
)

func testPipeline(t *testing.T) (*Pipeline, *store.Store, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	cfg, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Paths.MusicDir = filepath.Join(dir, "music")
	os.MkdirAll(cfg.Paths.MusicDir, 0o755)

	p := New(st, nil, cfg, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	return p, st, cfg.Paths.MusicDir
}

func TestMissingFilesAndForceModes(t *testing.T) {
	ctx := context.Background()
	p, st, musicDir := testPipeline(t)

	// A finished item whose file exists.
	st.Upsert(ctx, store.Discovered{SngID: 1})
	st.ClaimDownload(ctx)
	rel := filepath.Join("Artist", "Album", "01 Song.flac")
	st.MarkDownloaded(ctx, 1, "FLAC", rel)
	st.MarkFinished(ctx, 1)
	full := filepath.Join(musicDir, rel)
	os.MkdirAll(filepath.Dir(full), 0o755)
	os.WriteFile(full, []byte("audio"), 0o644)

	// Nothing missing yet.
	if miss, err := p.MissingFiles(ctx); err != nil || len(miss) != 0 {
		t.Fatalf("missing=%v err=%v, want none", miss, err)
	}

	// Delete the file: it must be reported missing but the row must remain.
	os.Remove(full)
	miss, err := p.MissingFiles(ctx)
	if err != nil || len(miss) != 1 || miss[0].SngID != 1 {
		t.Fatalf("missing=%v err=%v, want sng 1", miss, err)
	}
	if it, _ := st.Get(ctx, 1); it == nil || it.State != store.StateFinished {
		t.Fatal("row must not be deleted or altered by verify")
	}

	// force-missing requeues it while keeping the recorded path.
	n, err := p.ForceMissing(ctx)
	if err != nil || n != 1 {
		t.Fatalf("ForceMissing n=%d err=%v", n, err)
	}
	it, _ := st.Get(ctx, 1)
	if it.State != store.StateQueued || it.FilePath != rel {
		t.Fatalf("after force-missing state=%q file=%q", it.State, it.FilePath)
	}

	// force-all clears the recorded path.
	st.MarkFinished(ctx, 1)
	n, err = p.ForceAll(ctx, nil)
	if err != nil || n != 1 {
		t.Fatalf("ForceAll n=%d err=%v", n, err)
	}
	it, _ = st.Get(ctx, 1)
	if it.State != store.StateQueued || it.FilePath != "" {
		t.Fatalf("after force-all state=%q file=%q", it.State, it.FilePath)
	}
}

func TestRateGate(t *testing.T) {
	g := newRateGate(10*time.Millisecond, time.Minute, 3)

	if d, hard := g.blockedFor(); d != 0 || hard {
		t.Fatal("gate should start open")
	}
	w1, hard := g.hit()
	if hard || w1 != 10*time.Millisecond {
		t.Fatalf("hit1 wait=%v hard=%v", w1, hard)
	}
	w2, _ := g.hit()
	if w2 != 20*time.Millisecond {
		t.Fatalf("hit2 wait=%v, want exponential 20ms", w2)
	}
	_, hard = g.hit() // 3rd hit within window -> hard stop
	if !hard {
		t.Fatal("expected hard stop at max_hits")
	}
	if _, h := g.blockedFor(); !h {
		t.Fatal("blockedFor should report hard stop")
	}
}
