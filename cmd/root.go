// Package cmd implements the deebeets command-line interface.
package cmd

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strconv"

	"github.com/spf13/cobra"

	"deebeets/internal/config"
	"deebeets/internal/control"
)

var (
	cfgPath  string
	logLevel string
)

// cmdOut is the destination for command output (overridable in tests).
var cmdOut = os.Stdout

// Execute runs the root command.
func Execute() {
	if err := rootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func rootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "deebeets",
		Short:         "Sync and download your Deezer favorites for Navidrome",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	def := os.Getenv("DEEBEETS_CONFIG")
	if def == "" {
		def = "config.toml"
	}
	root.PersistentFlags().StringVar(&cfgPath, "config", def, "path to config file")
	root.PersistentFlags().StringVar(&logLevel, "log-level", "info", "log level: debug|info|warn|error")

	root.AddCommand(
		loginCmd(),
		daemonCmd(),
		syncCmd(),
		statusCmd(),
		listCmd(),
		downloadCmd(),
		redownloadCmd(),
		blocklistCmd(),
		convertCmd(),
		verifyCmd(),
		configCmd(),
	)
	return root
}

func loadConfig() (*config.Config, error) {
	return config.Load(cfgPath)
}

func newLogger() *slog.Logger {
	var lvl slog.Level
	switch logLevel {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lvl}))
}

// newClient builds a control client from config.
func newClient() (*control.Client, error) {
	cfg, err := loadConfig()
	if err != nil {
		return nil, err
	}
	return control.NewClient(cfg.Paths.SocketPath), nil
}

// requireClient returns a client and errors clearly if the daemon is down.
func requireClient() (*control.Client, error) {
	c, err := newClient()
	if err != nil {
		return nil, err
	}
	if !c.Available() {
		return nil, fmt.Errorf("daemon is not running (start it with `deebeets daemon`)")
	}
	return c, nil
}

func ctx() context.Context { return context.Background() }

// parseIDs converts positional args to int64 Deezer ids.
func parseIDs(args []string) ([]int64, error) {
	ids := make([]int64, 0, len(args))
	for _, a := range args {
		n, err := strconv.ParseInt(a, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid id %q", a)
		}
		ids = append(ids, n)
	}
	return ids, nil
}
