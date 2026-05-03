package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// canonicalManifestYAML is a pre-canonicalised manifest YAML for comparison
// tests. It must be byte-identical to what jsonToCanonicalYAML produces from
// sampleManifestJSON (keys sorted, 2-space indent).
const canonicalManifestYAML = `auth:
  mode: instance_credentials
  strategy: none
name: testplugin
schema_version: v1
services:
  tool: v1
tools:
  - description: Does things
    name: my_tool
version: 1.0.0
`

func TestValidateOK(t *testing.T) {
	orig := runBinary
	defer func() { runBinary = orig }()
	runBinary = func(_ string, _ []string) ([]byte, error) {
		return []byte(sampleManifestJSON), nil
	}

	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "manifest.yaml")
	if err := os.WriteFile(manifestPath, []byte(canonicalManifestYAML), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	fakeCmd := &cobra.Command{}
	var out bytes.Buffer
	var errOut bytes.Buffer
	fakeCmd.SetOut(&out)
	fakeCmd.SetErr(&errOut)

	if err := runValidate("./fake", manifestPath, fakeCmd); err != nil {
		t.Fatalf("expected OK, got error: %v\nstderr: %s", err, errOut.String())
	}
	if !strings.Contains(out.String(), "OK") {
		t.Errorf("expected OK in output, got: %q", out.String())
	}
}

func TestValidateDriftDetected(t *testing.T) {
	orig := runBinary
	defer func() { runBinary = orig }()
	// Binary emits a manifest with a different version.
	const driftedJSON = `{
		"schema_version": "v1",
		"name": "testplugin",
		"version": "2.0.0",
		"services": {"tool": "v1"},
		"auth": {"mode": "instance_credentials", "strategy": "none"},
		"tools": [{"name": "my_tool", "description": "Does things"}]
	}`
	runBinary = func(_ string, _ []string) ([]byte, error) {
		return []byte(driftedJSON), nil
	}

	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "manifest.yaml")
	if err := os.WriteFile(manifestPath, []byte(canonicalManifestYAML), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	fakeCmd := &cobra.Command{}
	fakeCmd.SetOut(&bytes.Buffer{})
	var errOut bytes.Buffer
	fakeCmd.SetErr(&errOut)

	err := runValidate("./fake", manifestPath, fakeCmd)
	if err == nil {
		t.Fatal("expected mismatch error, got nil")
	}
	if !strings.Contains(errOut.String(), "drift") {
		t.Errorf("expected 'drift' in stderr, got: %q", errOut.String())
	}
}

func TestValidateOnDiskNotCanonical(t *testing.T) {
	orig := runBinary
	defer func() { runBinary = orig }()
	runBinary = func(_ string, _ []string) ([]byte, error) {
		return []byte(sampleManifestJSON), nil
	}

	// Write the manifest with unsorted keys (schema_version before name, but
	// valid YAML). The validator canonicalises both sides before comparing.
	const unsortedYAML = `schema_version: v1
name: testplugin
version: 1.0.0
auth:
  mode: instance_credentials
  strategy: none
services:
  tool: v1
tools:
  - name: my_tool
    description: Does things
`

	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "manifest.yaml")
	if err := os.WriteFile(manifestPath, []byte(unsortedYAML), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	fakeCmd := &cobra.Command{}
	var out bytes.Buffer
	fakeCmd.SetOut(&out)
	fakeCmd.SetErr(&bytes.Buffer{})

	// Both sides canonicalised → should match.
	if err := runValidate("./fake", manifestPath, fakeCmd); err != nil {
		t.Fatalf("expected OK after canonicalisation, got: %v", err)
	}
}

func TestValidateMissingManifest(t *testing.T) {
	orig := runBinary
	defer func() { runBinary = orig }()
	runBinary = func(_ string, _ []string) ([]byte, error) {
		return []byte(sampleManifestJSON), nil
	}

	fakeCmd := &cobra.Command{}
	fakeCmd.SetOut(&bytes.Buffer{})
	fakeCmd.SetErr(&bytes.Buffer{})

	err := runValidate("./fake", "/nonexistent/manifest.yaml", fakeCmd)
	if err == nil {
		t.Fatal("expected error for missing manifest, got nil")
	}
	if !strings.Contains(err.Error(), "read") {
		t.Errorf("expected 'read' in error, got: %v", err)
	}
}
