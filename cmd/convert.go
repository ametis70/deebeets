package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

func convertCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "convert",
		Short: "Manage the ffmpeg conversion stage",
	}
	c.AddCommand(convertStartCmd(), convertStopCmd())
	return c
}

func convertStartCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "start",
		Short: "Trigger a convert run over all pending/missing files",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := requireClient()
			if err != nil {
				return err
			}
			if err := client.ConvertStart(ctx()); err != nil {
				return err
			}
			fmt.Println("convert started")
			return nil
		},
	}
}

func convertStopCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stop",
		Short: "Abort the active convert run",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := requireClient()
			if err != nil {
				return err
			}
			if err := client.ConvertStop(ctx()); err != nil {
				return err
			}
			fmt.Println("convert stop signal sent")
			return nil
		},
	}
}

func reconvertCmd() *cobra.Command {
	var all, failed bool
	c := &cobra.Command{
		Use:   "reconvert",
		Short: "Force re-conversion of files",
		Long: "Two modes (choose exactly one):\n" +
			"  --all     delete existing converted files and reconvert everything\n" +
			"  --failed  retry files stuck at state=downloaded (previous conversion failed)",
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
			n, err := client.Reconvert(ctx(), mode)
			if err != nil {
				return err
			}
			fmt.Printf("queued %d item(s) for reconversion (%s)\n", n, mode)
			return nil
		},
	}
	c.Flags().BoolVar(&all, "all", false, "delete existing converted files and reconvert everything")
	c.Flags().BoolVar(&failed, "failed", false, "retry files stuck at downloaded state")
	return c
}
