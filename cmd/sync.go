package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"deebeets/internal/control"
)

func syncCmd() *cobra.Command {
	var albums, artists, playlists, tracks bool
	c := &cobra.Command{
		Use:   "sync",
		Short: "Pull favorites from Deezer into the queue (does not download)",
		Long: "Pulls the selected favorite types and enqueues them. With no flags " +
			"the configured default favorite types are used. Downloading is " +
			"controlled separately with `start`/`stop`.",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := requireClient()
			if err != nil {
				return err
			}
			sel := control.Selection{Albums: albums, Artists: artists, Playlists: playlists, Tracks: tracks}
			res, err := client.Sync(ctx(), sel)
			if err != nil {
				return err
			}
			if res.Started {
				fmt.Println("sync started in the background; check `deebeets status`")
			} else {
				fmt.Println(res.Message)
			}
			return nil
		},
	}
	c.Flags().BoolVar(&albums, "albums", false, "sync favorite albums")
	c.Flags().BoolVar(&artists, "artists", false, "sync favorite artists")
	c.Flags().BoolVar(&playlists, "playlists", false, "sync favorite playlists")
	c.Flags().BoolVar(&tracks, "tracks", false, "sync loved tracks")
	return c
}

func startCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "start",
		Short: "Start draining the download queue",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := requireClient()
			if err != nil {
				return err
			}
			if err := client.Start(ctx()); err != nil {
				return err
			}
			fmt.Println("download stage started")
			return nil
		},
	}
}

func stopCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stop",
		Short: "Stop draining the download queue (finishes in-flight, then pauses)",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := requireClient()
			if err != nil {
				return err
			}
			if err := client.Stop(ctx()); err != nil {
				return err
			}
			fmt.Println("download stage stopped")
			return nil
		},
	}
}
