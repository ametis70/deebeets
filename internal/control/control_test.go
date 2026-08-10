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
	syncStarted    bool
	syncStopped    bool
	dlStarted      bool
	dlStopped      bool
	convertStarted bool
	lastKind       string
	lastIDs        []int64
	lastMode       string
	blocked        []store.Block
	syncSel        Selection
}

func (f *fakeController) Status(context.Context) (StatusResponse, error) {
	return StatusResponse{Stage: "idle", Counts: map[string]int{"finished": 3, "queued": 2}}, nil
}
func (f *fakeController) SyncStart(_ context.Context, sel Selection) error {
	f.syncStarted, f.syncSel = true, sel
	return nil
}
func (f *fakeController) SyncStop(context.Context) error { f.syncStopped = true; return nil }
func (f *fakeController) DownloadStart(_ context.Context, kind string, ids []int64) error {
	f.dlStarted, f.lastKind, f.lastIDs = true, kind, ids
	return nil
}
func (f *fakeController) DownloadStop(context.Context) error { f.dlStopped = true; return nil }
func (f *fakeController) ConvertStart(context.Context) error {
	f.convertStarted = true
	return nil
}
func (f *fakeController) Reconvert(_ context.Context, mode string) (int, error) {
	f.lastMode = mode
	return 3, nil
}
func (f *fakeController) Redownload(_ context.Context, mode string, ids []int64) (int, error) {
	f.lastMode, f.lastIDs = mode, ids
	return 7, nil
}
func (f *fakeController) BlocklistAdd(_ context.Context, kind string, ids []int64, reason string) error {
	f.blocked = append(f.blocked, store.Block{Kind: kind, ExtID: ids[0], Reason: reason})
	return nil
}
func (f *fakeController) BlocklistRemove(context.Context, string, []int64) error { return nil }
func (f *fakeController) BlocklistList(context.Context) ([]store.Block, error)   { return f.blocked, nil }
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
	if err != nil || st.Stage != "idle" || st.Counts["finished"] != 3 {
		t.Fatalf("status = %+v err=%v", st, err)
	}

	if err := c.SyncStart(ctx, Selection{Tracks: true}); err != nil {
		t.Fatalf("sync start err=%v", err)
	}
	if !fc.syncStarted || !fc.syncSel.Tracks {
		t.Fatal("sync start not forwarded")
	}

	if err := c.SyncStop(ctx); err != nil {
		t.Fatalf("sync stop err=%v", err)
	}
	if !fc.syncStopped {
		t.Fatal("sync stop not forwarded")
	}

	if err := c.DownloadStart(ctx, "album", []int64{10, 11}); err != nil {
		t.Fatalf("download start err=%v", err)
	}
	if !fc.dlStarted || fc.lastKind != "album" || len(fc.lastIDs) != 2 {
		t.Fatalf("download start not forwarded: started=%v kind=%s ids=%v",
			fc.dlStarted, fc.lastKind, fc.lastIDs)
	}

	if err := c.DownloadStop(ctx); err != nil {
		t.Fatalf("download stop err=%v", err)
	}
	if !fc.dlStopped {
		t.Fatal("download stop not forwarded")
	}

	if err := c.ConvertStart(ctx); err != nil || !fc.convertStarted {
		t.Fatalf("convert start err=%v called=%v", err, fc.convertStarted)
	}

	if n, err := c.Redownload(ctx, "failed", nil); err != nil || n != 7 {
		t.Fatalf("redownload n=%d err=%v", n, err)
	}
	if fc.lastMode != "failed" {
		t.Fatalf("redownload mode = %s", fc.lastMode)
	}

	if err := c.BlocklistAdd(ctx, "track", []int64{99}, "nope"); err != nil {
		t.Fatal(err)
	}
	blocks, err := c.BlocklistList(ctx)
	if err != nil || len(blocks) != 1 || blocks[0].ExtID != 99 {
		t.Fatalf("blocklist = %+v err=%v", blocks, err)
	}

	items, err := c.Items(ctx, []string{"finished"})
	if err != nil || len(items) != 1 || items[0].SngID != 1 {
		t.Fatalf("items = %+v err=%v", items, err)
	}
}
