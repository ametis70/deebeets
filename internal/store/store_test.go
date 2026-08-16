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
	t.Cleanup(func() { s.Close() }) //nolint:errcheck
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

	if err := s.MarkConverted(ctx, 1); err != nil {
		t.Fatal(err)
	}
	d.Title = "A2"
	inserted, err = s.Upsert(ctx, d)
	if err != nil || inserted {
		t.Fatalf("second upsert inserted=%v err=%v", inserted, err)
	}
	it, _ := s.Get(ctx, 1)
	if it.State != StateConverted {
		t.Errorf("state = %q, want converted (re-sync must not resurrect)", it.State)
	}
	if it.Title != "A2" {
		t.Errorf("title = %q, want refreshed A2", it.Title)
	}
}

func TestClaimDownloadFlow(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	s.Upsert(ctx, Discovered{SngID: 10, Title: "T"}) //nolint:errcheck

	it, ok, err := s.ClaimDownload(ctx)
	if err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}
	if it.State != StateDownloading || it.Attempts != 1 {
		t.Fatalf("claimed item state=%q attempts=%d", it.State, it.Attempts)
	}

	if _, ok, _ := s.ClaimDownload(ctx); ok {
		t.Fatal("expected empty queue")
	}

	if err := s.MarkDownloaded(ctx, 10, "FLAC", "X/Alb/01 T.flac"); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkTagged(ctx, 10); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkConverted(ctx, 10); err != nil {
		t.Fatal(err)
	}
	final, _ := s.Get(ctx, 10)
	if final.State != StateConverted || final.Format != "FLAC" {
		t.Fatalf("final state=%q format=%q", final.State, final.Format)
	}
}

func TestRecoverInterrupted(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	s.Upsert(ctx, Discovered{SngID: 1}) //nolint:errcheck
	s.ClaimDownload(ctx) //nolint:errcheck // -> downloading

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

func TestRecoverInterruptedAllStages(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	for i, state := range []string{StateDownloading, StateTagging, StateConverting} {
		s.Upsert(ctx, Discovered{SngID: int64(i + 1)}) //nolint:errcheck
		s.SetState(ctx, int64(i+1), state) //nolint:errcheck
	}
	n, err := s.RecoverInterrupted(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("recovered %d, want 3", n)
	}
	counts, _ := s.CountByState(ctx)
	if counts[StateQueued] != 1 || counts[StateDownloaded] != 1 || counts[StateTagged] != 1 {
		t.Fatalf("unexpected counts after recover: %v", counts)
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
	s.Upsert(ctx, Discovered{SngID: 5, Title: "B"}) //nolint:errcheck
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
	s.Upsert(ctx, Discovered{SngID: 1}) //nolint:errcheck
	s.ClaimDownload(ctx) //nolint:errcheck
	s.MarkDownloaded(ctx, 1, "FLAC", "path.flac") //nolint:errcheck
	s.MarkTagged(ctx, 1) //nolint:errcheck
	s.MarkConverted(ctx, 1) //nolint:errcheck

	n, err := s.Requeue(ctx, []int64{1}, false)
	if err != nil || n != 1 {
		t.Fatalf("requeue n=%d err=%v", n, err)
	}
	it, _ := s.Get(ctx, 1)
	if it.State != StateQueued || it.FilePath != "path.flac" {
		t.Fatalf("state=%q file=%q", it.State, it.FilePath)
	}

	s.MarkConverted(ctx, 1) //nolint:errcheck
	s.Requeue(ctx, []int64{1}, true) //nolint:errcheck
	it, _ = s.Get(ctx, 1)
	if it.State != StateQueued || it.FilePath != "" {
		t.Fatalf("force-all state=%q file=%q", it.State, it.FilePath)
	}
}

func TestBatchRetry(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)

	for _, id := range []int64{1, 2, 3} {
		s.Upsert(ctx, Discovered{SngID: id}) //nolint:errcheck
		s.ClaimDownload(ctx) //nolint:errcheck
	}
	s.MarkDownloaded(ctx, 3, "FLAC", "p.flac") //nolint:errcheck
	s.MarkTagged(ctx, 3) //nolint:errcheck
	s.MarkConverted(ctx, 3) //nolint:errcheck

	if err := s.MarkInFailedBatch(ctx, []int64{1, 2}, "timeout"); err != nil {
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
		if it.State != StateFailedDownload {
			t.Errorf("item %d: State=%q want failed_download", it.SngID, it.State)
		}
	}

	if err := s.RequeueFailedBatch(ctx, []int64{1, 2}); err != nil {
		t.Fatal(err)
	}
	it1, _ := s.Get(ctx, 1)
	if it1.State != StateQueued || it1.InFailedBatch || it1.BatchAttempts != 1 {
		t.Fatalf("after requeue: state=%q in_failed=%v batch_attempts=%d",
			it1.State, it1.InFailedBatch, it1.BatchAttempts)
	}

	s.MarkFailedDownload(ctx, 1, "still failing") //nolint:errcheck
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

func TestRequeueForRetag(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)

	for _, id := range []int64{1, 2, 3} {
		s.Upsert(ctx, Discovered{SngID: id}) //nolint:errcheck
		s.ClaimDownload(ctx) //nolint:errcheck
		s.MarkDownloaded(ctx, id, "FLAC", "f") //nolint:errcheck
	}
	s.MarkTagged(ctx, 1) //nolint:errcheck
	s.MarkConverted(ctx, 2) //nolint:errcheck
	s.MarkFailedTag(ctx, 3, "oops") //nolint:errcheck

	n, err := s.RequeueForRetag(ctx)
	if err != nil || n != 3 {
		t.Fatalf("RequeueForRetag n=%d err=%v", n, err)
	}
	counts, _ := s.CountByState(ctx)
	if counts[StateDownloaded] != 3 {
		t.Fatalf("all should be downloaded after retag requeue: %v", counts)
	}
}

func TestAlbumCache(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)

	data := `{"ALB_ID":"502723","ALB_TITLE":"Plastic Beach"}`
	if err := s.UpsertAlbumCache(ctx, 502723, data); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetAlbumCache(ctx, 502723)
	if err != nil || got != data {
		t.Fatalf("GetAlbumCache: got=%q err=%v", got, err)
	}

	// Upsert again with updated data.
	data2 := `{"ALB_ID":"502723","ALB_TITLE":"Plastic Beach Updated"}`
	if err := s.UpsertAlbumCache(ctx, 502723, data2); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetAlbumCache(ctx, 502723)
	if got != data2 {
		t.Fatalf("expected updated data, got=%q", got)
	}

	// Missing album returns empty string.
	empty, err := s.GetAlbumCache(ctx, 999)
	if err != nil || empty != "" {
		t.Fatalf("expected empty for missing album, got=%q err=%v", empty, err)
	}
}
