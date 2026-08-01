package cmd

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"deebeets/internal/credentials"
	"deebeets/internal/deezer"
	"deebeets/internal/store"
)

func loginCmd() *cobra.Command {
	var noVerify bool
	c := &cobra.Command{
		Use:   "login [ARL]",
		Short: "Save your Deezer ARL (stored encrypted in the database)",
		Long: `Saves the Deezer ARL cookie so the daemon can authenticate without
putting the secret in the config file.

The ARL is encrypted with XChaCha20-Poly1305 and stored in the SQLite database.
The encryption key lives in a separate file (.deebeets.key, next to the database),
so the database alone cannot be used to recover the ARL.

If ARL is not supplied as an argument, it is read interactively without echo.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}

			var arl string
			if len(args) == 1 {
				arl = args[0]
			} else {
				fmt.Fprint(os.Stderr, "ARL: ")
				raw, err := term.ReadPassword(int(os.Stdin.Fd()))
				fmt.Fprintln(os.Stderr)
				if err != nil {
					return fmt.Errorf("read ARL: %w", err)
				}
				arl = string(raw)
			}

			if arl == "" {
				return fmt.Errorf("ARL must not be empty")
			}

			if !noVerify {
				dz, err := deezer.New(arl)
				if err != nil {
					return err
				}
				if err := dz.Login(context.Background()); err != nil {
					return fmt.Errorf("ARL validation failed: %w", err)
				}
				fmt.Fprintf(os.Stderr, "verified: user_id=%d lossless=%v hq=%v\n",
					dz.UserID(), dz.CanStreamLossless(), dz.CanStreamHQ())
			}

			st, err := store.Open(cfg.Paths.DBPath)
			if err != nil {
				return err
			}
			defer st.Close()

			if err := credentials.SetARL(context.Background(), st, arl); err != nil {
				return fmt.Errorf("save ARL: %w", err)
			}
			fmt.Fprintln(cmdOut, "ARL saved (encrypted)")
			return nil
		},
	}
	c.Flags().BoolVar(&noVerify, "no-verify", false, "skip Deezer API verification before saving")
	return c
}
