// Package config loads deebeets configuration from a TOML file, environment
// variables (prefixed DEEBEETS_) and built-in defaults, in that increasing
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
	Deezer    Deezer    `koanf:"deezer"`
	Paths     Paths     `koanf:"paths"`
	Sync      Sync      `koanf:"sync"`
	Download  Download  `koanf:"download"`
	Import    Import    `koanf:"import"`
	RateLimit RateLimit `koanf:"ratelimit"`
	Tags      Tags      `koanf:"tags"`
	Beets     Beets     `koanf:"beets"`
	PostHooks []string  `koanf:"posthooks"`

	// FixtureAlbums is populated from DEEBEETS_FIXTURE_ALBUMS (comma-separated
	// album IDs). When set the daemon's sync uses this list instead of fetching
	// real Deezer favorites, exercising the full pipeline on a fixed set.
	FixtureAlbums []int64 `koanf:"-"`
}

// Deezer holds credentials and format preferences.
type Deezer struct {
	ARL            string   `koanf:"arl"`
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
	Auto      bool      `koanf:"auto"`
	Favorites Favorites `koanf:"favorites"`
	Retry     RetryPolicy `koanf:"retry"`
}

// Import controls the post-download import stage.
type Import struct {
	// Auto triggers beet import against the full music_dir after each download run.
	Auto bool `koanf:"auto"`
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

// Beets controls the optional beets import stage.
type Beets struct {
	Enabled    bool     `koanf:"enabled"`
	Binary     string   `koanf:"binary"`
	ConfigPath string   `koanf:"config_path"`
	Args       []string `koanf:"args"`
	Concurrency int     `koanf:"concurrency"`
}

const (
	EnvPrefix       = "DEEBEETS_"
	EnvARL          = "DEEBEETS_ARL"
	EnvFixtureAlbums = "DEEBEETS_FIXTURE_ALBUMS"
)

// Defaults returns the built-in configuration.
func Defaults() map[string]any {
	return map[string]any{
		"deezer.arl":             "",
		"deezer.format_priority": []string{"FLAC", "MP3_320", "MP3_128"},

		"paths.music_dir":   "./music",
		"paths.db_path":     "./deebeets.db",
		"paths.socket_path": "./deebeets.sock",

		"sync.interval":            "0s",
		"sync.retry.max_attempts":  3,
		"sync.retry.backoff":       "10s",

		"download.concurrency":          3,
		"download.inter_batch_delay":    "0s",
		"download.auto":                 false,
		"download.favorites.albums":     false,
		"download.favorites.artists":    false,
		"download.favorites.playlists":  false,
		"download.favorites.tracks":     true,
		"download.retry.max_attempts":   3,
		"download.retry.backoff":        "5s",

		"import.auto": false,

		"ratelimit.cooldown": "30s",
		"ratelimit.max_hits": 5,
		"ratelimit.window":   "5m",
		"ratelimit.backoff":  "10m",

		"tags.fields": []string{
			"title", "artist", "albumartist", "album",
			"tracknumber", "totaltracks", "discnumber", "disctotal",
			"date", "genre", "composer", "isrc", "barcode",
			"copyright", "bpm", "replaygain", "comment", "lyrics", "cover",
		},
		"tags.naming_template": `{{.AlbumArtist}}/{{.Album}}{{if .Year}} ({{.Year}}){{end}}/{{if .MultiDisc}}{{.Disc}}-{{end}}{{printf "%02d" .Track}} {{.Title}}.{{.Ext}}`,

		"beets.enabled":      false,
		"beets.binary":       "beet",
		"beets.config_path":  "",
		"beets.args":         []string{"import", "-q"},
		"beets.concurrency":  1,

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

	// Environment overrides: DEEBEETS_DOWNLOAD_CONCURRENCY -> download.concurrency.
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
	if len(c.Deezer.FormatPriority) == 0 {
		return fmt.Errorf("deezer.format_priority must list at least one format")
	}
	for _, f := range c.Deezer.FormatPriority {
		if !validFormat(f) {
			return fmt.Errorf("deezer.format_priority: unknown format %q", f)
		}
	}
	if c.Beets.Concurrency < 1 {
		return fmt.Errorf("beets.concurrency must be >= 1")
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
