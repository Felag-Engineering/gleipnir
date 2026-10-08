package main

import (
	"context"
	"os"

	"github.com/spf13/cobra"
)

func newCheckCmd() *cobra.Command {
	var dbPath string

	cmd := &cobra.Command{
		Use:   "check",
		Short: "Run read-only health checks on the database and encryption key",
		Long: `Prints PASS, WARN, FAIL or SKIP for each of:

  - database reachable
  - schema fully migrated (no pending migrations)
  - GLEIPNIR_ENCRYPTION_KEY present and a valid 32-byte key
  - the key decrypts every stored secret
  - at least one active admin user (WARN if none)

Exits 0 when no check failed (warnings do not fail the run), 1 otherwise.
Nothing is written: the database is opened read-only and never migrated, so the
server does not need to be stopped.`,
		SilenceUsage: true,
		Args:         cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			code := Check(context.Background(), dbPath, os.Getenv(encryptionKeyEnv), cmd.OutOrStdout())
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
