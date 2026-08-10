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
	store.StateDownloaded, store.StateFinished,
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
			if client.Available() {
				st, err := client.Status(ctx())
				if err != nil {
					return err
				}
				printStatus(st.Stage, st.LastSync, st.Counts)
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
			stage, _ := s.GetMeta(ctx(), "current_stage")
			last, _ := s.GetMeta(ctx(), "last_sync")
			if stage == "" {
				stage = store.StageIdle
			}
			fmt.Println("daemon: not running (showing database snapshot)")
			printStatus(stage, last, counts)
			return nil
		},
	}
}

func printStatus(stage, lastSync string, counts map[string]int) {
	fmt.Printf("stage:     %s\n", orDash(stage))
	if lastSync != "" {
		fmt.Printf("last sync: %s\n", lastSync)
	}
	fmt.Println("queue:")
	tw := tabwriter.NewWriter(cmdOut, 0, 0, 2, ' ', 0)
	// Labels for states that benefit from extra context.
	labels := map[string]string{
		store.StateDownloaded: "downloaded (pending convert)",
	}
	seen := map[string]bool{}
	for _, st := range stateOrder {
		if n, ok := counts[st]; ok {
			label := st
			if l, ok := labels[st]; ok {
				label = l
			}
			fmt.Fprintf(tw, "  %s\t%d\n", label, n)
			seen[st] = true
		}
	}
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
