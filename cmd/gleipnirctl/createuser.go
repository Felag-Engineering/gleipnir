package main

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

func newCreateUserCmd() *cobra.Command {
	var password, role, dbPath string

	cmd := &cobra.Command{
		Use:   "create-user <username>",
		Short: "Create a new Gleipnir user directly in the database",
		Long: `Creates a new Gleipnir user with the specified role, writing the account
directly to the database. This is useful for bootstrapping a second admin account
or automating user provisioning in CI/CD without going through the web UI.

The server does not need to be stopped — this command performs a short INSERT
that resolves any write contention on its own via SQLite's busy retry.

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

			code := CreateUser(context.Background(), dbPath, username, role, password, out, errOut)
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

	cmd.Flags().StringVar(&role, "role", "operator", "role to assign (admin, operator, approver, auditor)")
	cmd.Flags().StringVar(&password, "password", "", "password for the new user; if omitted a random password is generated and printed to stdout")
	cmd.Flags().StringVar(&dbPath, "db-path", envDBPath, "path to the SQLite database file")

	return cmd
}
