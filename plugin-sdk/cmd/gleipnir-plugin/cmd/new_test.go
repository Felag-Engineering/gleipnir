package cmd_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/felag-engineering/gleipnir/plugin-sdk/cmd/gleipnir-plugin/cmd"
)

func TestNewCreatesScaffold(t *testing.T) {
	dir := t.TempDir()
	outDir := filepath.Join(dir, "myplugin")

	root := newTestRoot()
	root.AddCommand(cmd.NewNewCmd())

	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"new", "myplugin", "--kind", "tool", "--dir", outDir, "--module", "example.com/myplugin"})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}

	if !strings.Contains(out.String(), "myplugin") {
		t.Errorf("expected plugin name in output, got: %q", out.String())
	}

	// Verify key files exist.
	for _, f := range []string{"main.go", "service.go", "manifest.go", "go.mod", ".gitignore"} {
		if _, err := os.Stat(filepath.Join(outDir, f)); err != nil {
			t.Errorf("expected file %s: %v", f, err)
		}
	}
}

func TestNewDefaultModule(t *testing.T) {
	dir := t.TempDir()
	outDir := filepath.Join(dir, "myplug")

	root := newTestRoot()
	root.AddCommand(cmd.NewNewCmd())
	root.SetArgs([]string{"new", "myplug", "--dir", outDir})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(outDir, "go.mod"))
	if err != nil {
		t.Fatalf("read go.mod: %v", err)
	}
	// Default module is example.com/<name>
	if !strings.Contains(string(data), "example.com/myplug") {
		t.Errorf("go.mod missing default module: %s", data)
	}
}

func TestNewRejectsExistingDir(t *testing.T) {
	dir := t.TempDir() // already exists

	root := newTestRoot()
	root.AddCommand(cmd.NewNewCmd())
	root.SetArgs([]string{"new", "plug", "--dir", dir})
	if err := root.Execute(); err == nil {
		t.Error("expected error for existing directory, got nil")
	}
}

func TestNewInvalidName(t *testing.T) {
	dir := t.TempDir()

	root := newTestRoot()
	root.AddCommand(cmd.NewNewCmd())
	root.SetArgs([]string{"new", "Bad_Name", "--dir", filepath.Join(dir, "out")})
	if err := root.Execute(); err == nil {
		t.Error("expected error for invalid name, got nil")
	}
}

// newTestRoot returns a minimal root command for use in tests.
func newTestRoot() *cobra.Command {
	return &cobra.Command{
		Use:           "gleipnir-plugin",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
}
