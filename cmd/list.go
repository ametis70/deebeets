package cmd

import (
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"deeznt/internal/store"
)

// stateOrder is the display order for state counts (also used by status).
func listCmd() *cobra.Command {
	var states []string
	var limit int
	var albums, artists, playlists, tracks bool

	c := &cobra.Command{
		Use:   "list",
		Short: "List queue items or sources",
		Long: "With no flags, lists all tracks.\n" +
			"Use --albums, --artists, --playlists to list sources instead.\n" +
			"Use --tracks with a source flag to list tracks for that source kind.",
		RunE: func(cmd *cobra.Command, args []string) error {
			showSources := albums || artists || playlists

			// --tracks alone (no source flag): list tracks as before.
			// --tracks with source flag: list tracks filtered to that source kind.
			// source flag alone: list sources of that kind.
			if showSources && !tracks {
				return listSources(albums, artists, playlists)
			}

			// Track listing — optionally filter by source kind.
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

			return listTracks(states, limit, sourceKindFilter)
		},
	}
	c.Flags().StringSliceVar(&states, "state", nil, "filter tracks by state(s), comma-separated")
	c.Flags().IntVar(&limit, "limit", 0, "max rows (0 = all)")
	c.Flags().BoolVar(&albums, "albums", false, "list albums (sources)")
	c.Flags().BoolVar(&artists, "artists", false, "list artists (sources)")
	c.Flags().BoolVar(&playlists, "playlists", false, "list playlists (sources)")
	c.Flags().BoolVar(&tracks, "tracks", false, "list tracks (use with source flags to filter by source kind)")
	return c
}

func listSources(albums, artists, playlists bool) error {
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
	if len(sources) == 0 {
		fmt.Println("no sources")
		return nil
	}
	tw := tabwriter.NewWriter(cmdOut, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "KIND\tID\tSTATE\tTRACKS\tARTIST\tNAME")
	for _, s := range sources {
		fmt.Fprintf(tw, "%s\t%d\t%s\t%d\t%s\t%s\n",
			s.Kind, s.ExtID, s.State, s.TrackCount,
			trunc(s.Artist, 24), trunc(s.Name, 40))
	}
	tw.Flush()
	return nil
}

func listTracks(states []string, limit int, sourceKindFilter []string) error {
	items, err := fetchItems(states, limit)
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
	fmt.Fprintln(tw, "SNG_ID\tSTATE\tARTIST\tTITLE\tFORMAT\tERROR")
	for _, it := range items {
		fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%s\n",
			it.SngID, it.State, trunc(it.Artist, 24), trunc(it.Title, 32),
			it.Format, trunc(it.Error, 40))
	}
	tw.Flush()
	return nil
}

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
	defer s.Close()
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
