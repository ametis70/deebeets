package cmd

import (
	"fmt"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"deeznt/internal/control"
	"deeznt/internal/store"
)

// stateOrder is the display order for state counts.
var stateOrder = []string{
	store.StateWaiting, store.StateQueued, store.StateDownloading,
	store.StateDownloaded, store.StateTagging, store.StateTagged,
	store.StateConverting, store.StateConverted,
	store.StateFailedDownload, store.StateFailedTag, store.StateFailedConvert,
	store.StateBlocklisted, store.StateSkipped,
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
				printStatus(st)
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
			albumAvailability, _ := s.CountSourcesByDeezerStatus(ctx(), store.SourceKindAlbum)
			printStatus(control.StatusResponse{
				Stage:             stage,
				Counts:            counts,
				AlbumAvailability: albumAvailability,
				LastSync:          last,
			})
			return nil
		},
	}
}

func printStatus(st control.StatusResponse) {
	// Activity indicators.
	syncLabel := "idle"
	if st.Syncing {
		syncLabel = "running"
	}
	dlLabel := "idle"
	if st.Downloading {
		dlLabel = "running"
	}
	tagLabel := "idle"
	if st.Tagging {
		tagLabel = "running"
	}
	convLabel := "idle"
	if st.Converting {
		if st.ConvertingCount > 0 {
			convLabel = fmt.Sprintf("running (%d files)", st.ConvertingCount)
		} else {
			convLabel = "running"
		}
	}

	fmt.Printf("sync:      %s\n", syncLabel)
	fmt.Printf("download:  %s\n", dlLabel)
	fmt.Printf("tag:       %s\n", tagLabel)
	fmt.Printf("convert:   %s\n", convLabel)
	if st.LastSync != "" {
		fmt.Printf("last sync: %s\n", st.LastSync)
	}
	if len(st.AlbumAvailability) > 0 {
		fmt.Println("albums:")
		tw2 := tabwriter.NewWriter(cmdOut, 0, 0, 2, ' ', 0)
		for _, s := range []string{store.DeezerStatusPresent, store.DeezerStatusReplaced, store.DeezerStatusMissing} {
			if n, ok := st.AlbumAvailability[s]; ok && n > 0 {
				fmt.Fprintf(tw2, "  %s\t%d\n", strings.ToLower(s), n)
			}
		}
		tw2.Flush()
	}
	fmt.Println("queue:")
	tw := tabwriter.NewWriter(cmdOut, 0, 0, 2, ' ', 0)
		labels := map[string]string{
			store.StateDownloaded:     "downloaded (pending tag)",
			store.StateFailedDownload: "failed (download)",
			store.StateFailedTag:      "failed (tag)",
			store.StateFailedConvert:  "failed (convert)",
		}
		seen := map[string]bool{}
		for _, s := range stateOrder {
			if n, ok := st.Counts[s]; ok {
				label := s
				if l, ok := labels[s]; ok {
					label = l
				}
				fmt.Fprintf(tw, "  %s\t%d\n", label, n)
				seen[s] = true
			}
		}
	var extra []string
	for s := range st.Counts {
		if !seen[s] {
			extra = append(extra, s)
		}
	}
	sort.Strings(extra)
	for _, s := range extra {
		fmt.Fprintf(tw, "  %s\t%d\n", s, st.Counts[s])
	}
	tw.Flush()
}


