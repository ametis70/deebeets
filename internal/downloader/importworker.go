package downloader

import (
	"context"
	"path/filepath"
	"time"

	"deebeets/internal/store"
)

// StartImport launches the import stage: a pool of workers (Beets.Concurrency,
// default 1) that drain downloaded releases through beets + post-hooks. It runs
// for the daemon's lifetime, independent of the download stage. A no-op when no
// import work is configured.
func (p *Pipeline) StartImport(ctx context.Context) {
	if !p.importActive {
		return
	}
	n := p.cfg.Beets.Concurrency
	if n < 1 {
		n = 1
	}
	for i := 0; i < n; i++ {
		p.importWG.Add(1)
		go func() {
			defer p.importWG.Done()
			p.importLoop(ctx)
		}()
	}
}

// WaitImport blocks until all import workers have exited (after ctx cancel).
func (p *Pipeline) WaitImport() { p.importWG.Wait() }

func (p *Pipeline) importLoop(ctx context.Context) {
	const poll = 3 * time.Second
	for {
		if ctx.Err() != nil {
			return
		}
		items, ok, err := p.store.ClaimImportGroup(ctx)
		if err != nil {
			p.log.Error("claim import group", "err", err)
			if !sleepCtx(ctx, poll) {
				return
			}
			continue
		}
		if !ok {
			if !sleepCtx(ctx, poll) {
				return
			}
			continue
		}
		p.importGroup(ctx, items)
	}
}

// importGroup runs the importer over one release's downloaded files.
func (p *Pipeline) importGroup(ctx context.Context, items []*store.Item) {
	ids := make([]int64, 0, len(items))
	files := make([]string, 0, len(items))
	for _, it := range items {
		ids = append(ids, it.SngID)
		if it.FilePath != "" {
			files = append(files, filepath.Join(p.musicDir, it.FilePath))
		}
	}
	if len(files) == 0 {
		_ = p.store.SetStateMany(ctx, ids, store.StateFinished)
		return
	}
	albumDir := filepath.Dir(files[0])

	if err := p.importer.Import(ctx, albumDir, files); err != nil {
		p.log.Error("import failed", "album_dir", albumDir, "err", err)
		_ = p.store.MarkFailedMany(ctx, ids, store.StageImport, err.Error())
		return
	}
	p.log.Info("imported", "album_dir", albumDir, "tracks", len(ids))
	_ = p.store.SetStateMany(ctx, ids, store.StateFinished)
}
