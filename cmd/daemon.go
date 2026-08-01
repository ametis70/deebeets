package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"deebeets/internal/beets"
	"deebeets/internal/credentials"
	"deebeets/internal/daemon"
	"deebeets/internal/deezer"
	"deebeets/internal/downloader"
	"deebeets/internal/store"
)

// EnvFixtureAlbums holds a comma-separated list of Deezer album IDs. When set,
// `deebeets daemon` runs a one-shot pipeline over those albums and exits instead
// of starting the long-lived daemon. Intended for end-to-end testing.
const EnvFixtureAlbums = "DEEBEETS_FIXTURE_ALBUMS"

func daemonCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "daemon",
		Short: "Run the long-lived downloader daemon and control socket",
		RunE: func(cmd *cobra.Command, args []string) error {
			if raw := os.Getenv(EnvFixtureAlbums); raw != "" {
				return runFixture(raw)
			}
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			log := newLogger()
			d, err := daemon.New(cfg, log)
			if err != nil {
				return err
			}
			return d.Run(context.Background())
		},
	}
}

// runFixture parses a comma-separated list of album IDs and runs the full
// download pipeline on them in one shot, then exits.
func runFixture(raw string) error {
	var ids []int64
	for _, s := range strings.Split(raw, ",") {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		id, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return fmt.Errorf("%s: invalid album id %q", EnvFixtureAlbums, s)
		}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return fmt.Errorf("%s is set but contains no valid ids", EnvFixtureAlbums)
	}

	log := newLogger()
	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	st, err := store.Open(cfg.Paths.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()

	arl, err := credentials.LoadARL(context.Background(), cfg.Deezer.ARL, cfg.Paths.DBPath, st)
	if err != nil {
		return fmt.Errorf("load credentials: %w", err)
	}
	if arl == "" {
		return fmt.Errorf("no ARL configured — run `deebeets login`, set deezer.arl in config, or export DEEBEETS_ARL")
	}

	dz, err := deezer.New(arl)
	if err != nil {
		return err
	}
	runCtx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if err := dz.Login(runCtx); err != nil {
		return fmt.Errorf("deezer login: %w", err)
	}
	log.Info("fixture mode", "albums", len(ids), "concurrency", cfg.Download.Concurrency,
		"user_id", dz.UserID(), "lossless", dz.CanStreamLossless())

	imp := beets.NewRunner(cfg.Beets, cfg.PostHooks, log)
	pipe := downloader.New(st, dz, cfg, imp, log)

	queued, err := pipe.EnqueueIDs(runCtx, deezer.KindAlbum, ids)
	if err != nil {
		return fmt.Errorf("enqueue: %w", err)
	}
	fmt.Fprintf(cmdOut, "enqueued %d track(s) from %d album(s)\n", queued, len(ids))

	pipe.StartImport(runCtx)
	pipe.DrainOnce(runCtx)
	pipe.FlushImport(runCtx, cancel)

	counts, err := st.CountByState(context.Background())
	if err != nil {
		return err
	}
	fmt.Fprintf(cmdOut, "finished=%d  failed=%d  skipped=%d\n",
		counts[store.StateFinished], counts[store.StateFailed], counts[store.StateSkipped])
	return nil
}
