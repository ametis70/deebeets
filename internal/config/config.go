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

	"github.com/knadh/koanf/parsers/toml"
	"github.com/knadh/koanf/providers/confmap"
	"github.com/knadh/koanf/providers/env"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"
	"github.com/go-viper/mapstructure/v2"
)

// Config is the fully-resolved application configuration.
type Config struct {
	Deezer    Deezer    `koanf:"deezer"`
	Paths     Paths     `koanf:"paths"`
	Download  Download  `koanf:"download"`
	Retry     Retry     `koanf:"retry"`
	RateLimit RateLimit `koanf:"ratelimit"`
	Tags      Tags      `koanf:"tags"`
	Beets     Beets     `koanf:"beets"`
	PostHooks []string  `koanf:"posthooks"`

	// FixtureAlbums is populated from DEEBEETS_FIXTURE_ALBUMS (comma-separated
	// album IDs). When set the daemon's sync uses this list instead of fetching
	// real Deezer favorites, exercising the full pipeline on a controlled set.
	FixtureAlbums []int64 `koanf:"-"`
}

// Deezer holds credentials and format preferences.
type Deezer struct {
	// ARL is the Deezer auth cookie. Prefer the DEEBEETS_ARL env var over
	// storing it in the config file.
	ARL string `koanf:"arl"`
	// FormatPriority is tried in order; the first the account/track can serve wins.
	FormatPriority []string `koanf:"format_priority"`
}

// Paths holds on-disk locations.
type Paths struct {
	MusicDir   string `koanf:"music_dir"`
	DBPath     string `koanf:"db_path"`
	SocketPath string `koanf:"socket_path"`
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
	// Concurrency is the number of tracks downloaded in parallel (the "batch").
	Concurrency int `koanf:"concurrency"`
	// InterBatchDelay is an optional pause between batches.
	InterBatchDelay time.Duration `koanf:"inter_batch_delay"`
	// Favorites picks the default item types for `sync` when no flags are given.
	Favorites Favorites `koanf:"favorites"`
}

// Retry controls failed-download retry behaviour.
type Retry struct {
	// Mode is one of: immediate, deferred, both.
	Mode        string        `koanf:"mode"`
	MaxAttempts int           `koanf:"max_attempts"`
	Backoff     time.Duration `koanf:"backoff"`
}

// RateLimit controls how aggressively the downloader backs off to avoid a ban.
type RateLimit struct {
	// Cooldown is the base backoff after a rate-limit hit (grows exponentially).
	Cooldown time.Duration `koanf:"cooldown"`
	// MaxHits within Window triggers a hard stop of the download stage.
	MaxHits int           `koanf:"max_hits"`
	Window  time.Duration `koanf:"window"`
}

// Tags controls baseline tagging and file naming.
type Tags struct {
	// Fields is the set of tags deebeets writes before beets runs.
	Fields []string `koanf:"fields"`
	// NamingTemplate is a Go text/template producing the relative file path.
	NamingTemplate string `koanf:"naming_template"`
}

// Beets controls the optional beets import stage.
type Beets struct {
	Enabled    bool     `koanf:"enabled"`
	Binary     string   `koanf:"binary"`
	ConfigPath string   `koanf:"config_path"`
	Args       []string `koanf:"args"`
	// Concurrency for the import queue; keep at 1 to serialize beets safely.
	Concurrency int `koanf:"concurrency"`
}

const (
	// EnvPrefix is the prefix for environment overrides, e.g. DEEBEETS_DOWNLOAD_CONCURRENCY.
	EnvPrefix = "DEEBEETS_"
	// EnvARL is a dedicated override for the sensitive ARL value.
	EnvARL = "DEEBEETS_ARL"
	// EnvFixtureAlbums is a comma-separated list of album IDs used to run the
	// full pipeline against a fixed set instead of real Deezer favorites.
	EnvFixtureAlbums = "DEEBEETS_FIXTURE_ALBUMS"
)

// Defaults returns the built-in configuration, tuned to work out-of-the-box
// with Navidrome's default library conventions.
func Defaults() map[string]any {
	return map[string]any{
		"deezer.arl":             "",
		"deezer.format_priority": []string{"FLAC", "MP3_320", "MP3_128"},

		"paths.music_dir":   "./music",
		"paths.db_path":     "./deebeets.db",
		"paths.socket_path": "./deebeets.sock",

		"download.concurrency":       3,
		"download.inter_batch_delay": "0s",
		"download.favorites.albums":    false,
		"download.favorites.artists":   false,
		"download.favorites.playlists": false,
		"download.favorites.tracks":    true,

		"retry.mode":         "both",
		"retry.max_attempts": 3,
		"retry.backoff":      "5s",

		"ratelimit.cooldown": "30s",
		"ratelimit.max_hits": 5,
		"ratelimit.window":   "5m",

		"tags.fields": []string{
			"title", "artist", "albumartist", "album",
			"tracknumber", "totaltracks", "discnumber", "disctotal",
			"date", "genre", "composer", "isrc", "barcode",
			"copyright", "bpm", "replaygain", "comment", "lyrics", "cover",
		},
		"tags.naming_template": `{{.AlbumArtist}}/{{.Album}}{{if .Year}} ({{.Year}}){{end}}/{{if .MultiDisc}}{{.Disc}}-{{end}}{{printf "%02d" .Track}} {{.Title}}.{{.Ext}}`,

		"beets.enabled":     false,
		"beets.binary":      "beet",
		"beets.config_path": "",
		"beets.args":        []string{"import", "-q"},
		"beets.concurrency": 1,

		"posthooks": []string{},
	}
}

// Load resolves configuration from defaults, then the TOML file at path (if it
// exists), then environment variables. The dedicated DEEBEETS_ARL var wins over
// everything for the ARL.
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

	// DEEBEETS_ARL is a first-class override so the secret can stay out of files.
	// (The generic env loader above maps DEEBEETS_ARL -> "arl", not "deezer.arl",
	// so handle it explicitly here.)
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
	switch c.Retry.Mode {
	case "immediate", "deferred", "both":
	default:
		return fmt.Errorf("retry.mode must be one of immediate|deferred|both, got %q", c.Retry.Mode)
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
