package downloader

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"deebeets/internal/deezer"
	"deebeets/internal/store"
)

// progressWriter forwards writes to the underlying file and periodically
// persists the resumable byte offset to the store.
type progressWriter struct {
	f        *os.File
	st       *store.Store
	sngID    int64
	tmpPath  string
	base     int64 // bytes already on disk before this run (resume offset)
	written  int64 // bytes written this run
	lastSave time.Time
}

func (w *progressWriter) Write(p []byte) (int, error) {
	n, err := w.f.Write(p)
	w.written += int64(n)
	if time.Since(w.lastSave) > 2*time.Second {
		w.lastSave = time.Now()
		// Best-effort; a failed progress save just means a slightly longer resume.
		_ = w.st.UpdateProgress(context.Background(), w.sngID,
			deezer.AlignResumeOffset(w.base+w.written), w.tmpPath)
	}
	return n, err
}

// tmpPathFor returns the incomplete-file path for a track.
func (p *Pipeline) tmpPathFor(sngID int64) string {
	return filepath.Join(p.incompleteDir, fmt.Sprintf("%d.part", sngID))
}

// streamToTemp downloads and decrypts a track into its temp file, resuming from
// a cipher-aligned boundary when a partial file already exists. It returns the
// temp file path on success.
func (p *Pipeline) streamToTemp(ctx context.Context, item *store.Item, url string) (string, error) {
	if err := os.MkdirAll(p.incompleteDir, 0o755); err != nil {
		return "", err
	}
	tmp := item.TmpPath
	if tmp == "" {
		tmp = p.tmpPathFor(item.SngID)
	}

	// Determine a cipher-aligned resume offset from any existing partial file.
	var resumeAt int64
	if fi, err := os.Stat(tmp); err == nil {
		resumeAt = deezer.AlignResumeOffset(fi.Size())
	}

	resp, err := p.dz.Download(ctx, url, resumeAt)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	// If we asked to resume but the server ignored the Range header, restart.
	if resumeAt > 0 && resp.StatusCode != http.StatusPartialContent {
		resumeAt = 0
	}

	flags := os.O_CREATE | os.O_WRONLY
	f, err := os.OpenFile(tmp, flags, 0o644)
	if err != nil {
		return "", err
	}
	// Truncate to the aligned boundary and seek there so we append cleanly.
	if err := f.Truncate(resumeAt); err != nil {
		f.Close()
		return "", err
	}
	if _, err := f.Seek(resumeAt, 0); err != nil {
		f.Close()
		return "", err
	}

	pw := &progressWriter{f: f, st: p.store, sngID: item.SngID, tmpPath: tmp, base: resumeAt}
	_, derr := deezer.DecryptTrack(pw, resp.Body, item.SngID)
	if cerr := f.Close(); cerr != nil && derr == nil {
		derr = cerr
	}
	if derr != nil {
		return "", derr
	}

	// Persist final offset for observability/resume bookkeeping.
	_ = p.store.UpdateProgress(ctx, item.SngID, deezer.AlignResumeOffset(resumeAt+pw.written), tmp)
	return tmp, nil
}
