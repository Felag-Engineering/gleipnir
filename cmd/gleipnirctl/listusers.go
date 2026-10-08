package main

import (
	"context"
	"os"

	"github.com/spf13/cobra"
)

func newListUsersCmd() *cobra.Command {
	var dbPath string

	cmd := &cobra.Command{
		Use:   "list-users",
		Short: "List all Gleipnir users directly from the database",
		Long: `Prints a table of every user with username, roles (comma-separated),
creation time, and status (active or deactivated), ordered by username.

Password hashes and other credential material are never read or printed. If
there are no users, only the header and a "no users" line are printed.

The server does not need to be stopped — this command only reads.`,
		SilenceUsage: true,
		Args:         cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			code := ListUsers(context.Background(), dbPath, cmd.OutOrStdout(), cmd.ErrOrStderr())
			if code != 0 {
				os.Exit(code)
			}
			return nil
		},
	}

	envDBPath := os.Getenv("GLEIPNIR_DB_PATH")
	if envDBPath == "" {
		envDBPath = defaultDBPath
	}

	cmd.Flags().StringVar(&dbPath, "db-path", envDBPath, "path to the SQLite database file")

	return cmd
}
