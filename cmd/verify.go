package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"deebeets/internal/store"
)

func verifyCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "verify",
		Short: "Report finished items whose files are missing from disk",
		Long: "Reads the database (the source of truth) and checks whether each " +
			"finished item's file still exists. It never modifies the database or " +
			"deletes anything. Use `redownload --missing` to restore reported files.",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			s, err := store.Open(cfg.Paths.DBPath)
			if err != nil {
				return err
			}
			defer s.Close()

			items, err := s.FinishedItems(ctx())
			if err != nil {
				return err
			}
			var missing []*store.Item
			for _, it := range items {
				if it.FilePath == "" {
					continue
				}
				if _, err := os.Stat(filepath.Join(cfg.Paths.MusicDir, it.FilePath)); os.IsNotExist(err) {
					missing = append(missing, it)
				}
			}
			if len(missing) == 0 {
				fmt.Printf("all %d finished item(s) present on disk\n", len(items))
				return nil
			}
			fmt.Printf("%d of %d finished item(s) missing from disk:\n", len(missing), len(items))
			tw := tabwriter.NewWriter(cmdOut, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "SNG_ID\tARTIST\tTITLE\tPATH")
			for _, it := range missing {
				fmt.Fprintf(tw, "%d\t%s\t%s\t%s\n", it.SngID, trunc(it.Artist, 24), trunc(it.Title, 32), it.FilePath)
			}
			tw.Flush()
			fmt.Println("\nrun `deebeets redownload --missing` to restore them")
			return nil
		},
	}
}
