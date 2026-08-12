package deezer

import (
	"context"
	"fmt"
)

// Blocklist kinds (kept in sync with the store's kind constants).
const (
	KindTrack    = "track"
	KindAlbum    = "album"
	KindArtist   = "artist"
	KindPlaylist = "playlist"
)

// Selection chooses which favorite item types to enumerate.
type Selection struct {
	Albums    bool
	Artists   bool
	Playlists bool
	Tracks    bool
}

// Any reports whether at least one type is selected.
func (s Selection) Any() bool { return s.Albums || s.Artists || s.Playlists || s.Tracks }

// FavItem is a discovered track ready to be persisted as a queue row.
type FavItem struct {
	SngID       int64
	Title       string
	Artist      string
	Album       string
	AlbumArtist string
	GroupKey    string // album id — groups tracks of one release
	SourceType  string // track|album|artist|playlist
	SourceID    string
}

// BlockedFunc reports whether an external id of the given kind is blocklisted,
// so enumeration can skip blocked albums/artists/playlists before expanding.
type BlockedFunc func(kind string, id int64) (bool, error)

// EnumerateFavorites walks the selected favorite types, calling yield for every
// track. It expands albums/artists/playlists into their tracks and skips any
// whose id is blocklisted. yield may be called with duplicate SngIDs across
// sources; the store upsert dedupes by primary key.
func (c *Client) EnumerateFavorites(ctx context.Context, sel Selection, blocked BlockedFunc, yield func(FavItem) error) error {
	if c.UserID() == 0 {
		if err := c.Login(ctx); err != nil {
			return err
		}
	}
	if sel.Tracks {
		if err := c.enumTracks(ctx, blocked, yield); err != nil {
			return fmt.Errorf("tracks: %w", err)
		}
	}
	if sel.Albums {
		if err := c.enumAlbums(ctx, blocked, yield); err != nil {
			return fmt.Errorf("albums: %w", err)
		}
	}
	if sel.Playlists {
		if err := c.enumPlaylists(ctx, blocked, yield); err != nil {
			return fmt.Errorf("playlists: %w", err)
		}
	}
	if sel.Artists {
		if err := c.enumArtists(ctx, blocked, yield); err != nil {
			return fmt.Errorf("artists: %w", err)
		}
	}
	return nil
}

func (c *Client) enumTracks(ctx context.Context, blocked BlockedFunc, yield func(FavItem) error) error {
	ids, err := c.favoriteTrackIDs(ctx)
	if err != nil {
		return err
	}
	const batch = 100
	for i := 0; i < len(ids); i += batch {
		end := min(i+batch, len(ids))
		chunk := ids[i:end]
		tracks, err := c.getTracksData(ctx, chunk)
		if err != nil {
			return err
		}
		for j := range tracks {
			t := &tracks[j]
			if t.SngID == "" {
				continue
			}
			skip, err := isBlockedTrack(blocked, t.ID())
			if err != nil {
				return err
			}
			if skip {
				continue
			}
			if err := yield(trackToFav(t, "track", t.SngID)); err != nil {
				return err
			}
		}
	}
	return nil
}

func (c *Client) enumAlbums(ctx context.Context, blocked BlockedFunc, yield func(FavItem) error) error {
	rows, err := c.profileTab(ctx, "albums")
	if err != nil {
		return err
	}
	for _, row := range rows {
		albID := mapID(row, "ALB_ID")
		if albID == 0 {
			continue
		}
		if skip, err := isBlocked(blocked, KindAlbum, albID); err != nil {
			return err
		} else if skip {
			continue
		}
		if err := c.yieldAlbum(ctx, albID, blocked, "album", albID, yield); err != nil {
			return err
		}
	}
	return nil
}

func (c *Client) enumPlaylists(ctx context.Context, blocked BlockedFunc, yield func(FavItem) error) error {
	rows, err := c.profileTab(ctx, "playlists")
	if err != nil {
		return err
	}
	for _, row := range rows {
		plID := mapID(row, "PLAYLIST_ID")
		if plID == 0 {
			continue
		}
		if skip, err := isBlocked(blocked, KindPlaylist, plID); err != nil {
			return err
		} else if skip {
			continue
		}
		tracks, err := c.playlistTracks(ctx, plID)
		if err != nil {
			return err
		}
		for j := range tracks {
			t := &tracks[j]
			if t.SngID == "" {
				continue
			}
			if skip, err := isBlockedTrack(blocked, t.ID()); err != nil {
				return err
			} else if skip {
				continue
			}
			if err := yield(trackToFav(t, "playlist", fmt.Sprintf("%d", plID))); err != nil {
				return err
			}
		}
	}
	return nil
}

