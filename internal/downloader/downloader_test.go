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

	p := New(st, nil, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	return p, st, cfg.Paths.MusicDir
}

func TestMissingFilesAndForceModes(t *testing.T) {
	ctx := context.Background()
	p, st, musicDir := testPipeline(t)

	st.Upsert(ctx, store.Discovered{SngID: 1})
	st.ClaimDownload(ctx)
	rel := filepath.Join("Artist", "Album", "01 Song.flac")
	st.MarkDownloaded(ctx, 1, "FLAC", rel)
	st.MarkFinished(ctx, 1)
	full := filepath.Join(musicDir, rel)
	os.MkdirAll(filepath.Dir(full), 0o755)
	os.WriteFile(full, []byte("audio"), 0o644)

	if miss, err := p.MissingFiles(ctx); err != nil || len(miss) != 0 {
		t.Fatalf("missing=%v err=%v, want none", miss, err)
	}

	os.Remove(full)
	miss, err := p.MissingFiles(ctx)
	if err != nil || len(miss) != 1 || miss[0].SngID != 1 {
		t.Fatalf("missing=%v err=%v, want sng 1", miss, err)
	}
	if it, _ := st.Get(ctx, 1); it == nil || it.State != store.StateFinished {
		t.Fatal("row must not be deleted or altered by verify")
	}

	n, err := p.ForceMissing(ctx)
	if err != nil || n != 1 {
		t.Fatalf("ForceMissing n=%d err=%v", n, err)
	}
	it, _ := st.Get(ctx, 1)
	if it.State != store.StateQueued || it.FilePath != rel {
		t.Fatalf("after force-missing state=%q file=%q", it.State, it.FilePath)
	}

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
	_, hard = g.hit()
	if !hard {
		t.Fatal("expected hard stop at max_hits")
	}
	if _, h := g.blockedFor(); !h {
		t.Fatal("blockedFor should report hard stop")
	}
}

func TestRunDownloadsBatchRetry(t *testing.T) {
	dir := t.TempDir()
	st, _ := store.Open(filepath.Join(dir, "t.db"))
	defer st.Close()

	ctx := context.Background()

	for _, id := range []int64{1, 2} {
		st.Upsert(ctx, store.Discovered{SngID: id, Title: "T"})
		st.ClaimDownload(ctx)
		st.MarkInFailedBatch(ctx, []int64{id}, store.StageDownload, "timeout")
	}

	failed, err := st.ClaimFailedBatch(ctx)
	if err != nil || len(failed) != 2 {
		t.Fatalf("expected 2 failed-batch items, got %d err=%v", len(failed), err)
	}

	ids := []int64{1, 2}
	if err := st.RequeueFailedBatch(ctx, ids); err != nil {
		t.Fatal(err)
	}
	it1, _ := st.Get(ctx, 1)
	if it1.State != store.StateQueued || it1.BatchAttempts != 1 || it1.InFailedBatch {
		t.Fatalf("after retry pass 1: state=%q batch_attempts=%d in_failed=%v",
			it1.State, it1.BatchAttempts, it1.InFailedBatch)
	}

	st.MarkInFailedBatch(ctx, ids, store.StageDownload, "still failing")
	for _, id := range ids {
		it, _ := st.Get(ctx, id)
		st.MarkFailed(ctx, it.SngID, store.StageDownload, it.Error)
	}

	it1, _ = st.Get(ctx, 1)
	it2, _ := st.Get(ctx, 2)
	if it1.State != store.StateFailed || it1.InFailedBatch {
		t.Fatalf("item 1 permanently failed: state=%q in_failed=%v", it1.State, it1.InFailedBatch)
	}
	if it2.State != store.StateFailed || it2.InFailedBatch {
		t.Fatalf("item 2 permanently failed: state=%q in_failed=%v", it2.State, it2.InFailedBatch)
	}

	n, err := st.RequeueAllFailed(ctx)
	if err != nil || n != 2 {
		t.Fatalf("RequeueAllFailed n=%d err=%v", n, err)
	}
	it1, _ = st.Get(ctx, 1)
	if it1.State != store.StateQueued || it1.BatchAttempts != 0 {
		t.Fatalf("after RequeueAllFailed: state=%q batch_attempts=%d", it1.State, it1.BatchAttempts)
	}
}

func TestRunDownloadsStopDeletesTempFile(t *testing.T) {
	dir := t.TempDir()
	st, _ := store.Open(filepath.Join(dir, "t.db"))
	defer st.Close()

	cfg, _ := config.Load("")
	cfg.Paths.MusicDir = filepath.Join(dir, "music")
	os.MkdirAll(cfg.Paths.MusicDir, 0o755)

	p := New(st, nil, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))

	os.MkdirAll(p.incompleteDir, 0o755)
	tmpFile := filepath.Join(p.incompleteDir, "42.part")
	os.WriteFile(tmpFile, []byte("partial"), 0o644)

	p.CleanIncomplete()

	if _, err := os.Stat(tmpFile); !os.IsNotExist(err) {
		t.Fatal("expected temp file to be deleted by CleanIncomplete")
	}
}
