package cmd

import (
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"deeznt/internal/store"
)

// stateOrder is the display order for state counts (also used by status).
func listCmd() *cobra.Command {
	var states []string
	var limit int
	var albums, artists, playlists, tracks bool
	var deezerStatus string

	c := &cobra.Command{
		Use:   "list",
		Short: "List queue items or sources",
		Long: "With no flags, lists all PRESENT tracks (excludes replaced/missing).\n" +
			"Use --deezer-status replaced|missing|present to filter by availability.\n" +
			"Use --albums, --artists, --playlists to list sources instead.",
		RunE: func(cmd *cobra.Command, args []string) error {
			showSources := albums || artists || playlists
			if showSources && !tracks {
				return listSources(albums, artists, playlists, deezerStatus)
			}
			var sourceKindFilter []string
			if albums {
				sourceKindFilter = append(sourceKindFilter, store.KindAlbum)
			}
			if artists {
				sourceKindFilter = append(sourceKindFilter, store.KindArtist)
			}
			if playlists {
				sourceKindFilter = append(sourceKindFilter, store.KindPlaylist)
			}
			return listTracks(states, limit, sourceKindFilter, deezerStatus)
		},
	}
	c.Flags().StringSliceVar(&states, "state", nil, "filter tracks by state(s), comma-separated")
	c.Flags().IntVar(&limit, "limit", 0, "max rows (0 = all)")
	c.Flags().BoolVar(&albums, "albums", false, "list albums (sources)")
	c.Flags().BoolVar(&artists, "artists", false, "list artists (sources)")
	c.Flags().BoolVar(&playlists, "playlists", false, "list playlists (sources)")
	c.Flags().BoolVar(&tracks, "tracks", false, "list tracks (use with source flags to filter by source kind)")
	c.Flags().StringVar(&deezerStatus, "deezer-status", "", "filter by Deezer availability: present|replaced|missing")
	return c
}

func listSources(albums, artists, playlists bool, deezerStatus string) error {
	var kinds []string
	if albums {
		kinds = append(kinds, store.KindAlbum)
	}
	if artists {
		kinds = append(kinds, store.KindArtist)
	}
	if playlists {
		kinds = append(kinds, store.KindPlaylist)
	}

	sources, err := fetchSources(kinds)
	if err != nil {
		return err
	}

	// Default to PRESENT only, same as tracks.
	if deezerStatus == "" {
		deezerStatus = store.DeezerStatusPresent
	}

	// Filter by deezer_status if specified (empty or "all" = no filter).
	if deezerStatus != "" && strings.ToLower(deezerStatus) != "all" {
		dsUpper := strings.ToUpper(deezerStatus)
		filtered := sources[:0]
		for _, s := range sources {
			if s.DeezerStatus == dsUpper || (s.DeezerStatus == "" && dsUpper == store.DeezerStatusPresent) {
				filtered = append(filtered, s)
			}
		}
		sources = filtered
	}

	if len(sources) == 0 {
		fmt.Println("no sources")
		return nil
	}
	showExtra := strings.ToLower(deezerStatus) == "all" ||
		strings.ToUpper(deezerStatus) == store.DeezerStatusReplaced ||
		strings.ToUpper(deezerStatus) == store.DeezerStatusMissing

	tw := tabwriter.NewWriter(cmdOut, 0, 0, 2, ' ', 0)
	if showExtra {
		fmt.Fprintln(tw, "KIND\tID\tSTATUS\tSTATE\tTRACKS\tREPLACED_BY\tARTIST\tNAME") //nolint:errcheck
	} else {
		fmt.Fprintln(tw, "KIND\tID\tSTATE\tTRACKS\tARTIST\tNAME") //nolint:errcheck
	}
	for _, s := range sources {
		if showExtra {
			replBy := ""
			if s.ReplacementID > 0 {
				replBy = fmt.Sprintf("%d", s.ReplacementID)
			}
		_, _ = fmt.Fprintf(tw, "%s\t%d\t%s\t%s\t%d\t%s\t%s\t%s\n",
			s.Kind, s.ExtID, s.DeezerStatus, s.State, s.TrackCount,
			replBy, trunc(s.Artist, 24), trunc(s.Name, 40))
		} else {
			_, _ = fmt.Fprintf(tw, "%s\t%d\t%s\t%d\t%s\t%s\n",
			s.Kind, s.ExtID, s.State, s.TrackCount,
			trunc(s.Artist, 24), trunc(s.Name, 40))
		}
	}
	tw.Flush() //nolint:errcheck
	return nil
}

