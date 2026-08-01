// Package beets runs the optional post-download import step: `beet import` over
// a downloaded release followed by any configured post-hook commands. It
// implements downloader.Importer.
package beets

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"

	"deebeets/internal/config"
)

// Runner imports releases via beets and runs post-hooks.
type Runner struct {
	cfg       config.Beets
	postHooks []string
	log       *slog.Logger
}

// NewRunner builds a Runner from config.
func NewRunner(cfg config.Beets, postHooks []string, log *slog.Logger) *Runner {
	return &Runner{cfg: cfg, postHooks: postHooks, log: log}
}

// Import runs beets (if enabled) on albumDir, then any post-hooks. It satisfies
// downloader.Importer.
func (r *Runner) Import(ctx context.Context, albumDir string, files []string) error {
	if r.cfg.Enabled {
		if err := r.runBeets(ctx, albumDir); err != nil {
			return err
		}
	}
	return r.runHooks(ctx, albumDir, files)
}

// runBeets invokes `beet [-c config] <args...> <albumDir>`. The global -c option
// must precede the import subcommand, so it is prepended before cfg.Args.
func (r *Runner) runBeets(ctx context.Context, albumDir string) error {
	argv := make([]string, 0, len(r.cfg.Args)+3)
	if r.cfg.ConfigPath != "" {
		argv = append(argv, "-c", r.cfg.ConfigPath)
	}
	argv = append(argv, r.cfg.Args...)
	argv = append(argv, albumDir)

	cmd := exec.CommandContext(ctx, r.cfg.Binary, argv...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("beets import failed: %w: %s", err, strings.TrimSpace(string(out)))
	}
	r.log.Debug("beets import ok", "album_dir", albumDir, "output", strings.TrimSpace(string(out)))
	return nil
}

// runHooks executes each configured post-hook via the shell, exporting the
// album directory and file list so hooks can act on the import.
func (r *Runner) runHooks(ctx context.Context, albumDir string, files []string) error {
	for _, hook := range r.postHooks {
		hook = strings.TrimSpace(hook)
		if hook == "" {
			continue
		}
		cmd := exec.CommandContext(ctx, "sh", "-c", hook)
		cmd.Env = append(os.Environ(),
			"DEEBEETS_ALBUM_DIR="+albumDir,
			"DEEBEETS_FILES="+strings.Join(files, "\n"),
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("post-hook %q failed: %w: %s", hook, err, strings.TrimSpace(string(out)))
		}
		r.log.Debug("post-hook ok", "hook", hook)
	}
	return nil
}
