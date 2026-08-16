package cmd

import (
	"fmt"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
)

func blocklistCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "blocklist",
		Short: "Manage blocklisted ids (never downloaded)",
	}
	c.AddCommand(blocklistAddCmd(), blocklistRemoveCmd(), blocklistListCmd())
	return c
}

func blocklistAddCmd() *cobra.Command {
	var kind, reason string
	c := &cobra.Command{
		Use:   "add <id> [<id>...]",
		Short: "Blocklist ids of a kind",
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
			if err := client.BlocklistAdd(ctx(), kind, ids, reason); err != nil {
				return err
			}
			fmt.Printf("blocklisted %d %s id(s)\n", len(ids), kind)
			return nil
		},
	}
	c.Flags().StringVar(&kind, "type", "track", "id type: track|album|artist|playlist")
	c.Flags().StringVar(&reason, "reason", "", "optional reason")
	return c
}

func blocklistRemoveCmd() *cobra.Command {
	var kind string
	c := &cobra.Command{
		Use:   "remove <id> [<id>...]",
		Short: "Remove ids from the blocklist",
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
			if err := client.BlocklistRemove(ctx(), kind, ids); err != nil {
				return err
			}
			fmt.Printf("removed %d %s id(s) from the blocklist\n", len(ids), kind)
			return nil
		},
	}
	c.Flags().StringVar(&kind, "type", "track", "id type: track|album|artist|playlist")
	return c
}

func blocklistListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List blocklist entries",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := requireClient()
			if err != nil {
				return err
			}
			blocks, err := client.BlocklistList(ctx())
			if err != nil {
				return err
			}
			if len(blocks) == 0 {
				fmt.Println("blocklist is empty")
				return nil
			}
			tw := tabwriter.NewWriter(cmdOut, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "KIND\tID\tREASON\tADDED") //nolint:errcheck
			for _, b := range blocks {
				_, _ = fmt.Fprintf(tw, "%s\t%d\t%s\t%s\n", b.Kind, b.ExtID, b.Reason,
					time.Unix(b.CreatedAt, 0).Format("2006-01-02"))
			}
			tw.Flush() //nolint:errcheck
			return nil
		},
	}
}
