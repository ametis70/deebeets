package cmd

import (
	"fmt"
	"sort"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"deebeets/internal/store"
)

// stateOrder is the display order for state counts.
var stateOrder = []string{
	store.StateWaiting, store.StateQueued, store.StateDownloading,
	store.StateDownloaded, store.StateImporting, store.StateFinished,
	store.StateFailed, store.StateBlocklisted, store.StateSkipped,
}

func statusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show daemon and queue status",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := newClient()
			if err != nil {
				return err
			}
			// Live view via the daemon; fall back to the DB when it's down.
			if client.Available() {
				st, err := client.Status(ctx())
				if err != nil {
					return err
				}
				printStatus(st.DownloadStatus, st.DownloadRunning, st.Syncing, st.LastSync, st.Counts)
				return nil
			}

			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			s, err := store.Open(cfg.Paths.DBPath)
			if err != nil {
				return err
			}
			defer s.Close()
			counts, err := s.CountByState(ctx())
			if err != nil {
				return err
			}
			last, _ := s.GetMeta(ctx(), "last_sync")
			fmt.Println("daemon: not running (showing database snapshot)")
			printStatus("stopped", false, false, last, counts)
			return nil
		},
	}
}

func printStatus(dlStatus string, running, syncing bool, lastSync string, counts map[string]int) {
	fmt.Printf("download stage: %s (running=%v)\n", orDash(dlStatus), running)
	fmt.Printf("syncing:        %v\n", syncing)
	if lastSync != "" {
		fmt.Printf("last sync:      %s\n", lastSync)
	}
	fmt.Println("queue:")
	tw := tabwriter.NewWriter(cmdOut, 0, 0, 2, ' ', 0)
	seen := map[string]bool{}
	for _, st := range stateOrder {
		if n, ok := counts[st]; ok {
			fmt.Fprintf(tw, "  %s\t%d\n", st, n)
			seen[st] = true
		}
	}
	// Any states not in the canonical order.
	var extra []string
	for st := range counts {
		if !seen[st] {
			extra = append(extra, st)
		}
	}
	sort.Strings(extra)
	for _, st := range extra {
		fmt.Fprintf(tw, "  %s\t%d\n", st, counts[st])
	}
	tw.Flush()
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
