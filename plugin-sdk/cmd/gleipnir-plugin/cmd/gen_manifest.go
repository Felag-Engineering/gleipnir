package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/felag-engineering/gleipnir/plugin-sdk/manifest"
)

// NewGenManifestCmd returns the cobra.Command for the `gen-manifest` subcommand.
func NewGenManifestCmd() *cobra.Command {
	var binary, out string

	cmd := &cobra.Command{
		Use:   "gen-manifest",
		Short: "Generate deterministic manifest YAML from a plugin binary",
		Long: `Invoke <binary> --emit-manifest, parse the JSON output, and write
deterministic canonical YAML (sorted keys, 2-space indent).

The canonical YAML is the artifact committed to version control and hashed for
signing. Re-running gen-manifest for the same Go declarations produces
byte-identical output.

Flags:
  --binary   path to the plugin binary (required)
  --out      output file path; defaults to stdout when omitted`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runGenManifest(binary, out, cmd)
		},
	}

	cmd.Flags().StringVar(&binary, "binary", "", "path to the plugin binary (required)")
	cmd.Flags().StringVar(&out, "out", "", "output file path (default: stdout)")
	_ = cmd.MarkFlagRequired("binary")

	return cmd
}

// runGenManifest invokes the binary with --emit-manifest, parses the JSON
// output, and writes canonical YAML to the output destination. Extracted for
// testability.
func runGenManifest(binary, out string, cmd *cobra.Command) error {
	raw, err := runBinary(binary, []string{"--emit-manifest"})
	if err != nil {
		return fmt.Errorf("gen-manifest: invoke binary: %w", err)
	}

	canonicalYAML, err := manifest.Canonicalize(raw)
	if err != nil {
		return fmt.Errorf("gen-manifest: canonicalise output: %w", err)
	}

	if out == "" {
		_, err = cmd.OutOrStdout().Write(canonicalYAML)
		return err
	}

	if err := os.WriteFile(out, canonicalYAML, 0o644); err != nil {
		return fmt.Errorf("gen-manifest: write %s: %w", out, err)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "wrote manifest to %s\n", out)
	return nil
}
