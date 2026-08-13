// Package config loads deeznt configuration from a TOML file, environment
// variables (prefixed DEEZNT_) and built-in defaults, in that increasing
// order of precedence.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/go-viper/mapstructure/v2"
	"github.com/knadh/koanf/parsers/toml"
	"github.com/knadh/koanf/providers/confmap"
	"github.com/knadh/koanf/providers/env"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"
)

// Config is the fully-resolved application configuration.
type Config struct {
	Deezer        Deezer        `koanf:"deezer"`
	Paths         Paths         `koanf:"paths"`
	Sync          Sync          `koanf:"sync"`
	Download      Download      `koanf:"download"`
	Tag           Tag           `koanf:"tag"`
	Convert       Convert       `koanf:"convert"`
	Notifications Notifications `koanf:"notifications"`
	RateLimit     RateLimit     `koanf:"ratelimit"`
	Tags          Tags          `koanf:"tags"`
	PostHooks     []string      `koanf:"posthooks"`

	// FixtureAlbums is populated from DEEZNT_FIXTURE_ALBUMS (comma-separated
	// album IDs). When set the daemon's sync uses this list instead of fetching
	// real Deezer favorites, exercising the full pipeline on a fixed set.
	FixtureAlbums []int64 `koanf:"-"`
}

// Deezer holds credentials and format preferences.
type Deezer struct {
	// ARL is only read from the dedicated DEEZNT_ARL env var or the encrypted
	// DB entry written by `deeznt login`. It must never appear in config files.
	ARL            string   `koanf:"-"`
	FormatPriority []string `koanf:"format_priority"`
}

// Paths holds on-disk locations.
type Paths struct {
	MusicDir   string `koanf:"music_dir"`
	DBPath     string `koanf:"db_path"`
	SocketPath string `koanf:"socket_path"`
}

// RetryPolicy is a reusable retry configuration block.
type RetryPolicy struct {
	MaxAttempts int           `koanf:"max_attempts"`
	Backoff     time.Duration `koanf:"backoff"`
}

// Sync controls automatic periodic syncing of favorites.
type Sync struct {
	// Interval between automatic syncs. Set to 0 to disable.
	Interval time.Duration `koanf:"interval"`
	Retry    RetryPolicy   `koanf:"retry"`
}

// Favorites selects which favorite item types a sync pulls.
type Favorites struct {
	Albums    bool `koanf:"albums"`
	Artists   bool `koanf:"artists"`
	Playlists bool `koanf:"playlists"`
	Tracks    bool `koanf:"tracks"`
}

// Download controls the batched download stage.
type Download struct {
	Concurrency     int           `koanf:"concurrency"`
	InterBatchDelay time.Duration `koanf:"inter_batch_delay"`
	// Auto triggers the download stage automatically after each sync.
	Auto      bool        `koanf:"auto"`
	Favorites Favorites   `koanf:"favorites"`
	Retry     RetryPolicy `koanf:"retry"`
}

// Tag controls the post-download tagging stage.
type Tag struct {
	// Auto triggers tagging automatically after each download batch.
	Auto bool `koanf:"auto"`
	// Concurrency is the number of files tagged in parallel.
	Concurrency int `koanf:"concurrency"`
}

// Convert controls the optional post-tag ffmpeg conversion stage.
type Convert struct {
	// Enabled controls whether conversion runs at all.
	Enabled bool `koanf:"enabled"`
	// Auto triggers conversion automatically after each download run.
	Auto bool `koanf:"auto"`
	// Dest is the directory where converted files are written.
	// Defaults to music_dir if empty (in-place, different extension).
	Dest string `koanf:"dest"`
	// Concurrency is the number of files converted in parallel.
	Concurrency int `koanf:"concurrency"`
	// Format is the target format name, e.g. "opus", "mp3".
	Format string `koanf:"format"`
	// FFmpegArgs is the ffmpeg command template for the target format.
	// Use $source and $dest as placeholders.
	// Defaults to a sensible opus command.
	FFmpegArgs string `koanf:"ffmpeg_args"`
}

