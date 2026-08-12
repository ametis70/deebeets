// Package converter runs ffmpeg on downloaded audio files to produce converted
// copies with full multi-value tags.
package converter

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"deeznt/internal/config"
	"deeznt/internal/tagger"
)

// ConvertJob is one file to convert, with the metadata to write to the output.
type ConvertJob struct {
	SngID      int64
	SourcePath string
	Metadata   tagger.Metadata
	// CopyTagsFromSource instructs the converter to read tags from the source
	// FLAC file rather than using Metadata. Used by RunConvert which doesn't
	// have metadata in memory.
	CopyTagsFromSource bool
}

// Runner converts audio files using ffmpeg and writes proper multi-value tags.
type Runner struct {
	cfg    config.Convert
	srcDir string // source music_dir for deriving relative paths
	log    *slog.Logger
}

// New creates a Runner.
func New(cfg config.Convert, srcDir string, log *slog.Logger) *Runner {
	return &Runner{cfg: cfg, srcDir: srcDir, log: log}
}

// DestDir returns the effective destination directory.
func (r *Runner) DestDir() string {
	if r.cfg.Dest != "" {
		return r.cfg.Dest
	}
	return r.srcDir
}

// RunAll converts all jobs concurrently (up to cfg.Concurrency).
// Returns the SngIDs of successfully converted files and a map of
// failed SngID → error message.
func (r *Runner) RunAll(ctx context.Context, jobs []ConvertJob) (converted []int64, failed map[int64]string) {
	if len(jobs) == 0 {
		return nil, nil
	}
	failed = make(map[int64]string)

	sem := make(chan struct{}, r.cfg.Concurrency)
	var (
		mu sync.Mutex
		wg sync.WaitGroup
	)

	for _, j := range jobs {
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		j := j
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			err := r.convertOne(ctx, j)
			mu.Lock()
			if err != nil {
				failed[j.SngID] = err.Error()
			} else {
				converted = append(converted, j.SngID)
			}
			mu.Unlock()
		}()
	}
	wg.Wait()
	return converted, failed
}

// convertOne converts and tags a single file.
func (r *Runner) convertOne(ctx context.Context, job ConvertJob) error {
	outPath, err := r.outputPath(job.SourcePath)
	if err != nil {
		return err
	}

	if _, err := os.Stat(outPath); err == nil {
		r.log.Debug("convert: already exists, skipping", "path", outPath)
		return nil
	}

	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return fmt.Errorf("convert mkdir: %w", err)
	}

	if err := r.runFFmpeg(ctx, job.SourcePath, outPath); err != nil {
		_ = os.Remove(outPath)
		return fmt.Errorf("convert %s: %w", filepath.Base(job.SourcePath), err)
	}

	// Write full multi-value tags to the converted file.
	if job.CopyTagsFromSource {
		// Read tags from source FLAC and copy them to the opus file.
		if err := tagger.CopyTagsFromFLAC(job.SourcePath, outPath); err != nil {
			r.log.Warn("convert: tag copy failed", "path", outPath, "err", err)
		}
	} else if err := tagger.Write(outPath, r.outputFormat(), job.Metadata, tagger.DefaultFieldSet()); err != nil {
		r.log.Warn("convert: tagging failed", "path", outPath, "err", err)
	}

	// Copy cover to dest album dir if not already present.
	if len(job.Metadata.Cover) > 0 {
		_ = tagger.WriteCoverFile(filepath.Dir(outPath), job.Metadata.Cover)
	}

	r.log.Info("converted", "src", job.SourcePath, "dest", outPath)
	return nil
}

func (r *Runner) runFFmpeg(ctx context.Context, src, dst string) error {
	// Split the template first, then substitute $source/$dest as whole tokens.
	// This avoids path splitting on spaces inside directory names.
	argv, err := splitArgs(r.cfg.FFmpegArgs)
	if err != nil {
		return fmt.Errorf("parse ffmpeg_args: %w", err)
	}
	if len(argv) == 0 {
		return fmt.Errorf("ffmpeg_args is empty")
	}
	for i, arg := range argv {
		arg = strings.ReplaceAll(arg, "$source", src)
		arg = strings.ReplaceAll(arg, "$dest", dst)
		argv[i] = arg
	}

	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// outputFormat returns the tagger format string for the configured output.
func (r *Runner) outputFormat() string {
	switch strings.ToLower(r.cfg.Format) {
	case "opus":
		return "OGG_OPUS"
	case "mp3":
		return "MP3_320"
	case "flac":
		return "FLAC"
	default:
		return "OGG_OPUS"
	}
}

// OutputPath returns the expected output path for a given source file.
// Returns empty string if the source is not under srcDir.
func (r *Runner) OutputPath(srcPath string) (string, error) {
	return r.outputPath(srcPath)
}

// outputPath derives the destination path for a source file.
func (r *Runner) outputPath(srcPath string) (string, error) {
	rel, err := filepath.Rel(r.srcDir, srcPath)
	if err != nil {
		rel = filepath.Base(srcPath)
	}
	ext := "." + tagger.ExtForOutputFormat(r.cfg.Format)
	noExt := strings.TrimSuffix(rel, filepath.Ext(rel))
	return filepath.Join(r.DestDir(), noExt+ext), nil
}

// splitArgs splits a shell-like command string into argv, handling double-quoted segments.
func splitArgs(s string) ([]string, error) {
	var args []string
	var cur strings.Builder
	inQuote := false

	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '"' && !inQuote:
			inQuote = true
		case c == '"' && inQuote:
			inQuote = false
		case c == ' ' && !inQuote:
			if cur.Len() > 0 {
				args = append(args, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteByte(c)
		}
	}
	if inQuote {
		return nil, fmt.Errorf("unterminated quote in ffmpeg_args")
	}
	if cur.Len() > 0 {
		args = append(args, cur.String())
	}
	return args, nil
}
