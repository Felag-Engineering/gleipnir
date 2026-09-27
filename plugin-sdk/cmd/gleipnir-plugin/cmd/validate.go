package cmd

import (
	"bytes"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/felag-engineering/gleipnir/plugin-sdk/manifest"
	manifestv2 "github.com/felag-engineering/gleipnir/plugin-sdk/manifestv2"
)

// NewValidateCmd returns the cobra.Command for the `validate` subcommand.
func NewValidateCmd() *cobra.Command {
	var binary, manifestPath string

	cmd := &cobra.Command{
		Use:   "validate",
		Short: "Validate a plugin manifest",
		Long: `For a v1 (gRPC-subprocess) manifest: invoke <binary> --emit-manifest,
canonicalise both the binary's output and the on-disk manifest.yaml, then
byte-compare them. A diff is printed to stderr when they diverge.

For a v2 (containerized) manifest: parse and validate it against every
manifestv2 rule (profiles, egress, resources, auth, tier2) — there is no
binary to compare against, since a v2 plugin is a container image and the
manifest is the sole source of truth reviewed at install time.

Exit code is 1 on mismatch/invalid or error, 0 on success.

Run 'gleipnir-plugin gen-manifest' to regenerate a v1 manifest.yaml from the
binary (v1 only; see 'gen-manifest --help').`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runValidate(binary, manifestPath, cmd)
		},
	}

	cmd.Flags().StringVar(&binary, "binary", "", "path to the plugin binary (required for a v1 manifest)")
	cmd.Flags().StringVar(&manifestPath, "manifest", "manifest.yaml", "path to manifest.yaml")

	return cmd
}

// runValidate implements the validate subcommand logic. Extracted for
// testability.
func runValidate(binary, manifestPath string, cmd *cobra.Command) error {
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		return fmt.Errorf("validate: read %s: %w", manifestPath, err)
	}

	if manifestv2.IsV2(raw) {
		return runValidateV2(raw, manifestPath, cmd)
	}

	if binary == "" {
		return fmt.Errorf("validate: --binary is required for a v1 manifest")
	}
	return runValidateV1(binary, manifestPath, cmd)
}

// runValidateV1 checks that manifest.yaml matches the binary's own
// --emit-manifest declarations.
func runValidateV1(binary, manifestPath string, cmd *cobra.Command) error {
	canonicalDisk, err := loadCanonicalManifest(manifestPath)
	if err != nil {
		return fmt.Errorf("validate: %w", err)
	}

	// Invoke the binary to get its manifest.
	raw, err := runBinary(binary, []string{"--emit-manifest"})
	if err != nil {
		return fmt.Errorf("validate: invoke binary: %w", err)
	}

	canonicalBinary, err := manifest.Canonicalize(raw)
	if err != nil {
		return fmt.Errorf("validate: canonicalise binary output: %w", err)
	}

	if bytes.Equal(canonicalDisk, canonicalBinary) {
		fmt.Fprintln(cmd.OutOrStdout(), "OK: manifest matches binary")
		return nil
	}

	// Print a simple line diff to stderr and return an error.
	diff := lineDiff(string(canonicalDisk), string(canonicalBinary))
	fmt.Fprintf(cmd.ErrOrStderr(), "manifest drift detected (run gen-manifest to update):\n%s\n", diff)
	return fmt.Errorf("manifest does not match binary output")
}

// runValidateV2 parses and validates a v2 manifest against every
// manifestv2.Validate rule.
func runValidateV2(raw []byte, manifestPath string, cmd *cobra.Command) error {
	if _, err := manifestv2.Parse(raw); err != nil {
		return fmt.Errorf("validate: %s: %w", manifestPath, err)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "OK: %s is a valid schema_version %s manifest\n", manifestPath, manifestv2.SchemaVersion)
	return nil
}

// lineDiff produces a simple unified-style diff between two multi-line strings.
// It uses no external library — just two passes over the line slices.
func lineDiff(a, b string) string {
	aLines := strings.Split(a, "\n")
	bLines := strings.Split(b, "\n")

	var sb strings.Builder
	sb.WriteString("--- manifest.yaml (on disk)\n")
	sb.WriteString("+++ manifest.yaml (from binary)\n")

	// Longest common subsequence would be expensive; use a simple contextual
	// approach: output every line from a as "-" if it's not in b at the same
	// position, and every line from b as "+" if it differs.
	maxLen := len(aLines)
	if len(bLines) > maxLen {
		maxLen = len(bLines)
	}
	for i := 0; i < maxLen; i++ {
		aLine := lineAt(aLines, i)
		bLine := lineAt(bLines, i)
		if aLine == bLine {
			sb.WriteString(" ")
			sb.WriteString(aLine)
			sb.WriteString("\n")
		} else {
			if aLine != "" {
				sb.WriteString("-")
				sb.WriteString(aLine)
				sb.WriteString("\n")
			}
			if bLine != "" {
				sb.WriteString("+")
				sb.WriteString(bLine)
				sb.WriteString("\n")
			}
		}
	}
	return sb.String()
}

func lineAt(lines []string, i int) string {
	if i < len(lines) {
		return lines[i]
	}
	return ""
}
