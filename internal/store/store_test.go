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

func TestClaimDownloadFlow(t *testing.T) {
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
	s.ClaimDownload(ctx) // -> downloading

	n, err := s.RecoverInterrupted(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("recovered %d, want 1", n)
	}
	counts, _ := s.CountByState(ctx)
	if counts[StateQueued] != 1 {
		t.Fatalf("counts = %v, want queued=1", counts)
	}
}

func TestBlocklist(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)

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

	n, err := s.Requeue(ctx, []int64{1}, false)
	if err != nil || n != 1 {
		t.Fatalf("requeue n=%d err=%v", n, err)
	}
	it, _ := s.Get(ctx, 1)
	if it.State != StateQueued || it.FilePath != "path.flac" {
		t.Fatalf("state=%q file=%q", it.State, it.FilePath)
	}

	s.MarkFinished(ctx, 1)
	s.Requeue(ctx, []int64{1}, true)
	it, _ = s.Get(ctx, 1)
	if it.State != StateQueued || it.FilePath != "" {
		t.Fatalf("force-all state=%q file=%q", it.State, it.FilePath)
	}
}

func TestBatchRetry(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)

	// Set up three items: mark two as in-failed-batch.
	for _, id := range []int64{1, 2, 3} {
		s.Upsert(ctx, Discovered{SngID: id})
		s.ClaimDownload(ctx)
	}
	s.MarkDownloaded(ctx, 3, "FLAC", "p.flac")
	s.MarkFinished(ctx, 3)

	if err := s.MarkInFailedBatch(ctx, []int64{1, 2}, StageDownload, "timeout"); err != nil {
		t.Fatal(err)
	}

	failed, err := s.ClaimFailedBatch(ctx)
	if err != nil || len(failed) != 2 {
		t.Fatalf("claim failed batch: got %d items err=%v", len(failed), err)
	}
	for _, it := range failed {
		if !it.InFailedBatch {
			t.Errorf("item %d: InFailedBatch should be true", it.SngID)
		}
	}

	// Requeue for retry: batch_attempts should increment, in_failed_batch cleared.
	if err := s.RequeueFailedBatch(ctx, []int64{1, 2}); err != nil {
		t.Fatal(err)
	}
	it1, _ := s.Get(ctx, 1)
	if it1.State != StateQueued || it1.InFailedBatch || it1.BatchAttempts != 1 {
		t.Fatalf("after requeue: state=%q in_failed=%v batch_attempts=%d",
			it1.State, it1.InFailedBatch, it1.BatchAttempts)
	}

	// RequeueAllFailed resets batch_attempts too.
	s.MarkFailed(ctx, 1, StageDownload, "still failing")
	n, err := s.RequeueAllFailed(ctx)
	if err != nil || n != 1 {
		t.Fatalf("RequeueAllFailed n=%d err=%v", n, err)
	}
	it1, _ = s.Get(ctx, 1)
	if it1.State != StateQueued || it1.BatchAttempts != 0 {
		t.Fatalf("after RequeueAllFailed: state=%q batch_attempts=%d",
			it1.State, it1.BatchAttempts)
	}
}

func TestMarkAllDownloadedFinished(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)

	for _, id := range []int64{1, 2, 3} {
		s.Upsert(ctx, Discovered{SngID: id})
		s.ClaimDownload(ctx)
		s.MarkDownloaded(ctx, id, "FLAC", "f")
	}
	// One stays downloaded, two get finished manually — only the remaining one
	// should be affected by MarkAllDownloadedFinished.
	s.MarkFinished(ctx, 1)

	if err := s.MarkAllDownloadedFinished(ctx); err != nil {
		t.Fatal(err)
	}
	counts, _ := s.CountByState(ctx)
	if counts[StateDownloaded] != 0 || counts[StateFinished] != 3 {
		t.Fatalf("after MarkAllDownloadedFinished: counts=%v", counts)
	}
}
