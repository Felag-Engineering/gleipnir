package scaffold_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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

// TestGeneratedProjectBuilds scaffolds each kind into a temp dir, wires the
// in-repo plugin-sdk via a synthetic go.work (so the generated project resolves
// the SDK locally without needing a module proxy), then runs go build, go vet,
// and go test. This is the canonical guard against "scaffold generates broken code".
//
// Skip guards keep CI fast: the test is skipped when the go binary is absent or
// when -short is passed.
func TestGeneratedProjectBuilds(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go binary not found in PATH; skipping build test")
	}
	if testing.Short() {
		t.Skip("skipping scaffold build test in -short mode")
	}

	// Locate the plugin-sdk module root via the source file path.
	// scaffold_test.go is at: plugin-sdk/cmd/gleipnir-plugin/internal/scaffold/
	// Four levels up (scaffold → internal → gleipnir-plugin → cmd → plugin-sdk)
	// lands at the plugin-sdk module root.
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed")
	}
	sdkRoot, err := filepath.Abs(filepath.Join(filepath.Dir(thisFile), "..", "..", "..", ".."))
	if err != nil {
		t.Fatalf("resolve sdkRoot: %v", err)
	}

	// Verify we actually found the right directory before trusting it.
	goModPath := filepath.Join(sdkRoot, "go.mod")
	goModBytes, err := os.ReadFile(goModPath)
	if err != nil {
		t.Fatalf("read %s: %v", goModPath, err)
	}
	if !strings.Contains(string(goModBytes), "module github.com/felag-engineering/gleipnir/plugin-sdk") {
		t.Fatalf("sdkRoot %q does not look like the plugin-sdk module (go.mod module line not found)", sdkRoot)
	}

	for _, kind := range []string{"tool", "channel", "trigger", "combo"} {
		kind := kind
		t.Run(kind, func(t *testing.T) {
			t.Parallel()

			tempRoot := t.TempDir()
			plugDir := filepath.Join(tempRoot, "plug")

			// Generate the scaffold. No SDKReplace: the go.work below owns the
			// replace directive so generated go.mod stays clean.
			if err := scaffold.Generate(scaffold.Opts{
				Name:   "plug",
				Kind:   kind,
				Dir:    plugDir,
				Module: "example.com/plug",
			}); err != nil {
				t.Fatalf("Generate kind=%s: %v", kind, err)
			}

			// Write a synthetic go.work that makes the Go toolchain resolve the
			// plugin-sdk from the in-repo path. The replace directive here stands
			// in for the usual --sdk-replace flag, keeping generated go.mod clean.
			goWork := "go 1.25.10\n\nuse (\n\t./plug\n)\n\nreplace github.com/felag-engineering/gleipnir/plugin-sdk => " + sdkRoot + "\n"
			goWorkPath := filepath.Join(tempRoot, "go.work")
			if err := os.WriteFile(goWorkPath, []byte(goWork), 0o644); err != nil {
				t.Fatalf("write go.work: %v", err)
			}

			// GOWORK tells the toolchain to use our synthetic workspace so that
			// it does not walk up and find the repo's own go.work (which does not
			// list the temp module).
			env := append(os.Environ(), "GOWORK="+goWorkPath)

			for _, subcmd := range [][]string{
				{"go", "build", "./..."},
				{"go", "vet", "./..."},
				{"go", "test", "./..."},
			} {
				cmd := exec.Command(subcmd[0], subcmd[1:]...)
				cmd.Dir = plugDir
				cmd.Env = env
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("kind=%s: %v failed:\n%s", kind, subcmd, out)
				}
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
