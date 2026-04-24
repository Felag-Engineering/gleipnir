package main

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

func newResetPasswordCmd() *cobra.Command {
	var password, dbPath string

	cmd := &cobra.Command{
		Use:   "reset-password <username>",
		Short: "Reset a user's password directly in the database",
		Long: `Resets a Gleipnir user's password by writing a new bcrypt hash directly to the
database. This is the primary recovery path when an admin is locked out of the
web UI and no other admin account is available to reset the password through the
settings page.

The server does not need to be stopped — this command performs a single short
UPDATE that resolves any write contention on its own via SQLite's busy retry.

If --password is not provided, a cryptographically random 24-character password
is generated and printed to stdout before the confirmation line. Store it
immediately; it is printed only once.`,
		SilenceUsage: true,
		Args:         cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			username := args[0]
			out := cmd.OutOrStdout()
			errOut := cmd.ErrOrStderr()

			if password == "" {
				generated, err := generateRandomPassword()
				if err != nil {
					fmt.Fprintf(errOut, "error: %v\n", err)
					os.Exit(1)
				}
				password = generated
				fmt.Fprintf(out, "generated password: %s\n", password)
			}

			code := ResetPassword(context.Background(), dbPath, username, password, out, errOut)
			if code != 0 {
				os.Exit(code)
			}
			return nil
		},
	}

	// Resolve default DB path from env var, falling back to the hardcoded default.
	envDBPath := os.Getenv("GLEIPNIR_DB_PATH")
	if envDBPath == "" {
		envDBPath = defaultDBPath
	}

	cmd.Flags().StringVar(&password, "password", "", "new password; if omitted a random password is generated and printed to stdout")
	cmd.Flags().StringVar(&dbPath, "db-path", envDBPath, "path to the SQLite database file")

	return cmd
}
