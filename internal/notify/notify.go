// Package notify sends webhook notifications for pipeline events.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"deeznt/internal/config"
)

// Event names.
const (
	EventDownloadsStarted  = "downloads_started"
	EventDownloadsFinished = "downloads_finished"
	EventDownloadsFailed   = "downloads_failed"
	EventConvertsFinished  = "converts_finished"
	EventConvertsFailed    = "converts_failed"
	EventTest              = "test"
)

// Payload is the JSON body sent to the webhook.
type Payload struct {
	Event     string         `json:"event"`
	Timestamp string         `json:"timestamp"`
	Data      map[string]any `json:"data,omitempty"`
}

// Notifier sends webhook notifications.
type Notifier struct {
	cfg    config.Notifications
	log    *slog.Logger
	client *http.Client
}

// New creates a Notifier. If webhook_url is empty all Send calls are no-ops.
func New(cfg config.Notifications, log *slog.Logger) *Notifier {
	return &Notifier{
		cfg:    cfg,
		log:    log,
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

// NewSilent creates a Notifier that discards all log output.
// Used by CLI commands where errors are reported directly to stdout.
func NewSilent(cfg config.Notifications) *Notifier {
	return New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// Enabled reports whether notifications are configured.
func (n *Notifier) Enabled() bool {
	return n.cfg.WebhookURL != ""
}

// Send fires a webhook for the given event if that event is enabled.
// Non-blocking — the POST happens in a goroutine so it never delays the pipeline.
// Failures are logged at Error level.
func (n *Notifier) Send(event string, data map[string]any) {
	if !n.Enabled() || !n.eventEnabled(event) {
		return
	}
	p := Payload{
		Event:     event,
		Timestamp: time.Now().Format(time.RFC3339),
		Data:      data,
	}
	go func() {
		if err := n.post(context.Background(), p); err != nil {
			n.log.Error("webhook notification failed",
				"event", event,
				"url", n.cfg.WebhookURL,
				"err", err)
		} else {
			n.log.Debug("webhook notification sent", "event", event)
		}
	}()
}

// SendTest sends a test notification synchronously and returns any error.
// Used by `deeznt notify test` to verify the webhook is reachable.
func (n *Notifier) SendTest(ctx context.Context) error {
	if !n.Enabled() {
		return fmt.Errorf("notifications not configured (webhook_url is empty)")
	}
	p := Payload{
		Event:     EventTest,
		Timestamp: time.Now().Format(time.RFC3339),
		Data:      map[string]any{"message": "deeznt webhook test"},
	}
	return n.post(ctx, p)
}

func (n *Notifier) post(ctx context.Context, p Payload) error {
	body, err := json.Marshal(p)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.cfg.WebhookURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "deeznt-notify/1.0")
	if n.cfg.AuthHeader != "" {
		req.Header.Set(n.cfg.AuthHeader, n.cfg.AuthValue)
	}

	resp, err := n.client.Do(req)
	if err != nil {
		return fmt.Errorf("POST %s: %w", n.cfg.WebhookURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("POST %s: server returned %d", n.cfg.WebhookURL, resp.StatusCode)
	}
	return nil
}

func (n *Notifier) eventEnabled(event string) bool {
	on := n.cfg.On
	switch event {
	case EventDownloadsStarted:
		return on.DownloadsStarted
	case EventDownloadsFinished:
		return on.DownloadsFinished
	case EventDownloadsFailed:
		return on.DownloadsFailed
	case EventConvertsFinished:
		return on.ConvertsFinished
	case EventConvertsFailed:
		return on.ConvertsFailed
	default:
		return false
	}
}
