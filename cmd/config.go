package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

func configCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "config",
		Short: "Inspect configuration",
	}
	c.AddCommand(configValidateCmd(), configPrintCmd())
	return c
}

func configValidateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "validate",
		Short: "Validate the config file",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			fmt.Printf("config OK (music_dir=%s, db=%s, socket=%s)\n",
				cfg.Paths.MusicDir, cfg.Paths.DBPath, cfg.Paths.SocketPath)
			return nil
		},
	}
}

func configPrintCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "print",
		Short: "Print the resolved configuration (ARL redacted)",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			arl := "(unset)"
			if cfg.Deezer.ARL != "" {
				arl = "(set, redacted)"
			}
			fmt.Printf("deezer.arl:             %s\n", arl)
			fmt.Printf("deezer.format_priority: %v\n", cfg.Deezer.FormatPriority)
			fmt.Printf("paths.music_dir:        %s\n", cfg.Paths.MusicDir)
			fmt.Printf("paths.db_path:          %s\n", cfg.Paths.DBPath)
			fmt.Printf("paths.socket_path:      %s\n", cfg.Paths.SocketPath)
			fmt.Printf("download.concurrency:   %d\n", cfg.Download.Concurrency)
			fmt.Printf("download.favorites:     albums=%v artists=%v playlists=%v tracks=%v\n",
				cfg.Download.Favorites.Albums, cfg.Download.Favorites.Artists,
				cfg.Download.Favorites.Playlists, cfg.Download.Favorites.Tracks)
			fmt.Printf("download.auto:          %v\n", cfg.Download.Auto)
			fmt.Printf("download.retry:         max=%d backoff=%s\n",
				cfg.Download.Retry.MaxAttempts, cfg.Download.Retry.Backoff)
			fmt.Printf("sync.interval:          %s\n", cfg.Sync.Interval)
			fmt.Printf("sync.retry:             max=%d backoff=%s\n",
				cfg.Sync.Retry.MaxAttempts, cfg.Sync.Retry.Backoff)
			fmt.Printf("convert.enabled:        %v\n", cfg.Convert.Enabled)
			fmt.Printf("convert.auto:           %v\n", cfg.Convert.Auto)
			fmt.Printf("convert.dest:           %s\n", cfg.Convert.Dest)
			fmt.Printf("convert.format:         %s\n", cfg.Convert.Format)
			fmt.Printf("convert.concurrency:    %d\n", cfg.Convert.Concurrency)
			fmt.Printf("ratelimit:              cooldown=%s max_hits=%d window=%s backoff=%s\n",
				cfg.RateLimit.Cooldown, cfg.RateLimit.MaxHits, cfg.RateLimit.Window, cfg.RateLimit.Backoff)
			return nil
		},
	}
}
