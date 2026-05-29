package cmd

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/felag-engineering/gleipnir/plugin-sdk/manifest"
)

// NewValidateCmd returns the cobra.Command for the `validate` subcommand.
func NewValidateCmd() *cobra.Command {
	var binary, manifestPath string

	cmd := &cobra.Command{
		Use:   "validate",
		Short: "Validate that a manifest matches the binary's emit-manifest output",
		Long: `Invoke <binary> --emit-manifest, canonicalise both the binary's output and
the on-disk manifest.yaml, then byte-compare them.

A diff is printed to stderr when they diverge. Exit code is 1 on mismatch or
error, 0 on success.

Run 'gleipnir-plugin gen-manifest' to regenerate manifest.yaml from the binary.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runValidate(binary, manifestPath, cmd)
		},
	}

	cmd.Flags().StringVar(&binary, "binary", "", "path to the plugin binary (required)")
	cmd.Flags().StringVar(&manifestPath, "manifest", "manifest.yaml", "path to manifest.yaml")
	_ = cmd.MarkFlagRequired("binary")

	return cmd
}

// runValidate implements the validate subcommand logic. Extracted for
// testability.
func runValidate(binary, manifestPath string, cmd *cobra.Command) error {
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
