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
	var path string
	c := &cobra.Command{
		Use:   "import --path <dir>",
		Short: "Trigger a beets import of a path on demand",
		RunE: func(cmd *cobra.Command, args []string) error {
			if path == "" && len(args) > 0 {
				path = args[0]
			}
			if path == "" {
				return fmt.Errorf("provide a path with --path or as an argument")
			}
			client, err := requireClient()
			if err != nil {
				return err
			}
			if err := client.BeetsImport(ctx(), path); err != nil {
				return err
			}
			fmt.Println("import triggered")
			return nil
		},
	}
	c.Flags().StringVar(&path, "path", "", "directory to import")
	return c
}
