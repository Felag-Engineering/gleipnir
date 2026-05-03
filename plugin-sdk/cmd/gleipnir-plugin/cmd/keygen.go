package cmd

import (
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/felag-engineering/gleipnir/plugin-sdk/signing"
)

// NewKeygenCmd returns the cobra.Command for the `keygen` subcommand.
func NewKeygenCmd() *cobra.Command {
	var outDir, name, kdf string
	var force, passStdin, unencrypted bool

	defaultDir := filepath.Join(mustUserHomeDir(), ".config", "gleipnir-plugin", "keys")

	cmd := &cobra.Command{
		Use:   "keygen",
		Short: "Generate a Minisign Ed25519 signing keypair",
		Long: `Generate a new Minisign-compatible Ed25519 signing keypair.

The secret key is written to <out-dir>/<name>.key (mode 0600) and the public
key to <out-dir>/<name>.pub (mode 0644). The public key block is also printed
to stdout for copy/paste into a policy or install configuration.

The private key is encrypted with a passphrase by default. Use --unencrypted
only for testing (emits a warning). Use --passphrase-stdin or the env var
GLEIPNIR_PLUGIN_SIGNING_KEY_PASSPHRASE for CI.

KDF choices:
  scrypt  (default) — broadest compatibility; works with all minisign versions
  argon2  — requires upstream minisign >= 0.11 (released 2023)`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runKeygen(cmd, outDir, name, kdf, force, passStdin, unencrypted)
		},
	}

	cmd.Flags().StringVar(&outDir, "out-dir", defaultDir, "directory for key files")
	cmd.Flags().StringVar(&name, "name", "signing", "base name for key files (<name>.key, <name>.pub)")
	cmd.Flags().StringVar(&kdf, "kdf", "scrypt", "KDF for passphrase encryption: scrypt (default) or argon2")
	cmd.Flags().BoolVar(&force, "force", false, "overwrite existing key files")
	cmd.Flags().BoolVar(&passStdin, "passphrase-stdin", false, "read passphrase from stdin (for CI)")
	cmd.Flags().BoolVar(&unencrypted, "unencrypted", false, "skip passphrase encryption (testing only)")

	return cmd
}

func runKeygen(cmd *cobra.Command, outDir, name, kdfFlag string, force, passStdin, unencrypted bool) error {
	keyPath := filepath.Join(outDir, name+".key")
	pubPath := filepath.Join(outDir, name+".pub")

	if !force {
		if _, err := os.Stat(keyPath); err == nil {
			return fmt.Errorf("keygen: %s already exists (use --force to overwrite)", keyPath)
		}
	}

	if err := os.MkdirAll(outDir, 0o700); err != nil {
		return fmt.Errorf("keygen: create output directory: %w", err)
	}

	pk, sk, err := signing.GenerateKeypair(nil)
	if err != nil {
		return fmt.Errorf("keygen: %w", err)
	}

	if unencrypted {
		slog.Warn("generating unencrypted signing key — not recommended outside testing")
	} else {
		kdfAlg, err := parseKDFFlag(kdfFlag)
		if err != nil {
			return fmt.Errorf("keygen: %w", err)
		}

		passphrase, err := readNewPassphrase(passStdin, cmd.InOrStdin())
		if err != nil {
			return fmt.Errorf("keygen: %w", err)
		}

		sk, err = signing.EncryptSecretKey(sk, passphrase, kdfAlg)
		if err != nil {
			return fmt.Errorf("keygen: encrypt key: %w", err)
		}
	}

	keyID := hex.EncodeToString(pk.KeyID[:])
	keyComment := fmt.Sprintf("gleipnir-plugin secret key %s", keyID)
	pubComment := fmt.Sprintf("gleipnir-plugin public key %s", keyID)

	keyData := signing.MarshalSecretKey(sk, keyComment)
	pubData := signing.MarshalPublicKey(pk, pubComment)

	if err := os.WriteFile(keyPath, keyData, 0o600); err != nil {
		return fmt.Errorf("keygen: write secret key: %w", err)
	}
	if err := os.WriteFile(pubPath, pubData, 0o644); err != nil {
		return fmt.Errorf("keygen: write public key: %w", err)
	}

	fmt.Fprintf(cmd.ErrOrStderr(), "wrote secret key: %s\n", keyPath)
	fmt.Fprintf(cmd.ErrOrStderr(), "wrote public key: %s\n", pubPath)
	fmt.Fprintln(cmd.OutOrStdout(), "")
	fmt.Fprintln(cmd.OutOrStdout(), "Public key (copy this for host pinning):")
	fmt.Fprint(cmd.OutOrStdout(), string(pubData))
	return nil
}

// readNewPassphrase reads a new passphrase from stdin (for CI) or interactively
// with confirmation.
func readNewPassphrase(passStdin bool, stdin io.Reader) ([]byte, error) {
	if v := os.Getenv("GLEIPNIR_PLUGIN_SIGNING_KEY_PASSPHRASE"); v != "" {
		return []byte(v), nil
	}

	if passStdin {
		data, err := io.ReadAll(stdin)
		if err != nil {
			return nil, fmt.Errorf("read passphrase from stdin: %w", err)
		}
		// Strip trailing newline that shells typically add.
		for len(data) > 0 && (data[len(data)-1] == '\n' || data[len(data)-1] == '\r') {
			data = data[:len(data)-1]
		}
		return data, nil
	}

	fd := int(os.Stdin.Fd())
	if term.IsTerminal(fd) {
		return promptPassphraseWithConfirm()
	}

	return nil, fmt.Errorf("no passphrase available; use --passphrase-stdin or set GLEIPNIR_PLUGIN_SIGNING_KEY_PASSPHRASE")
}

// parseKDFFlag converts the --kdf flag string to the signing package constant.
func parseKDFFlag(kdfFlag string) ([2]byte, error) {
	switch kdfFlag {
	case "scrypt", "":
		return signing.KDFAlgScrypt, nil
	case "argon2":
		return signing.KDFAlgArgon2id, nil
	default:
		return [2]byte{}, fmt.Errorf("unknown --kdf %q: choose scrypt (default) or argon2", kdfFlag)
	}
}

func mustUserHomeDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return home
}
