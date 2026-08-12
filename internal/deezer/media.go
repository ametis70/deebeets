package deezer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
)

// ErrWrongLicense means the account can't stream the requested format.
var ErrWrongLicense = errors.New("deezer: account cannot stream this format")

// ErrWrongGeolocation means the track isn't available in the account's country.
var ErrWrongGeolocation = errors.New("deezer: track not available in your country")

// ResolvedURL is a playable (still-encrypted) source for a track in one format.
type ResolvedURL struct {
	URL    string
	Format string // config name, e.g. "FLAC"
}

// parseID converts a Deezer string id to int64.
func parseID(s string) int64 {
	n, _ := strconv.ParseInt(s, 10, 64)
	return n
}

// canStream reports whether the account may stream the named format.
func (c *Client) canStream(format string) bool {
	switch format {
	case "FLAC":
		return c.CanStreamLossless()
	case "MP3_320", "MP3_256":
		return c.CanStreamHQ()
	default:
		return true
	}
}

// getTrackURLViaMedia resolves a single track token to a source URL for one
// format using media.deezer.com/v1/get_url.
func (c *Client) getTrackURLViaMedia(ctx context.Context, trackToken, format string) (string, error) {
	c.mu.Lock()
	license := c.licenseToken
	c.mu.Unlock()
	if license == "" {
		if err := c.Login(ctx); err != nil {
			return "", err
		}
		c.mu.Lock()
		license = c.licenseToken
		c.mu.Unlock()
	}
	if !c.canStream(format) {
		return "", ErrWrongLicense
	}

	payload := map[string]any{
		"license_token": license,
		"media": []map[string]any{{
			"type":    "FULL",
			"formats": []map[string]any{{"cipher": "BF_CBC_STRIPE", "format": format}},
		}},
		"track_tokens": []string{trackToken},
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.mediaURL, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", classifyErr(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests {
		return "", ErrRateLimited
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("get_url: http %d", resp.StatusCode)
	}

	var parsed struct {
		Data []struct {
			Media []struct {
				Sources []struct {
					URL string `json:"url"`
				} `json:"sources"`
			} `json:"media"`
			Errors []struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"errors"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return "", fmt.Errorf("get_url decode: %w", err)
	}
	if len(parsed.Data) == 0 {
		return "", fmt.Errorf("get_url: empty response")
	}
	d := parsed.Data[0]
	if len(d.Errors) > 0 {
		if d.Errors[0].Code == 2002 {
			return "", ErrWrongGeolocation
		}
		return "", fmt.Errorf("get_url: %s", d.Errors[0].Message)
	}
	if len(d.Media) > 0 && len(d.Media[0].Sources) > 0 {
		return d.Media[0].Sources[0].URL, nil
	}
	return "", nil // no source for this format
}

// ResolveDownload walks the caller's format priority and returns the first
// playable source. It uses the modern media endpoint first and falls back to the
// legacy CDN URL. formatPriority holds config format names (e.g. "FLAC").
func (c *Client) ResolveDownload(ctx context.Context, t *GWTrack, formatPriority []string) (*ResolvedURL, error) {
	var lastErr error
	for _, format := range formatPriority {
		if _, ok := formatCode(format); !ok {
			continue
		}
		if !c.canStream(format) {
			continue // don't even try formats the license forbids
		}

		if t.TrackToken != "" {
			url, err := c.getTrackURLViaMedia(ctx, t.TrackToken, format)
			if IsRateLimited(err) {
				return nil, err
			}
			if err == nil && url != "" {
				return &ResolvedURL{URL: url, Format: format}, nil
			}
			if err != nil && !errors.Is(err, ErrWrongLicense) {
				lastErr = err
			}
		}

		// Legacy fallback via the CDN URL derived from MD5_ORIGIN.
		code, _ := formatCode(format)
		if url, err := legacyStreamURL(t.ID(), t.MD5Origin, t.MediaVersionInt(), code); err == nil && url != "" {
			return &ResolvedURL{URL: url, Format: format}, nil
		}
	}

	// Try the track's own fallback track if Deezer supplied one.
	if t.Fallback != nil && t.Fallback.SngID != "" && t.Fallback.SngID != t.SngID {
		return c.ResolveDownload(ctx, t.Fallback, formatPriority)
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("no downloadable source for track %d", t.ID())
}

// FetchCover downloads the album cover JPEG for a track's ALB_PICTURE hash at
// the given square size. Returns (nil, "", nil) when no picture is available.
func (c *Client) FetchCover(ctx context.Context, albPicture string, size int) ([]byte, string, error) {
	return c.fetchImage(ctx, "cover", albPicture, size)
}

// FetchArtistImage downloads the artist image JPEG for an ART_PICTURE hash at
// the given square size. Returns (nil, "", nil) when no picture is available.
func (c *Client) FetchArtistImage(ctx context.Context, artPicture string, size int) ([]byte, string, error) {
	return c.fetchImage(ctx, "artist", artPicture, size)
}

func (c *Client) fetchImage(ctx context.Context, kind, picHash string, size int) ([]byte, string, error) {
	if picHash == "" {
		return nil, "", nil
	}
	url := fmt.Sprintf("https://e-cdns-images.dzcdn.net/images/%s/%s/%dx%d-000000-80-0-0.jpg",
		kind, picHash, size, size)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", nil
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, "", err
	}
	return data, "image/jpeg", nil
}

// Download opens the (encrypted) body stream for a resolved URL, optionally
// resuming from a byte offset. Returns the response so the caller can inspect
// Content-Length and status; the caller must close resp.Body.
func (c *Client) Download(ctx context.Context, rawURL string, resumeAt int64) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	if resumeAt > 0 {
		req.Header.Set("Range", "bytes="+strconv.FormatInt(resumeAt, 10)+"-")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, classifyErr(err)
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		resp.Body.Close()
		return nil, ErrRateLimited
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		resp.Body.Close()
		return nil, fmt.Errorf("download: http %d", resp.StatusCode)
	}
	return resp, nil
}