func (c *Client) enumArtists(ctx context.Context, blocked BlockedFunc, yield func(FavItem) error) error {
	rows, err := c.profileTab(ctx, "artists")
	if err != nil {
		return err
	}
	for _, row := range rows {
		artID := mapID(row, "ART_ID")
		if artID == 0 {
			continue
		}
		if skip, err := isBlocked(blocked, KindArtist, artID); err != nil {
			return err
		} else if skip {
			continue
		}
		albIDs, err := c.artistAlbumIDs(ctx, artID)
		if err != nil {
			return err
		}
		for _, albID := range albIDs {
			if skip, err := isBlocked(blocked, KindAlbum, albID); err != nil {
				return err
			} else if skip {
				continue
			}
			if err := c.yieldAlbum(ctx, albID, blocked, "artist", artID, yield); err != nil {
				return err
			}
		}
	}
	return nil
}

// SourceItem describes a top-level favorite source (album, artist, or playlist).
type SourceItem struct {
	Kind   string // album|artist|playlist
	ID     int64
	Name   string
	Artist string // album artist name; empty for artists/playlists
}

// FavoriteSources returns the user's favorited albums, artists, and playlists
// according to the Selection, without expanding to tracks.
func (c *Client) FavoriteSources(ctx context.Context, sel Selection, blocked BlockedFunc) ([]SourceItem, error) {
	if c.UserID() == 0 {
		if err := c.Login(ctx); err != nil {
			return nil, err
		}
	}
	var out []SourceItem

	if sel.Albums {
		rows, err := c.profileTab(ctx, "albums")
		if err != nil {
			return nil, fmt.Errorf("albums tab: %w", err)
		}
		for _, row := range rows {
			id := mapID(row, "ALB_ID")
			if id == 0 {
				continue
			}
			if skip, err := isBlocked(blocked, KindAlbum, id); err != nil {
				return nil, err
			} else if skip {
				continue
			}
			out = append(out, SourceItem{
				Kind:   KindAlbum,
				ID:     id,
				Name:   mapStr(row, "ALB_TITLE"),
				Artist: mapStr(row, "ART_NAME"),
			})
		}
	}

	if sel.Artists {
		rows, err := c.profileTab(ctx, "artists")
		if err != nil {
			return nil, fmt.Errorf("artists tab: %w", err)
		}
		for _, row := range rows {
			id := mapID(row, "ART_ID")
			if id == 0 {
				continue
			}
			if skip, err := isBlocked(blocked, KindArtist, id); err != nil {
				return nil, err
			} else if skip {
				continue
			}
			out = append(out, SourceItem{
				Kind: KindArtist,
				ID:   id,
				Name: mapStr(row, "ART_NAME"),
			})
		}
	}

	if sel.Playlists {
		rows, err := c.profileTab(ctx, "playlists")
		if err != nil {
			return nil, fmt.Errorf("playlists tab: %w", err)
		}
		for _, row := range rows {
			id := mapID(row, "PLAYLIST_ID")
			if id == 0 {
				continue
			}
			if skip, err := isBlocked(blocked, KindPlaylist, id); err != nil {
				return nil, err
			} else if skip {
				continue
			}
			out = append(out, SourceItem{
				Kind: KindPlaylist,
				ID:   id,
				Name: mapStr(row, "TITLE"),
			})
		}
	}

	return out, nil
}