// Notifications controls webhook notifications for pipeline events.
type Notifications struct {
	// WebhookURL is the endpoint to POST events to. Empty disables notifications.
	// Override via DEEZNT_WEBHOOK_URL env var.
	WebhookURL string `koanf:"webhook_url"`
	// AuthHeader is the HTTP header name for authentication, e.g. "Authorization".
	// Override via DEEZNT_WEBHOOK_AUTH_HEADER env var.
	AuthHeader string `koanf:"auth_header"`
	// AuthValue is the value sent in the auth header, e.g. "Bearer token123".
	// Override via DEEZNT_WEBHOOK_AUTH_VALUE env var (preferred for secrets).
	AuthValue string `koanf:"auth_value"`
	// On controls which events trigger a notification.
	On NotificationEvents `koanf:"on"`
}

// NotificationEvents selects which pipeline events fire a webhook.
type NotificationEvents struct {
	DownloadsStarted  bool `koanf:"downloads_started"`
	DownloadsFinished bool `koanf:"downloads_finished"`
	DownloadsFailed   bool `koanf:"downloads_failed"`
	ConvertsFinished  bool `koanf:"converts_finished"`
	ConvertsFailed    bool `koanf:"converts_failed"`
}

// RateLimit controls how the downloader backs off to avoid a ban.
type RateLimit struct {
	// Cooldown is the base backoff after a rate-limit hit (grows exponentially).
	Cooldown time.Duration `koanf:"cooldown"`
	// MaxHits within Window trips a hard stop of the download stage.
	MaxHits int           `koanf:"max_hits"`
	Window  time.Duration `koanf:"window"`
	// Backoff is the flat wait duration applied to all stages when rate limited.
	Backoff time.Duration `koanf:"backoff"`
}

// Tags controls baseline tagging and file naming.
type Tags struct {
	Fields         []string `koanf:"fields"`
	NamingTemplate string   `koanf:"naming_template"`
}

const (
	EnvPrefix        = "DEEZNT_"
	EnvARL           = "DEEZNT_ARL"
	EnvFixtureAlbums = "DEEZNT_FIXTURE_ALBUMS"
	EnvWebhookURL    = "DEEZNT_WEBHOOK_URL"
	EnvWebhookHeader = "DEEZNT_WEBHOOK_AUTH_HEADER"
	EnvWebhookValue  = "DEEZNT_WEBHOOK_AUTH_VALUE"
)

// Defaults returns the built-in configuration.
func Defaults() map[string]any {
	return map[string]any{
		"deezer.format_priority": []string{"FLAC", "MP3_320", "MP3_128"},

		"paths.music_dir":   "./music",
		"paths.db_path":     "./deeznt.db",
		"paths.socket_path": "./deeznt.sock",

		"sync.interval":           "0s",
		"sync.retry.max_attempts": 3,
		"sync.retry.backoff":      "10s",

		"download.concurrency":         3,
		"download.inter_batch_delay":   "0s",
		"download.auto":                false,
		"download.favorites.albums":    false,
		"download.favorites.artists":   false,
		"download.favorites.playlists": false,
		"download.favorites.tracks":    true,
		"download.retry.max_attempts":  3,
		"download.retry.backoff":       "5s",

		"tag.auto":        true,
		"tag.concurrency": 3,

		"convert.enabled":     false,
		"convert.auto":        false,
		"convert.dest":        "",
		"convert.concurrency": 2,
		"convert.format":      "opus",
		"convert.ffmpeg_args": "ffmpeg -i $source -y -vn -c:a libopus -b:a 160k -vbr on -compression_level 10 $dest",

		"notifications.webhook_url":           "",
		"notifications.auth_header":            "",
		"notifications.auth_value":             "",
		"notifications.on.downloads_started":  false,
		"notifications.on.downloads_finished": true,
		"notifications.on.downloads_failed":   true,
		"notifications.on.converts_finished":  false,
		"notifications.on.converts_failed":    true,

		"ratelimit.cooldown": "30s",
		"ratelimit.max_hits": 5,
		"ratelimit.window":   "5m",
		"ratelimit.backoff":  "10m",

		"tags.fields": []string{
			"title", "artist", "albumartist", "album",
			"tracknumber", "totaltracks", "discnumber", "disctotal",
			"date", "genre", "label", "composer", "isrc", "barcode",
			"copyright", "bpm", "replaygain", "comment", "lyrics", "cover",
		},
		"tags.naming_template": `{{.AlbumArtist}}/{{.Album}}{{if .Year}} ({{.Year}}){{end}}/{{if .MultiDisc}}{{.Disc}}-{{end}}{{printf "%02d" .Track}} {{.Title}}.{{.Ext}}`,

		"posthooks": []string{},
	}
}

