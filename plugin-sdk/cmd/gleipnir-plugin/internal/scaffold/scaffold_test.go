package scaffold_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/felag-engineering/gleipnir/plugin-sdk/cmd/gleipnir-plugin/internal/scaffold"
)

func TestDefaultKindIsTool(t *testing.T) {
	dir := t.TempDir()
	outDir := filepath.Join(dir, "myplugin")

	err := scaffold.Generate(scaffold.Opts{
		Name:   "myplugin",
		Kind:   "tool",
		Dir:    outDir,
		Module: "example.com/myplugin",
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	// main.go and service.go must exist for a tool scaffold.
	for _, f := range []string{"main.go", "service.go", "manifest.go", "service_test.go"} {
		if _, err := os.Stat(filepath.Join(outDir, f)); err != nil {
			t.Errorf("expected file %s: %v", f, err)
		}
	}
}

func TestAllKinds(t *testing.T) {
	for _, kind := range []string{"tool", "channel", "trigger", "combo"} {
		kind := kind
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			outDir := filepath.Join(dir, "plug")

			err := scaffold.Generate(scaffold.Opts{
				Name:   "plug",
				Kind:   kind,
				Dir:    outDir,
				Module: "example.com/plug",
			})
			if err != nil {
				t.Fatalf("Generate kind=%s: %v", kind, err)
			}

			// Common files must always be present.
			for _, f := range []string{"go.mod", ".gitignore", "Makefile", "README.md"} {
				if _, err := os.Stat(filepath.Join(outDir, f)); err != nil {
					t.Errorf("kind=%s: expected common file %s: %v", kind, f, err)
				}
			}

			// Kind-specific files must be present.
			for _, f := range []string{"main.go", "service.go", "manifest.go"} {
				if _, err := os.Stat(filepath.Join(outDir, f)); err != nil {
					t.Errorf("kind=%s: expected kind file %s: %v", kind, f, err)
				}
			}
		})
	}
}

func TestRejectsExistingDir(t *testing.T) {
	dir := t.TempDir()
	// dir already exists — Generate must fail.
	err := scaffold.Generate(scaffold.Opts{
		Name:   "myplugin",
		Kind:   "tool",
		Dir:    dir,
		Module: "example.com/myplugin",
	})
	if err == nil {
		t.Fatal("expected error for existing directory, got nil")
	}
}

func TestNoCommittedKeyFile(t *testing.T) {
	dir := t.TempDir()
	outDir := filepath.Join(dir, "safeplug")

	if err := scaffold.Generate(scaffold.Opts{
		Name:   "safeplug",
		Kind:   "tool",
		Dir:    outDir,
		Module: "example.com/safeplug",
	}); err != nil {
		t.Fatalf("Generate: %v", err)
	}

	// (a) No signing key files should be written.
	entries, err := os.ReadDir(outDir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		name := e.Name()
		for _, suffix := range []string{".pem", ".key", ".minisig"} {
			if strings.HasSuffix(name, suffix) {
				t.Errorf("scaffold wrote key file %s (security hazard)", name)
			}
		}
	}

	// (b) The .gitignore must contain patterns for key files.
	gitignoreData, err := os.ReadFile(filepath.Join(outDir, ".gitignore"))
	if err != nil {
		t.Fatalf("read .gitignore: %v", err)
	}
	gitignore := string(gitignoreData)
	for _, pattern := range []string{"*.pem", "*.key", "*.minisig"} {
		if !strings.Contains(gitignore, pattern) {
			t.Errorf(".gitignore missing pattern %q", pattern)
		}
	}
}

func TestValidateName(t *testing.T) {
	cases := []struct {
		name    string
		wantErr bool
	}{
		{"myplugin", false},
		{"my-plugin", false},
		{"plugin123", false},
		{"a", false},
		{"", true},
		{"MyPlugin", true},
		{"my plugin", true},
		{"1start", true},
		{"-start", true},
		{"my_plugin", true},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			outDir := filepath.Join(dir, "out")
			err := scaffold.Generate(scaffold.Opts{
				Name:   tc.name,
				Kind:   "tool",
				Dir:    outDir,
				Module: "example.com/plug",
			})
			if tc.wantErr && err == nil {
				t.Errorf("name=%q: expected error, got nil", tc.name)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("name=%q: unexpected error: %v", tc.name, err)
			}
		})
	}
}

func TestGoModContainsModule(t *testing.T) {
	dir := t.TempDir()
	outDir := filepath.Join(dir, "modcheck")
	module := "github.com/myorg/modcheck"

	if err := scaffold.Generate(scaffold.Opts{
		Name:   "modcheck",
		Kind:   "tool",
		Dir:    outDir,
		Module: module,
	}); err != nil {
		t.Fatalf("Generate: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(outDir, "go.mod"))
	if err != nil {
		t.Fatalf("read go.mod: %v", err)
	}
	if !strings.Contains(string(data), "module "+module) {
		t.Errorf("go.mod does not contain module declaration %q:\n%s", module, data)
	}
}
