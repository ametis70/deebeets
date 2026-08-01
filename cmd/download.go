package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

func downloadCmd() *cobra.Command {
	var kind string
	var forceAll bool
	c := &cobra.Command{
		Use:   "download <id> [<id>...]",
		Short: "Enqueue specific Deezer ids for downloading",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ids, err := parseIDs(args)
			if err != nil {
				return err
			}
			client, err := requireClient()
			if err != nil {
				return err
			}
			switch kind {
			case "track", "album", "artist", "playlist":
			default:
				return fmt.Errorf("--type must be track|album|artist|playlist")
			}

			n, err := client.Download(ctx(), kind, ids)
			if err != nil {
				return err
			}
			fmt.Printf("queued %d track(s)\n", n)

			if forceAll {
				m, err := client.Redownload(ctx(), "all", ids)
				if err != nil {
					return err
				}
				fmt.Printf("force-all requeued %d existing track(s)\n", m)
			}
			return nil
		},
	}
	c.Flags().StringVar(&kind, "type", "track", "id type: track|album|artist|playlist")
	c.Flags().BoolVar(&forceAll, "force-all", false, "also requeue matching ids already downloaded")
	return c
}

func redownloadCmd() *cobra.Command {
	var all, missing bool
	c := &cobra.Command{
		Use:   "redownload [<id>...]",
		Short: "Force re-download of items",
		Long: "Two distinct modes:\n" +
			"  --all      re-download regardless of state (optionally limited to ids), " +
			"overwriting files. For quality upgrades or corruption.\n" +
			"  --missing  re-download only finished items whose file is gone from disk. " +
			"For restoring deleted files.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if all == missing {
				return fmt.Errorf("choose exactly one of --all or --missing")
			}
			client, err := requireClient()
			if err != nil {
				return err
			}
			mode := "all"
			var ids []int64
			if missing {
				mode = "missing"
			} else {
				ids, err = parseIDs(args)
				if err != nil {
					return err
				}
			}
			n, err := client.Redownload(ctx(), mode, ids)
			if err != nil {
				return err
			}
			fmt.Printf("requeued %d item(s) for re-download (%s)\n", n, mode)
			return nil
		},
	}
	c.Flags().BoolVar(&all, "all", false, "re-download everything (or the given ids)")
	c.Flags().BoolVar(&missing, "missing", false, "re-download only files missing from disk")
	return c
}