func listTracks(states []string, limit int, sourceKindFilter []string, deezerStatus string) error {
	// Default: hide replaced/missing unless explicitly requested.
	if deezerStatus == "" {
		deezerStatus = store.DeezerStatusPresent
	}
	// "all" opt-out: show everything.
	if strings.ToLower(deezerStatus) == "all" {
		deezerStatus = ""
	}

	items, err := fetchItems(states, limit, deezerStatus)
	if err != nil {
		return err
	}

	// Filter by source kind if requested.
	if len(sourceKindFilter) > 0 {
		kindSet := make(map[string]bool, len(sourceKindFilter))
		for _, k := range sourceKindFilter {
			kindSet[k] = true
		}
		filtered := items[:0]
		for _, it := range items {
			if kindSet[it.SourceType] {
				filtered = append(filtered, it)
			}
		}
		items = filtered
	}

	if len(items) == 0 {
		fmt.Println("no items")
		return nil
	}
	tw := tabwriter.NewWriter(cmdOut, 0, 0, 2, ' ', 0)
	showReplaced := deezerStatus == "" || strings.ToUpper(deezerStatus) == store.DeezerStatusReplaced
	if showReplaced {
		fmt.Fprintln(tw, "SNG_ID\tSTATUS\tSTATE\tARTIST\tTITLE\tFORMAT\tREPLACED_BY\tERROR") //nolint:errcheck
	} else {
		fmt.Fprintln(tw, "SNG_ID\tSTATE\tARTIST\tTITLE\tFORMAT\tERROR") //nolint:errcheck
	}
	for _, it := range items {
		if showReplaced {
			replBy := ""
			if it.ReplacementID > 0 {
				replBy = fmt.Sprintf("%d", it.ReplacementID)
			}
			_, _ = fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
				it.SngID, it.DeezerStatus, it.State,
				trunc(it.Artist, 24), trunc(it.Title, 32),
				it.Format, replBy, trunc(it.Error, 40))
		} else {
			_, _ = fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%s\n",
				it.SngID, it.State, trunc(it.Artist, 24), trunc(it.Title, 32),
				it.Format, trunc(it.Error, 40))
		}
	}
	tw.Flush() //nolint:errcheck
	return nil
}

func fetchItems(states []string, limit int, deezerStatus string) ([]store.Item, error) {
	client, err := newClient()
	if err != nil {
		return nil, err
	}
	if client.Available() {
		return client.Items(ctx(), states, deezerStatus)
	}
	cfg, err := loadConfig()
	if err != nil {
		return nil, err
	}
	s, err := store.Open(cfg.Paths.DBPath)
	if err != nil {
		return nil, err
	}
	defer s.Close() //nolint:errcheck
	var ptrs []*store.Item
	if deezerStatus != "" {
		ptrs, err = s.ListByDeezerStatus(ctx(), deezerStatus, states, limit)
	} else {
		ptrs, err = s.List(ctx(), states, limit)
	}
	if err != nil {
		return nil, err
	}
	out := make([]store.Item, 0, len(ptrs))
	for _, p := range ptrs {
		out = append(out, *p)
	}
	return out, nil
}

func fetchSources(kinds []string) ([]store.Source, error) {
	client, err := newClient()
	if err != nil {
		return nil, err
	}
	if client.Available() {
		return client.Sources(ctx(), kinds)
	}
	cfg, err := loadConfig()
	if err != nil {
		return nil, err
	}
	s, err := store.Open(cfg.Paths.DBPath)
	if err != nil {
		return nil, err
	}
	defer s.Close() //nolint:errcheck
	ptrs, err := s.ListSources(ctx(), kinds)
	if err != nil {
		return nil, err
	}
	out := make([]store.Source, 0, len(ptrs))
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
