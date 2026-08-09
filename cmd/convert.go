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
	c.AddCommand(convertStartCmd())
	return c
}

func convertStartCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "start",
		Short: "Trigger a convert run over all downloaded files",
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