func mapStr(row map[string]any, key string) string {
	if v, ok := row[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}


// TracksForSource expands a SourceItem into its constituent FavItems.
// For artists, it expands all albums in the discography.
func (c *Client) TracksForSource(ctx context.Context, src SourceItem, blocked BlockedFunc) ([]FavItem, error) {
	sid := fmt.Sprintf("%d", src.ID)
	switch src.Kind {
	case KindAlbum:
		tracks, err := c.albumTracks(ctx, src.ID)
		if err != nil {
			return nil, err
		}
		return filteredFavs(tracks, blocked, src.Kind, sid)

	case KindArtist:
		albIDs, err := c.artistAlbumIDs(ctx, src.ID)
		if err != nil {
			return nil, err
		}
		var out []FavItem
		for _, albID := range albIDs {
			if skip, err := isBlocked(blocked, KindAlbum, albID); err != nil {
				return nil, err
			} else if skip {
				continue
			}
			tracks, err := c.albumTracks(ctx, albID)
			if err != nil {
				return nil, err
			}
			favs, err := filteredFavs(tracks, blocked, src.Kind, sid)
			if err != nil {
				return nil, err
			}
			out = append(out, favs...)
		}
		return out, nil

	case KindPlaylist:
		tracks, err := c.playlistTracks(ctx, src.ID)
		if err != nil {
			return nil, err
		}
		return filteredFavs(tracks, blocked, src.Kind, sid)

	default:
		return nil, fmt.Errorf("unknown source kind %q", src.Kind)
	}
}

func filteredFavs(tracks []GWTrack, blocked BlockedFunc, sourceType, sourceID string) ([]FavItem, error) {
	var out []FavItem
	for j := range tracks {
		t := &tracks[j]
		if t.SngID == "" {
			continue
		}
		if skip, err := isBlockedTrack(blocked, t.ID()); err != nil {
			return nil, err
		} else if skip {
			continue
		}
		out = append(out, trackToFav(t, sourceType, sourceID))
	}
	return out, nil
}

func (c *Client) yieldAlbum(ctx context.Context, albID int64, blocked BlockedFunc, sourceType string, sourceID int64, yield func(FavItem) error) error {
	tracks, err := c.albumTracks(ctx, albID)
	if err != nil {
		return err
	}
	for j := range tracks {
		t := &tracks[j]
		if t.SngID == "" {
			continue
		}
		if skip, err := isBlockedTrack(blocked, t.ID()); err != nil {
			return err
		} else if skip {
			continue
		}
		if err := yield(trackToFav(t, sourceType, fmt.Sprintf("%d", sourceID))); err != nil {
			return err
		}
	}
	return nil
}

// TracksByKind expands a single external id of the given kind into its tracks.
// Used by `download <ids> --type ...`.
func (c *Client) TracksByKind(ctx context.Context, kind string, id int64) ([]FavItem, error) {
	if c.UserID() == 0 {
		if err := c.Login(ctx); err != nil {
			return nil, err
		}
	}
	sid := fmt.Sprintf("%d", id)
	switch kind {
	case KindTrack:
		t, err := c.GetTrack(ctx, id)
		if err != nil {
			return nil, err
		}
		if t.SngID == "" {
			return nil, nil
		}
		return []FavItem{trackToFav(t, KindTrack, t.SngID)}, nil
	case KindAlbum:
		tracks, err := c.albumTracks(ctx, id)
		if err != nil {
			return nil, err
		}
		return favsFrom(tracks, KindAlbum, sid), nil
	case KindPlaylist:
		tracks, err := c.playlistTracks(ctx, id)
		if err != nil {
			return nil, err
		}
		return favsFrom(tracks, KindPlaylist, sid), nil
	case KindArtist:
		albIDs, err := c.artistAlbumIDs(ctx, id)
		if err != nil {
			return nil, err
		}
		var out []FavItem
		for _, albID := range albIDs {
			tracks, err := c.albumTracks(ctx, albID)
			if err != nil {
				return nil, err
			}
			out = append(out, favsFrom(tracks, KindArtist, sid)...)
		}
		return out, nil
	default:
		return nil, fmt.Errorf("unknown kind %q", kind)
	}
}

func favsFrom(tracks []GWTrack, sourceType, sourceID string) []FavItem {
	out := make([]FavItem, 0, len(tracks))
	for j := range tracks {
		t := &tracks[j]
		if t.SngID == "" {
			continue
		}
		out = append(out, trackToFav(t, sourceType, sourceID))
	}
	return out
}

func trackToFav(t *GWTrack, sourceType, sourceID string) FavItem {
	return FavItem{
		SngID:       t.ID(),
		Title:       t.SngTitle,
		Artist:      t.ArtName,
		Album:       t.AlbTitle,
		AlbumArtist: t.ArtName,
		GroupKey:    t.AlbID,
		SourceType:  sourceType,
		SourceID:    sourceID,
	}
}

func isBlocked(blocked BlockedFunc, kind string, id int64) (bool, error) {
	if blocked == nil {
		return false, nil
	}
	return blocked(kind, id)
}

func isBlockedTrack(blocked BlockedFunc, id int64) (bool, error) {
	return isBlocked(blocked, KindTrack, id)
}

// mapID reads a Deezer id (string or number) from a decoded JSON row.
func mapID(row map[string]any, key string) int64 {
	v, ok := row[key]
	if !ok {
		return 0
	}
	switch x := v.(type) {
	case string:
		return parseID(x)
	case float64:
		return int64(x)
	default:
		return 0
	}
}
