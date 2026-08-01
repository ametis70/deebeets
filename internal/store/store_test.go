package store

import (
	"context"
	"path/filepath"
	"testing"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestUpsertIdempotent(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)

	d := Discovered{SngID: 1, Title: "A", Artist: "X", Album: "Alb", GroupKey: "100"}
	inserted, err := s.Upsert(ctx, d)
	if err != nil || !inserted {
		t.Fatalf("first upsert inserted=%v err=%v", inserted, err)
	}

	// Advance state, then re-sync: state must be preserved, metadata refreshed.
	if err := s.MarkFinished(ctx, 1); err != nil {
		t.Fatal(err)
	}
	d.Title = "A2"
	inserted, err = s.Upsert(ctx, d)
	if err != nil || inserted {
		t.Fatalf("second upsert inserted=%v err=%v", inserted, err)
	}
	it, _ := s.Get(ctx, 1)
	if it.State != StateFinished {
		t.Errorf("state = %q, want finished (re-sync must not resurrect)", it.State)
	}
	if it.Title != "A2" {
		t.Errorf("title = %q, want refreshed A2", it.Title)
	}
}

func TestClaimDownloadAndImportFlow(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	s.Upsert(ctx, Discovered{SngID: 10, Title: "T"})

	it, ok, err := s.ClaimDownload(ctx)
	if err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}
	if it.State != StateDownloading || it.Attempts != 1 {
		t.Fatalf("claimed item state=%q attempts=%d", it.State, it.Attempts)
	}

	// Queue is now empty.
	if _, ok, _ := s.ClaimDownload(ctx); ok {
		t.Fatal("expected empty queue")
	}

	if err := s.MarkDownloaded(ctx, 10, "FLAC", "X/Alb/01 T.flac"); err != nil {
		t.Fatal(err)
	}
	imp, ok, err := s.ClaimImport(ctx)
	if err != nil || !ok || imp.State != StateImporting {
		t.Fatalf("claim import: ok=%v err=%v state=%q", ok, err, imp.State)
	}
	if err := s.MarkFinished(ctx, 10); err != nil {
		t.Fatal(err)
	}
	final, _ := s.Get(ctx, 10)
	if final.State != StateFinished || final.Format != "FLAC" {
		t.Fatalf("final state=%q format=%q", final.State, final.Format)
	}
}

func TestRecoverInterrupted(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	s.Upsert(ctx, Discovered{SngID: 1})
	s.Upsert(ctx, Discovered{SngID: 2})
	s.ClaimDownload(ctx) // -> downloading
	s.MarkDownloaded(ctx, 2, "MP3_320", "f")
	s.ClaimImport(ctx) // -> importing

	n, err := s.RecoverInterrupted(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("recovered %d, want 2", n)
	}
	counts, _ := s.CountByState(ctx)
	if counts[StateQueued] != 1 || counts[StateDownloaded] != 1 {
		t.Fatalf("counts = %v", counts)
	}
}

func TestBlocklist(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)

	// Blocklist a track before it exists: a later sync must mark it blocklisted.
	if err := s.AddBlock(ctx, KindTrack, 5, "nope"); err != nil {
		t.Fatal(err)
	}
	blocked, _ := s.IsBlocked(ctx, KindTrack, 5)
	if !blocked {
		t.Fatal("expected blocked")
	}
	s.Upsert(ctx, Discovered{SngID: 5, Title: "B"})
	it, _ := s.Get(ctx, 5)
	if it.State != StateBlocklisted {
		t.Fatalf("state = %q, want blocklisted", it.State)
	}

	// Unblock resets it to waiting.
	if err := s.RemoveBlock(ctx, KindTrack, 5); err != nil {
		t.Fatal(err)
	}
	it, _ = s.Get(ctx, 5)
	if it.State != StateWaiting {
		t.Fatalf("state = %q, want waiting after unblock", it.State)
	}
}

func TestRequeueForceModes(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	s.Upsert(ctx, Discovered{SngID: 1})
	s.ClaimDownload(ctx)
	s.MarkDownloaded(ctx, 1, "FLAC", "path.flac")
	s.MarkFinished(ctx, 1)

	// force-missing style: requeue without clearing file_path.
	n, err := s.Requeue(ctx, []int64{1}, false)
	if err != nil || n != 1 {
		t.Fatalf("requeue n=%d err=%v", n, err)
	}
	it, _ := s.Get(ctx, 1)
	if it.State != StateQueued || it.FilePath != "path.flac" {
		t.Fatalf("state=%q file=%q", it.State, it.FilePath)
	}

	// force-all style: clear the file path too.
	s.MarkFinished(ctx, 1)
	s.Requeue(ctx, []int64{1}, true)
	it, _ = s.Get(ctx, 1)
	if it.State != StateQueued || it.FilePath != "" {
		t.Fatalf("force-all state=%q file=%q", it.State, it.FilePath)
	}
}
