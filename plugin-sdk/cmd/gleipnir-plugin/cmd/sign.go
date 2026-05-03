package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/felag-engineering/gleipnir/plugin-sdk/signing"
)

// NewSignCmd returns the cobra.Command for the `sign` subcommand.
func NewSignCmd() *cobra.Command {
	var flagKey, flagBinary, flagManifest, flagOut, flagTrustedComment string
	var flagKeyStdin bool

	defaultKey := defaultKeyPath()

	cmd := &cobra.Command{
		Use:   "sign",
		Short: "Sign a plugin binary + manifest with a Minisign key",
		Long: `Sign a plugin binary and manifest, producing a .minisig file.

The signed payload is sha256(binary) || sha256(manifest) per spec §5.2.

Key resolution order:
  1. --key-stdin (reads .key file content from stdin)
  2. env GLEIPNIR_PLUGIN_SIGNING_KEY (path or inline .key content)
  3. --key flag
  4. default: ~/.config/gleipnir-plugin/keys/signing.key

Passphrase resolution order:
  1. env GLEIPNIR_PLUGIN_SIGNING_KEY_PASSPHRASE
  2. interactive terminal prompt`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSign(cmd, flagKey, flagKeyStdin, flagBinary, flagManifest, flagOut, flagTrustedComment)
		},
	}

	cmd.Flags().StringVar(&flagKey, "key", defaultKey, "path to .key file")
	cmd.Flags().BoolVar(&flagKeyStdin, "key-stdin", false, "read .key file from stdin (CI mode)")
	cmd.Flags().StringVar(&flagBinary, "binary", "", "path to the plugin binary (required)")
	cmd.Flags().StringVar(&flagManifest, "manifest", "manifest.yaml", "path to manifest.yaml")
	cmd.Flags().StringVar(&flagOut, "out", "", "output .minisig path (default: <binary-basename>.minisig)")
	cmd.Flags().StringVar(&flagTrustedComment, "trusted-comment", "", "trusted comment (default: timestamp + manifest name/version)")

	_ = cmd.MarkFlagRequired("binary")

	return cmd
}

func runSign(cmd *cobra.Command, flagKey string, flagKeyStdin bool, binary, manifestPath, out, trustedComment string) error {
	stdin := cmd.InOrStdin()

	raw, keyID, _, err := loadSecretKey(flagKey, flagKeyStdin, stdin)
	if err != nil {
		return fmt.Errorf("sign: %w", err)
	}

	binaryData, err := os.ReadFile(binary)
	if err != nil {
		return fmt.Errorf("sign: read binary %s: %w", binary, err)
	}

	manifestData, err := os.ReadFile(manifestPath)
	if err != nil {
		return fmt.Errorf("sign: read manifest %s: %w", manifestPath, err)
	}

	if trustedComment == "" {
		trustedComment = buildDefaultTrustedComment(manifestData, binary)
	}

	payload := signing.PluginPayload(binaryData, manifestData)
	sig, err := signing.Sign(raw, keyID, payload, trustedComment)
	if err != nil {
		return fmt.Errorf("sign: %w", err)
	}

	outPath := out
	if outPath == "" {
		outPath = filepath.Base(binary) + ".minisig"
	}

	sigData := signing.MarshalSignature(sig, "signature from gleipnir-plugin sign")
	if err := os.WriteFile(outPath, sigData, 0o644); err != nil {
		return fmt.Errorf("sign: write %s: %w", outPath, err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "wrote signature: %s\n", outPath)
	return nil
}

// buildDefaultTrustedComment constructs the default trusted comment by parsing
// the manifest for name and version, combined with the current unix timestamp.
func buildDefaultTrustedComment(manifestData []byte, binaryPath string) string {
	m, err := parseManifestFromBytes(manifestData)
	if err == nil && m.Name != "" && m.Version != "" {
		return fmt.Sprintf("timestamp:%d\tname:%s\tversion:%s",
			time.Now().Unix(), m.Name, m.Version)
	}
	return fmt.Sprintf("timestamp:%d\tfile:%s", time.Now().Unix(), filepath.Base(binaryPath))
}
