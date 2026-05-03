package cmd

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

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

	canonicalYAML, err := jsonToCanonicalYAML(raw)
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

// jsonToCanonicalYAML parses JSON from --emit-manifest output into a Manifest
// and re-marshals it through the canonical YAML marshaller. The two-pass
// approach (JSON → Manifest struct → canonical YAML) guarantees that the
// output conforms to the canonical schema regardless of the JSON key order.
func jsonToCanonicalYAML(jsonData []byte) ([]byte, error) {
	// First unmarshal the JSON into a generic yaml.Node so we can pass it
	// through the canonical YAML path without losing *yaml.Node fields.
	//
	// Strategy: unmarshal JSON into a manifest.Manifest struct. Fields that
	// are *yaml.Node (like ConfigSchema, InputSchema) must come through as
	// JSON objects — yaml.v3 can decode them from JSON-encoded YAML nodes when
	// we go via an intermediate yaml.Node.

	// Convert JSON bytes to a yaml.Node by first parsing JSON into a generic
	// map, converting to YAML bytes, then decoding as yaml.Node.
	var generic interface{}
	if err := json.Unmarshal(jsonData, &generic); err != nil {
		return nil, fmt.Errorf("parse JSON: %w", err)
	}

	// Re-encode as YAML so we can unmarshal into manifest.Manifest.
	rawYAML, err := yaml.Marshal(generic)
	if err != nil {
		return nil, fmt.Errorf("re-marshal to YAML: %w", err)
	}

	var m manifest.Manifest
	if err := manifest.Unmarshal(rawYAML, &m); err != nil {
		return nil, fmt.Errorf("unmarshal manifest: %w", err)
	}

	return manifest.Marshal(&m)
}
