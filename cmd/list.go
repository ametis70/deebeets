package cmd

import (
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"deebeets/internal/store"
)

func listCmd() *cobra.Command {
	var states []string
	var limit int
	c := &cobra.Command{
		Use:   "list",
		Short: "List queue items, optionally filtered by state",
		Long: "States: waiting, queued, downloading, downloaded, importing, " +
			"finished, failed, blocklisted, skipped.",
		RunE: func(cmd *cobra.Command, args []string) error {
			items, err := fetchItems(states, limit)
			if err != nil {
				return err
			}
			if len(items) == 0 {
				fmt.Println("no items")
				return nil
			}
			tw := tabwriter.NewWriter(cmdOut, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "SNG_ID\tSTATE\tARTIST\tTITLE\tFORMAT\tERROR")
			for _, it := range items {
				fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%s\n",
					it.SngID, it.State, trunc(it.Artist, 24), trunc(it.Title, 32),
					it.Format, trunc(it.Error, 40))
			}
			tw.Flush()
			return nil
		},
	}
	c.Flags().StringSliceVar(&states, "state", nil, "filter by state(s), comma-separated")
	c.Flags().IntVar(&limit, "limit", 0, "max rows (0 = all)")
	return c
}

// fetchItems uses the daemon when available, else reads the DB directly.
func fetchItems(states []string, limit int) ([]store.Item, error) {
	client, err := newClient()
	if err != nil {
		return nil, err
	}
	if client.Available() {
		return client.Items(ctx(), states)
	}
	cfg, err := loadConfig()
	if err != nil {
		return nil, err
	}
	s, err := store.Open(cfg.Paths.DBPath)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	ptrs, err := s.List(ctx(), states, limit)
	if err != nil {
		return nil, err
	}
	out := make([]store.Item, 0, len(ptrs))
	for _, p := range ptrs {
		out = append(out, *p)
	}
	return out, nil
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:n]
	}
	return s[:n-1] + "…"
}
