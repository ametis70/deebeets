package control

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"time"

	"deebeets/internal/store"
)

// Client talks to the daemon's control socket.
type Client struct {
	http       *http.Client
	socketPath string
}

// NewClient builds a client for the given Unix socket path.
func NewClient(socketPath string) *Client {
	return &Client{
		socketPath: socketPath,
		http: &http.Client{
			Timeout: 30 * time.Minute,
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					var d net.Dialer
					return d.DialContext(ctx, "unix", socketPath)
				},
			},
		},
	}
}

// Available reports whether the daemon socket accepts connections.
func (c *Client) Available() bool {
	conn, err := net.DialTimeout("unix", c.socketPath, time.Second)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var rdr *bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://unix"+path, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("daemon not reachable at %s: %w", c.socketPath, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		var apiErr APIResponse
		if json.NewDecoder(resp.Body).Decode(&apiErr) == nil && apiErr.Error != "" {
			return fmt.Errorf("%s", apiErr.Error)
		}
		return fmt.Errorf("daemon returned %d", resp.StatusCode)
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

// Status fetches daemon/queue status.
func (c *Client) Status(ctx context.Context) (StatusResponse, error) {
	var out StatusResponse
	err := c.do(ctx, http.MethodGet, "/status", nil, &out)
	return out, err
}

// SyncStart triggers an immediate sync run.
func (c *Client) SyncStart(ctx context.Context, sel Selection) error {
	return c.do(ctx, http.MethodPost, "/sync/start", SyncStartRequest{Selection: sel}, nil)
}

// SyncStop cancels an in-progress sync.
func (c *Client) SyncStop(ctx context.Context) error {
	return c.do(ctx, http.MethodPost, "/sync/stop", nil, nil)
}

// DownloadStart enqueues ids (optional) and triggers the download run.
func (c *Client) DownloadStart(ctx context.Context, kind string, ids []int64) error {
	return c.do(ctx, http.MethodPost, "/download/start", DownloadStartRequest{Kind: kind, IDs: ids}, nil)
}

// DownloadStop aborts the active download run.
func (c *Client) DownloadStop(ctx context.Context) error {
	return c.do(ctx, http.MethodPost, "/download/stop", nil, nil)
}

// Redownload forces re-download (mode "all", "missing", or "failed").
func (c *Client) Redownload(ctx context.Context, mode string, ids []int64) (int, error) {
	var out CountResponse
	err := c.do(ctx, http.MethodPost, "/redownload", RedownloadRequest{Mode: mode, IDs: ids}, &out)
	return out.Count, err
}

// BlocklistAdd adds entries.
func (c *Client) BlocklistAdd(ctx context.Context, kind string, ids []int64, reason string) error {
	return c.do(ctx, http.MethodPost, "/blocklist", BlocklistRequest{Kind: kind, IDs: ids, Reason: reason}, nil)
}

// BlocklistRemove removes entries.
func (c *Client) BlocklistRemove(ctx context.Context, kind string, ids []int64) error {
	return c.do(ctx, http.MethodDelete, "/blocklist", BlocklistRequest{Kind: kind, IDs: ids}, nil)
}

// BlocklistList lists entries.
func (c *Client) BlocklistList(ctx context.Context) ([]store.Block, error) {
	var out []store.Block
	err := c.do(ctx, http.MethodGet, "/blocklist", nil, &out)
	return out, err
}

// ConvertStart triggers a manual convert run.
func (c *Client) ConvertStart(ctx context.Context) error {
	return c.do(ctx, http.MethodPost, "/convert/start", nil, nil)
}

// Items lists items in the given states.
func (c *Client) Items(ctx context.Context, states []string) ([]store.Item, error) {
	path := "/items"
	if len(states) > 0 {
		path += "?state=" + joinCSV(states)
	}
	var out ItemsResponse
	err := c.do(ctx, http.MethodGet, path, nil, &out)
	return out.Items, err
}

func joinCSV(parts []string) string {
	s := ""
	for i, p := range parts {
		if i > 0 {
			s += ","
		}
		s += p
	}
	return s
}
