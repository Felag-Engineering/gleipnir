package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// sampleManifestJSON is representative --emit-manifest output from a plugin
// binary. Keys are intentionally unordered to validate canonicalisation.
const sampleManifestJSON = `{
  "version": "1.0.0",
  "schema_version": "v1",
  "name": "testplugin",
  "services": {"tool": "v1"},
  "auth": {"strategy": "none", "mode": "instance_credentials"},
  "tools": [
    {"description": "Does things", "name": "my_tool"}
  ]
}`

func TestGenManifestWritesDeterministicOutput(t *testing.T) {
	orig := runBinary
	defer func() { runBinary = orig }()
	runBinary = func(_ string, _ []string) ([]byte, error) {
		return []byte(sampleManifestJSON), nil
	}

	dir := t.TempDir()
	outFile := filepath.Join(dir, "manifest.yaml")

	fakeCmd := &cobra.Command{}
	var buf bytes.Buffer
	fakeCmd.SetOut(&buf)

	if err := runGenManifest("./fake", outFile, fakeCmd); err != nil {
		t.Fatalf("runGenManifest: %v", err)
	}

	data, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}

	// Run again — output must be byte-identical.
	outFile2 := filepath.Join(dir, "manifest2.yaml")
	fakeCmd2 := &cobra.Command{}
	fakeCmd2.SetOut(&bytes.Buffer{})
	if err := runGenManifest("./fake", outFile2, fakeCmd2); err != nil {
		t.Fatalf("second runGenManifest: %v", err)
	}
	data2, err := os.ReadFile(outFile2)
	if err != nil {
		t.Fatalf("read second output: %v", err)
	}

	if !bytes.Equal(data, data2) {
		t.Fatalf("non-deterministic output:\nfirst:\n%s\nsecond:\n%s", data, data2)
	}
}

func TestGenManifestStdoutMode(t *testing.T) {
	orig := runBinary
	defer func() { runBinary = orig }()
	runBinary = func(_ string, _ []string) ([]byte, error) {
		return []byte(sampleManifestJSON), nil
	}

	fakeCmd := &cobra.Command{}
	var buf bytes.Buffer
	fakeCmd.SetOut(&buf)

	// Empty out path → write to stdout.
	if err := runGenManifest("./fake", "", fakeCmd); err != nil {
		t.Fatalf("runGenManifest stdout mode: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "schema_version") {
		t.Errorf("expected YAML in stdout, got: %q", out)
	}
}

func TestGenManifestBinaryFailurePropagates(t *testing.T) {
	orig := runBinary
	defer func() { runBinary = orig }()
	runBinary = func(_ string, _ []string) ([]byte, error) {
		return nil, errBinaryFailed("exit status 1")
	}

	fakeCmd := &cobra.Command{}
	fakeCmd.SetOut(&bytes.Buffer{})

	err := runGenManifest("./nonexistent", "", fakeCmd)
	if err == nil {
		t.Fatal("expected error when binary fails, got nil")
	}
	if !strings.Contains(err.Error(), "invoke binary") {
		t.Errorf("expected 'invoke binary' in error, got: %v", err)
	}
}

type errBinaryFailed string

func (e errBinaryFailed) Error() string { return string(e) }
