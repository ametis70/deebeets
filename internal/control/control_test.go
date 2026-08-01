package control

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"deebeets/internal/store"
)

// fakeController records calls and returns canned data.
type fakeController struct {
	started      bool
	stopped      bool
	lastKind     string
	lastIDs      []int64
	lastMode     string
	blocked      []store.Block
	syncSel      Selection
	importedPath string
}

func (f *fakeController) Status(context.Context) (StatusResponse, error) {
	return StatusResponse{DownloadRunning: true, DownloadStatus: "running",
		Counts: map[string]int{"finished": 3, "queued": 2}}, nil
}
func (f *fakeController) Sync(_ context.Context, sel Selection) (SyncStarted, error) {
	f.syncSel = sel
	return SyncStarted{Started: true, Message: "sync started"}, nil
}
func (f *fakeController) Download(_ context.Context, kind string, ids []int64) (int, error) {
	f.lastKind, f.lastIDs = kind, ids
	return len(ids), nil
}
func (f *fakeController) Redownload(_ context.Context, mode string, ids []int64) (int, error) {
	f.lastMode, f.lastIDs = mode, ids
	return 7, nil
}
func (f *fakeController) StartDownload(context.Context) error { f.started = true; return nil }
func (f *fakeController) StopDownload(context.Context) error  { f.stopped = true; return nil }
func (f *fakeController) BlocklistAdd(_ context.Context, kind string, ids []int64, reason string) error {
	f.blocked = append(f.blocked, store.Block{Kind: kind, ExtID: ids[0], Reason: reason})
	return nil
}
func (f *fakeController) BlocklistRemove(context.Context, string, []int64) error { return nil }
func (f *fakeController) BlocklistList(context.Context) ([]store.Block, error)   { return f.blocked, nil }
func (f *fakeController) BeetsImport(_ context.Context, path string) error {
	f.importedPath = path
	return nil
}
func (f *fakeController) Items(context.Context, []string, int) ([]store.Item, error) {
	return []store.Item{{SngID: 1, State: "finished", Title: "T"}}, nil
}

func startServer(t *testing.T) (*Client, *fakeController) {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "d.sock")
	fc := &fakeController{}
	srv := NewServer(fc, sock)
	if err := srv.Listen(); err != nil {
		t.Fatal(err)
	}
	go srv.Serve()
	t.Cleanup(func() { srv.Close(context.Background()) })

	c := NewClient(sock)
	// Wait for the socket to accept connections.
	for i := 0; i < 50 && !c.Available(); i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if !c.Available() {
		t.Fatal("server socket never became available")
	}
	return c, fc
}

func TestControlRoundTrip(t *testing.T) {
	ctx := context.Background()
	c, fc := startServer(t)

	st, err := c.Status(ctx)
	if err != nil || !st.DownloadRunning || st.Counts["finished"] != 3 {
		t.Fatalf("status = %+v err=%v", st, err)
	}

	if s, err := c.Sync(ctx, Selection{Tracks: true}); err != nil || !s.Started {
		t.Fatalf("sync = %+v err=%v", s, err)
	}
	if !fc.syncSel.Tracks {
		t.Fatal("server did not receive selection")
	}

	if n, err := c.Download(ctx, "album", []int64{10, 11}); err != nil || n != 2 {
		t.Fatalf("download n=%d err=%v", n, err)
	}
	if fc.lastKind != "album" || len(fc.lastIDs) != 2 {
		t.Fatalf("download not forwarded: %s %v", fc.lastKind, fc.lastIDs)
	}

	if n, err := c.Redownload(ctx, "missing", nil); err != nil || n != 7 {
		t.Fatalf("redownload n=%d err=%v", n, err)
	}
	if fc.lastMode != "missing" {
		t.Fatalf("redownload mode = %s", fc.lastMode)
	}

	if err := c.Start(ctx); err != nil || !fc.started {
		t.Fatalf("start err=%v started=%v", err, fc.started)
	}
	if err := c.Stop(ctx); err != nil || !fc.stopped {
		t.Fatalf("stop err=%v stopped=%v", err, fc.stopped)
	}

	if err := c.BlocklistAdd(ctx, "track", []int64{99}, "nope"); err != nil {
		t.Fatal(err)
	}
	blocks, err := c.BlocklistList(ctx)
	if err != nil || len(blocks) != 1 || blocks[0].ExtID != 99 {
		t.Fatalf("blocklist = %+v err=%v", blocks, err)
	}

	if err := c.BeetsImport(ctx, "/music/x"); err != nil || fc.importedPath != "/music/x" {
		t.Fatalf("beets import err=%v path=%q", err, fc.importedPath)
	}

	items, err := c.Items(ctx, []string{"finished"})
	if err != nil || len(items) != 1 || items[0].SngID != 1 {
		t.Fatalf("items = %+v err=%v", items, err)
	}
}
