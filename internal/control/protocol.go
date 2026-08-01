// Package control implements the daemon's local control plane: a JSON-over-HTTP
// API served on a Unix socket, plus a matching client used by the CLI.
package control

import (
	"context"

	"deebeets/internal/store"
)

// Controller is the daemon-side behaviour the server exposes. The daemon
// implements it; the control package never imports the daemon.
type Controller interface {
	Status(ctx context.Context) (StatusResponse, error)
	Sync(ctx context.Context, sel Selection) (SyncStarted, error)
	Download(ctx context.Context, kind string, ids []int64) (int, error)
	Redownload(ctx context.Context, mode string, ids []int64) (int, error)
	StartDownload(ctx context.Context) error
	StopDownload(ctx context.Context) error
	BlocklistAdd(ctx context.Context, kind string, ids []int64, reason string) error
	BlocklistRemove(ctx context.Context, kind string, ids []int64) error
	BlocklistList(ctx context.Context) ([]store.Block, error)
	BeetsImport(ctx context.Context, path string) error
	Items(ctx context.Context, states []string, limit int) ([]store.Item, error)
}

// Selection mirrors deezer.Selection over the wire.
type Selection struct {
	Albums    bool `json:"albums"`
	Artists   bool `json:"artists"`
	Playlists bool `json:"playlists"`
	Tracks    bool `json:"tracks"`
}

// StatusResponse summarises daemon and queue state.
type StatusResponse struct {
	DownloadRunning bool           `json:"download_running"`
	DownloadStatus  string         `json:"download_status"`
	Counts          map[string]int `json:"counts"`
	LastSync        string         `json:"last_sync,omitempty"`
	Syncing         bool           `json:"syncing"`
}

// SyncStarted reports whether a background sync was kicked off.
type SyncStarted struct {
	Started bool   `json:"started"`
	Message string `json:"message"`
}

// SyncRequest selects favorite types to pull.
type SyncRequest struct {
	Selection
}

// DownloadRequest enqueues specific ids of a kind (track|album|artist|playlist).
type DownloadRequest struct {
	Kind string  `json:"kind"`
	IDs  []int64 `json:"ids"`
}

// RedownloadRequest forces re-download. Mode is "all" or "missing".
type RedownloadRequest struct {
	Mode string  `json:"mode"`
	IDs  []int64 `json:"ids,omitempty"`
}

// BlocklistRequest adds/removes blocklist entries.
type BlocklistRequest struct {
	Kind   string  `json:"kind"`
	IDs    []int64 `json:"ids"`
	Reason string  `json:"reason,omitempty"`
}

// BeetsImportRequest triggers a manual import of a path.
type BeetsImportRequest struct {
	Path string `json:"path"`
}

// ItemsResponse carries a list of items.
type ItemsResponse struct {
	Items []store.Item `json:"items"`
}

// CountResponse reports how many items an action affected.
type CountResponse struct {
	Count int `json:"count"`
}

// APIResponse is the generic envelope for actions with no specific payload.
type APIResponse struct {
	OK      bool   `json:"ok"`
	Message string `json:"message,omitempty"`
	Error   string `json:"error,omitempty"`
}
