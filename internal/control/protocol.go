// Package control implements the daemon's local control plane: a JSON-over-HTTP
// API served on a Unix socket, plus a matching client used by the CLI.
package control

import (
	"context"

	"deebeets/internal/store"
)

// Controller is the daemon-side behaviour the server exposes.
type Controller interface {
	Status(ctx context.Context) (StatusResponse, error)

	// Sync triggers an immediate sync run. Errors if downloads are active.
	SyncStart(ctx context.Context, sel Selection) error
	// SyncStop cancels an in-progress sync. Errors if downloads are active.
	SyncStop(ctx context.Context) error

	// DownloadStart enqueues ids (optional) and triggers the download run.
	DownloadStart(ctx context.Context, kind string, ids []int64) error
	// DownloadStop aborts the active download run after the current batch.
	DownloadStop(ctx context.Context) error

	Redownload(ctx context.Context, mode string, ids []int64) (int, error)

	BlocklistAdd(ctx context.Context, kind string, ids []int64, reason string) error
	BlocklistRemove(ctx context.Context, kind string, ids []int64) error
	BlocklistList(ctx context.Context) ([]store.Block, error)

	// ConvertStart triggers a manual convert run (pending/missing files only).
	ConvertStart(ctx context.Context) error
	// Reconvert forces reconversion. Mode: "all" deletes existing converted
	// files and reconverts everything; "failed" retries state=downloaded items.
	Reconvert(ctx context.Context, mode string) (int, error)

	Items(ctx context.Context, states []string, limit int) ([]store.Item, error)
	Sources(ctx context.Context, kinds []string) ([]store.Source, error)
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
	// Stage is the current orchestrator stage: idle|syncing|downloading|importing.
	Stage           string         `json:"stage"`
	Syncing         bool           `json:"syncing"`
	Downloading     bool           `json:"downloading"`
	Converting      bool           `json:"converting"`
	ConvertingCount int            `json:"converting_count"`
	Counts          map[string]int `json:"counts"`
	// FailedByStage breaks down failed items by stage: "download" or "convert".
	FailedByStage   map[string]int `json:"failed_by_stage,omitempty"`
	LastSync        string         `json:"last_sync,omitempty"`
}

// SyncStartRequest selects favorite types to pull.
type SyncStartRequest struct {
	Selection
}

// DownloadStartRequest enqueues specific ids of a kind (optional).
type DownloadStartRequest struct {
	Kind string  `json:"kind,omitempty"`
	IDs  []int64 `json:"ids,omitempty"`
}

// RedownloadRequest forces re-download. Mode is "all", "missing", or "failed".
type RedownloadRequest struct {
	Mode string  `json:"mode"`
	IDs  []int64 `json:"ids,omitempty"`
}

// ReconvertRequest forces re-conversion. Mode is "all" or "failed".
type ReconvertRequest struct {
	Mode string `json:"mode"`
}

// BlocklistRequest adds/removes blocklist entries.
type BlocklistRequest struct {
	Kind   string  `json:"kind"`
	IDs    []int64 `json:"ids"`
	Reason string  `json:"reason,omitempty"`
}

// ItemsResponse carries a list of items.
type ItemsResponse struct {
	Items []store.Item `json:"items"`
}

// SourcesResponse carries a list of sources.
type SourcesResponse struct {
	Sources []store.Source `json:"sources"`
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
