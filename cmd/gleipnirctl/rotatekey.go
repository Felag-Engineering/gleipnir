package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/felag-engineering/gleipnir/internal/admin"
)

const defaultDBPath = "/data/gleipnir.db"

func newRotateKeyCmd() *cobra.Command {
	var oldKeyFlag, newKeyFlag, dbPath string
	var dryRun bool

	cmd := &cobra.Command{
		Use:   "rotate-key",
		Short: "Re-encrypt all at-rest secrets under a new encryption key",
		Long: `Re-encrypts every at-rest secret in the Gleipnir database (provider API keys,
OpenAI-compat keys, and per-policy webhook secrets) under a new AES-256-GCM key
in a single atomic transaction.

The server must be stopped before running this command. The command refuses to
proceed if another process is holding the database write lock.

Keys may be passed as 64-character hex strings or as base64. Use "-" for --old
or --new to read the key from stdin (one line each) — this prevents the key
from appearing in shell history or process listings.`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runRotateKey(cmd, oldKeyFlag, newKeyFlag, dbPath, dryRun)
		},
	}

	// Resolve default DB path from env var, falling back to the hardcoded default.
	envDBPath := os.Getenv("GLEIPNIR_DB_PATH")
	if envDBPath == "" {
		envDBPath = defaultDBPath
	}

	cmd.Flags().StringVar(&oldKeyFlag, "old", "", "current encryption key (hex or base64); use \"-\" to read from stdin")
	cmd.Flags().StringVar(&newKeyFlag, "new", "", "new encryption key (hex or base64); use \"-\" to read from stdin")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "validate decryption and simulate rotation without writing changes")
	cmd.Flags().StringVar(&dbPath, "db-path", envDBPath, "path to the SQLite database file")

	_ = cmd.MarkFlagRequired("old")
	_ = cmd.MarkFlagRequired("new")

	return cmd
}

func runRotateKey(cmd *cobra.Command, oldKeyFlag, newKeyFlag, dbPath string, dryRun bool) error {
	out := cmd.OutOrStdout()
	errOut := cmd.ErrOrStderr()

	// Record whether keys came in as literal values on the command line, so we
	// can warn about process-list leakage after both keys are resolved.
	oldFromFlag := oldKeyFlag != "-"
	newFromFlag := newKeyFlag != "-"

	// Read key material from stdin when either flag is "-". If both are "-",
	// we read old first, then new (one whitespace-trimmed line each).
	stdinReader := bufio.NewReader(cmd.InOrStdin())
	if oldKeyFlag == "-" {
		line, err := stdinReader.ReadString('\n')
		if err != nil && err != io.EOF {
			return fmt.Errorf("read --old from stdin: %w", err)
		}
		oldKeyFlag = strings.TrimSpace(line)
	}
	if newKeyFlag == "-" {
		line, err := stdinReader.ReadString('\n')
		if err != nil && err != io.EOF {
			return fmt.Errorf("read --new from stdin: %w", err)
		}
		newKeyFlag = strings.TrimSpace(line)
	}

	oldKey, err := admin.ParseEncryptionKey(oldKeyFlag)
	if err != nil {
		return fmt.Errorf("parse --old key: %w", err)
	}
	defer zeroKey(oldKey)

	newKey, err := admin.ParseEncryptionKey(newKeyFlag)
	if err != nil {
		return fmt.Errorf("parse --new key: %w", err)
	}
	defer zeroKey(newKey)

	if oldFromFlag || newFromFlag {
		fmt.Fprintln(errOut, "warning: keys passed via --old/--new flags are visible in process listings and shell history; prefer --old - and --new - to read from stdin")
	}

	if bytes.Equal(oldKey, newKey) {
		return fmt.Errorf("new key equals old key; nothing to do")
	}

	code := Rotate(context.Background(), dbPath, oldKey, newKey, dryRun, out, errOut)
	if code != 0 {
		// rotatekey.Rotate already wrote the error message to errOut; exit with
		// the appropriate code without adding another error layer.
		os.Exit(code)
	}
	return nil
}

// zeroKey overwrites b with zeros to reduce the window during which sensitive
// bytes are readable in memory (e.g. from a core dump). This is best-effort
// in Go because the GC may have already copied the slice.
func zeroKey(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
