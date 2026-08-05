package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

// downloadCmd returns the `download` parent command with start/stop subcommands.
func downloadCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "download",
		Short: "Manage the download stage",
	}
	c.AddCommand(downloadStartCmd(), downloadStopCmd())
	return c
}

func downloadStartCmd() *cobra.Command {
	var kind string
	c := &cobra.Command{
		Use:   "start [<id>...]",
		Short: "Start downloading (optionally enqueue specific ids first)",
		Long: "Triggers the download run immediately, interrupting the poll timer. " +
			"If ids are supplied they are enqueued before the run starts.",
		RunE: func(cmd *cobra.Command, args []string) error {
			var ids []int64
			if len(args) > 0 {
				var err error
				ids, err = parseIDs(args)
				if err != nil {
					return err
				}
				switch kind {
				case "track", "album", "artist", "playlist":
				default:
					return fmt.Errorf("--type must be track|album|artist|playlist")
				}
			}
			client, err := requireClient()
			if err != nil {
				return err
			}
			if err := client.DownloadStart(ctx(), kind, ids); err != nil {
				return err
			}
			fmt.Println("download started")
			return nil
		},
	}
	c.Flags().StringVar(&kind, "type", "track", "id type when enqueuing: track|album|artist|playlist")
	return c
}

func downloadStopCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stop",
		Short: "Abort the active download run (partial files deleted, items requeued)",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := requireClient()
			if err != nil {
				return err
			}
			if err := client.DownloadStop(ctx()); err != nil {
				return err
			}
			fmt.Println("download stop signal sent")
			return nil
		},
	}
}

func redownloadCmd() *cobra.Command {
	var all, missing, failed bool
	c := &cobra.Command{
		Use:   "redownload [<id>...]",
		Short: "Force re-download of items",
		Long: "Three modes (choose exactly one):\n" +
			"  --all      re-download regardless of state (optionally limited to ids)\n" +
			"  --missing  re-download only finished items whose file is gone from disk\n" +
			"  --failed   requeue all permanently-failed items and start downloading",
		RunE: func(cmd *cobra.Command, args []string) error {
			n := 0
			for _, b := range []bool{all, missing, failed} {
				if b {
					n++
				}
			}
			if n != 1 {
				return fmt.Errorf("choose exactly one of --all, --missing, or --failed")
			}
			client, err := requireClient()
			if err != nil {
				return err
			}

			mode := "all"
			var ids []int64
			switch {
			case missing:
				mode = "missing"
			case failed:
				mode = "failed"
			default:
				ids, err = parseIDs(args)
				if err != nil {
					return err
				}
			}
			count, err := client.Redownload(ctx(), mode, ids)
			if err != nil {
				return err
			}
			fmt.Printf("requeued %d item(s) for re-download (%s)\n", count, mode)
			return nil
		},
	}
	c.Flags().BoolVar(&all, "all", false, "re-download everything (or the given ids)")
	c.Flags().BoolVar(&missing, "missing", false, "re-download only files missing from disk")
	c.Flags().BoolVar(&failed, "failed", false, "requeue all permanently-failed items")
	return c
}
