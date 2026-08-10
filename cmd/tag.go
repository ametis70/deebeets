package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

func tagCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "tag",
		Short: "Manage the tagging stage",
	}
	c.AddCommand(tagStartCmd(), tagStopCmd())
	return c
}

func tagStartCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "start",
		Short: "Trigger a tag run over all downloaded files",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := requireClient()
			if err != nil {
				return err
			}
			if err := client.TagStart(ctx()); err != nil {
				return err
			}
			fmt.Println("tag started")
			return nil
		},
	}
}

func tagStopCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stop",
		Short: "Abort the active tag run",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := requireClient()
			if err != nil {
				return err
			}
			if err := client.TagStop(ctx()); err != nil {
				return err
			}
			fmt.Println("tag stopped")
			return nil
		},
	}
}

func retagCmd() *cobra.Command {
	var all, failed bool
	c := &cobra.Command{
		Use:   "retag",
		Short: "Force re-tagging of files from cached metadata",
		Long: "Re-tags files using metadata cached in the DB — no Deezer API calls needed.\n" +
			"Two modes (choose exactly one):\n" +
			"  --all     retag all tagged/converted files\n" +
			"  --failed  retry files stuck at state=failed_tag",
		RunE: func(cmd *cobra.Command, args []string) error {
			if all == failed {
				return fmt.Errorf("choose exactly one of --all or --failed")
			}
			client, err := requireClient()
			if err != nil {
				return err
			}
			mode := "all"
			if failed {
				mode = "failed"
			}
			n, err := client.Retag(ctx(), mode)
			if err != nil {
				return err
			}
			fmt.Printf("queued %d item(s) for retagging (%s)\n", n, mode)
			return nil
		},
	}
	c.Flags().BoolVar(&all, "all", false, "retag all tagged/converted files")
	c.Flags().BoolVar(&failed, "failed", false, "retry files stuck at failed_tag state")
	return c
}
