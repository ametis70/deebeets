package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"deeznt/internal/control"
)

// syncCmd returns the `sync` parent command with start/stop subcommands.
func syncCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "sync",
		Short: "Manage the favorites sync",
	}
	c.AddCommand(syncStartCmd(), syncStopCmd())
	return c
}

func syncStartCmd() *cobra.Command {
	var albums, artists, playlists, tracks bool
	c := &cobra.Command{
		Use:   "start",
		Short: "Trigger an immediate favorites sync",
		Long: "Pulls the selected favorite types and enqueues them. With no flags " +
			"the configured default favorite types are used. Cannot run while a " +
			"download or import is active.",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := requireClient()
			if err != nil {
				return err
			}
			sel := control.Selection{Albums: albums, Artists: artists, Playlists: playlists, Tracks: tracks}
			if err := client.SyncStart(ctx(), sel); err != nil {
				return err
			}
			fmt.Println("sync started")
			return nil
		},
	}
	c.Flags().BoolVar(&albums, "albums", false, "sync favorite albums")
	c.Flags().BoolVar(&artists, "artists", false, "sync favorite artists")
	c.Flags().BoolVar(&playlists, "playlists", false, "sync favorite playlists")
	c.Flags().BoolVar(&tracks, "tracks", false, "sync loved tracks")
	return c
}

func syncStopCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stop",
		Short: "Cancel an in-progress sync",
		Long:  "Cannot stop sync while a download or import is active.",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := requireClient()
			if err != nil {
				return err
			}
			if err := client.SyncStop(ctx()); err != nil {
				return err
			}
			fmt.Println("sync stopped")
			return nil
		},
	}
}
