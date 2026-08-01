package cmd

import (
	"context"

	"github.com/spf13/cobra"

	"deebeets/internal/daemon"
)

func daemonCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "daemon",
		Short: "Run the long-lived downloader daemon and control socket",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			log := newLogger()
			d, err := daemon.New(cfg, log)
			if err != nil {
				return err
			}
			return d.Run(context.Background())
		},
	}
}
