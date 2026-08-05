package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

func beetsCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "beets",
		Short: "Beets integration",
	}
	c.AddCommand(beetsImportCmd())
	return c
}

func beetsImportCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "import",
		Short: "Trigger a full-library beets import on demand",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := requireClient()
			if err != nil {
				return err
			}
			if err := client.BeetsImport(ctx()); err != nil {
				return err
			}
			fmt.Println("import triggered")
			return nil
		},
	}
}
