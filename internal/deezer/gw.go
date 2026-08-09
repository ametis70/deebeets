package deezer

import (
	"context"
	"encoding/json"
	"fmt"
)

// getTracksData hydrates full track objects for the given SNG_IDs via
// song.getListData.
func (c *Client) getTracksData(ctx context.Context, ids []int64) ([]GWTrack, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	results, err := c.apiCall(ctx, "song.getListData", map[string]any{"SNG_IDS": ids})
	if err != nil {
		return nil, err
	}
	var ld listData
	if err := json.Unmarshal(results, &ld); err != nil {
		return nil, fmt.Errorf("getListData: %w", err)
	}
	return ld.Data, nil
}

// GetTrack fetches a single track's full data via song.getData.
func (c *Client) GetTrack(ctx context.Context, sngID int64) (*GWTrack, error) {
	results, err := c.apiCall(ctx, "song.getData", map[string]any{"SNG_ID": sngID})
	if err != nil {
		return nil, err
	}
	var t GWTrack
	if err := json.Unmarshal(results, &t); err != nil {
		return nil, fmt.Errorf("getData: %w", err)
	}
	return &t, nil
}

// GetAlbum fetches album metadata via album.getData.
func (c *Client) GetAlbum(ctx context.Context, albID int64) (*GWAlbum, error) {
	results, err := c.apiCall(ctx, "album.getData", map[string]any{"ALB_ID": albID})
	if err != nil {
		return nil, err
	}
	var a GWAlbum
	if err := json.Unmarshal(results, &a); err != nil {
		return nil, fmt.Errorf("album.getData: %w", err)
	}
	return &a, nil
}

// GetLyrics fetches synced and plain lyrics via song.getLyrics.
// Returns nil if the track has no lyrics (LYRICS_ID == 0).
func (c *Client) GetLyrics(ctx context.Context, sngID int64) (*GWLyrics, error) {
	results, err := c.apiCall(ctx, "song.getLyrics", map[string]any{"SNG_ID": sngID})
	if err != nil {
		return nil, err
	}
	var l GWLyrics
	if err := json.Unmarshal(results, &l); err != nil {
		return nil, fmt.Errorf("getLyrics: %w", err)
	}
	return &l, nil
}

// favoriteTrackIDs returns the user's loved-track SNG_IDs (paged internally).
func (c *Client) favoriteTrackIDs(ctx context.Context) ([]int64, error) {
	const page = 2000
	var out []int64
	for start := 0; ; start += page {
		results, err := c.apiCall(ctx, "song.getFavoriteIds", map[string]any{
			"nb": page, "start": start, "checksum": nil,
		})
		if err != nil {
			return nil, err
		}
		var fav favoriteIDs
		if err := json.Unmarshal(results, &fav); err != nil {
			return nil, fmt.Errorf("getFavoriteIds: %w", err)
		}
		for _, d := range fav.Data {
			if id := parseID(d.SngID); id != 0 {
				out = append(out, id)
			}
		}
		if len(fav.Data) < page {
			break
		}
	}
	return out, nil
}

// profileTab fetches a deezer.pageProfile tab ("albums"|"artists"|"playlists")
// and returns the raw row maps.
func (c *Client) profileTab(ctx context.Context, tab string) ([]map[string]any, error) {
	results, err := c.apiCall(ctx, "deezer.pageProfile", map[string]any{
		"USER_ID": c.UserID(), "tab": tab, "nb": 10000,
	})
	if err != nil {
		return nil, err
	}
	var pp profilePage
	if err := json.Unmarshal(results, &pp); err != nil {
		return nil, fmt.Errorf("pageProfile %s: %w", tab, err)
	}
	switch tab {
	case "albums":
		return pp.Tab.Albums.Data, nil
	case "artists":
		return pp.Tab.Artists.Data, nil
	case "playlists":
		return pp.Tab.Playlists.Data, nil
	default:
		return nil, fmt.Errorf("unknown profile tab %q", tab)
	}
}

// albumTracks returns all tracks of an album via song.getListByAlbum.
func (c *Client) albumTracks(ctx context.Context, albID int64) ([]GWTrack, error) {
	results, err := c.apiCall(ctx, "song.getListByAlbum", map[string]any{
		"ALB_ID": albID, "nb": -1,
	})
	if err != nil {
		return nil, err
	}
	var ld listData
	if err := json.Unmarshal(results, &ld); err != nil {
		return nil, fmt.Errorf("getListByAlbum: %w", err)
	}
	return ld.Data, nil
}

// playlistTracks returns all tracks of a playlist via playlist.getSongs.
func (c *Client) playlistTracks(ctx context.Context, playlistID int64) ([]GWTrack, error) {
	results, err := c.apiCall(ctx, "playlist.getSongs", map[string]any{
		"PLAYLIST_ID": playlistID, "nb": -1,
	})
	if err != nil {
		return nil, err
	}
	var ld listData
	if err := json.Unmarshal(results, &ld); err != nil {
		return nil, fmt.Errorf("getSongs: %w", err)
	}
	return ld.Data, nil
}

// artistAlbumIDs returns all album ids in an artist's discography.
func (c *Client) artistAlbumIDs(ctx context.Context, artID int64) ([]int64, error) {
	const page = 100
	var out []int64
	for start := 0; ; start += page {
		results, err := c.apiCall(ctx, "album.getDiscography", map[string]any{
			"ART_ID": artID, "discography_mode": "all",
			"nb": page, "nb_songs": 0, "start": start,
		})
		if err != nil {
			return nil, err
		}
		var d discography
		if err := json.Unmarshal(results, &d); err != nil {
			return nil, fmt.Errorf("getDiscography: %w", err)
		}
		for _, a := range d.Data {
			if id := parseID(a.AlbID); id != 0 {
				out = append(out, id)
			}
		}
		if len(d.Data) < page {
			break
		}
	}
	return out, nil
}
