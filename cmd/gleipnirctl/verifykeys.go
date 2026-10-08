package main

import (
	"context"
	"os"

	"github.com/spf13/cobra"
)

func newVerifyKeysCmd() *cobra.Command {
	var dbPath string

	cmd := &cobra.Command{
		Use:   "verify-keys",
		Short: "Check that the encryption key decrypts every stored secret",
		Long: `Attempts to decrypt every at-rest secret in the database (the same set
rotate-key re-encrypts) using the key in GLEIPNIR_ENCRYPTION_KEY.

On success prints "verified N secrets OK" and exits 0. On failure lists each
row that did not decrypt as table.column plus a row identifier, and exits 1.
Neither plaintext nor ciphertext is ever printed.

The key is read from the environment only; there is deliberately no --key flag.
The database is opened read-only and is never migrated, so the server does not
need to be stopped.`,
		SilenceUsage: true,
		Args:         cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			key, err := encryptionKeyFromEnv()
			if err != nil {
				return err
			}
			defer zeroKey(key)

			code := VerifyKeys(context.Background(), dbPath, key, cmd.OutOrStdout(), cmd.ErrOrStderr())
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
