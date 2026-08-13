package cmd

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"deeznt/internal/notify"
)

func notifyCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "notify",
		Short: "Manage webhook notifications",
	}
	c.AddCommand(notifyTestCmd())
	return c
}

func notifyTestCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "test",
		Short: "Send a test notification to the configured webhook",
		Long: "Sends a test event synchronously and reports success or failure.\n" +
			"Reads webhook_url, auth_header, and auth_value from config.\n" +
			"Env vars DEEZNT_WEBHOOK_URL, DEEZNT_WEBHOOK_AUTH_HEADER, " +
			"DEEZNT_WEBHOOK_AUTH_VALUE override config file values.",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			n := notify.NewSilent(cfg.Notifications)
			if err := n.SendTest(context.Background()); err != nil {
				return fmt.Errorf("test notification failed: %w", err)
			}
			fmt.Printf("test notification sent successfully to %s\n", cfg.Notifications.WebhookURL)
			return nil
		},
	}
}