// Load resolves configuration from defaults, then the TOML file at path (if it
// exists), then environment variables.
func Load(path string) (*Config, error) {
	k := koanf.New(".")

	if err := k.Load(confmap.Provider(Defaults(), "."), nil); err != nil {
		return nil, fmt.Errorf("load defaults: %w", err)
	}

	if path != "" {
		if _, err := os.Stat(path); err == nil {
			if err := k.Load(file.Provider(path), toml.Parser()); err != nil {
				return nil, fmt.Errorf("load config file %q: %w", path, err)
			}
		} else if !os.IsNotExist(err) {
			return nil, fmt.Errorf("stat config file %q: %w", path, err)
		}
	}

	// Environment overrides: DEEZNT_DOWNLOAD_CONCURRENCY -> download.concurrency.
	err := k.Load(env.Provider(EnvPrefix, ".", func(s string) string {
		return strings.ReplaceAll(strings.ToLower(strings.TrimPrefix(s, EnvPrefix)), "_", ".")
	}), nil)
	if err != nil {
		return nil, fmt.Errorf("load env: %w", err)
	}

	var cfg Config
	if err := k.UnmarshalWithConf("", &cfg, koanf.UnmarshalConf{
		Tag: "koanf",
		DecoderConfig: &mapstructure.DecoderConfig{
			DecodeHook:       mapstructure.StringToTimeDurationHookFunc(),
			WeaklyTypedInput: true,
			Result:           &cfg,
		},
	}); err != nil {
		return nil, fmt.Errorf("unmarshal config: %w", err)
	}

	if arl := os.Getenv(EnvARL); arl != "" {
		cfg.Deezer.ARL = arl
	}

	if raw := os.Getenv(EnvFixtureAlbums); raw != "" {
		for _, s := range strings.Split(raw, ",") {
			s = strings.TrimSpace(s)
			if s == "" {
				continue
			}
			id, err := strconv.ParseInt(s, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("%s: invalid album id %q", EnvFixtureAlbums, s)
			}
			cfg.FixtureAlbums = append(cfg.FixtureAlbums, id)
		}
	}

	// Dedicated webhook env vars — these take precedence over config file values.
	if v := os.Getenv(EnvWebhookURL); v != "" {
		cfg.Notifications.WebhookURL = v
	}
	if v := os.Getenv(EnvWebhookHeader); v != "" {
		cfg.Notifications.AuthHeader = v
	}
	if v := os.Getenv(EnvWebhookValue); v != "" {
		cfg.Notifications.AuthValue = v
	}

	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// Validate checks required fields and value ranges.
func (c *Config) Validate() error {
	if c.Paths.MusicDir == "" {
		return fmt.Errorf("paths.music_dir must be set")
	}
	if c.Paths.DBPath == "" {
		return fmt.Errorf("paths.db_path must be set")
	}
	if c.Paths.SocketPath == "" {
		return fmt.Errorf("paths.socket_path must be set")
	}
	if c.Download.Concurrency < 1 {
		return fmt.Errorf("download.concurrency must be >= 1")
	}
	if c.Tag.Concurrency < 1 {
		return fmt.Errorf("tag.concurrency must be >= 1")
	}
	if len(c.Deezer.FormatPriority) == 0 {
		return fmt.Errorf("deezer.format_priority must list at least one format")
	}
	for _, f := range c.Deezer.FormatPriority {
		if !validFormat(f) {
			return fmt.Errorf("deezer.format_priority: unknown format %q", f)
		}
	}
	if c.Convert.Enabled && c.Convert.Concurrency < 1 {
		return fmt.Errorf("convert.concurrency must be >= 1")
	}
	if c.Tags.NamingTemplate == "" {
		return fmt.Errorf("tags.naming_template must be set")
	}
	return nil
}

func validFormat(f string) bool {
	switch f {
	case "FLAC", "MP3_320", "MP3_128", "MP3_64", "AAC_64":
		return true
	default:
		return false
	}
}
