package beets

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"deebeets/internal/config"
)

func TestPostHooksRunWithEnv(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "hook.out")

	r := NewRunner(
		config.Beets{Enabled: false}, // beets off, hooks still run
		[]string{"printf '%s' \"$DEEBEETS_ALBUM_DIR\" > " + marker},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)

	albumDir := filepath.Join(dir, "Artist", "Album")
	if err := r.Import(context.Background(), albumDir, []string{albumDir + "/01.flac"}); err != nil {
		t.Fatalf("Import: %v", err)
	}
	got, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != albumDir {
		t.Fatalf("hook saw album dir %q, want %q", got, albumDir)
	}
}

func TestBeetsBinaryErrorSurfaces(t *testing.T) {
	r := NewRunner(
		config.Beets{Enabled: true, Binary: "definitely-not-a-real-binary-xyz", Args: []string{"import"}},
		nil,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	if err := r.Import(context.Background(), "/tmp/x", nil); err == nil {
		t.Fatal("expected error when beets binary is missing")
	}
}
