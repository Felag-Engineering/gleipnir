package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/felag-engineering/gleipnir/plugin-sdk/signing"
)

// writeTestKey generates an unencrypted keypair and writes the .key and .pub
// files to dir. Returns the public key.
func writeTestKey(t *testing.T, dir string) signing.PublicKey {
	t.Helper()
	pk, sk, err := signing.GenerateKeypair(nil)
	if err != nil {
		t.Fatalf("generate keypair: %v", err)
	}
	keyData := signing.MarshalSecretKey(sk, "test key")
	pubData := signing.MarshalPublicKey(pk, "test pub")
	if err := os.WriteFile(filepath.Join(dir, "signing.key"), keyData, 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "signing.pub"), pubData, 0o644); err != nil {
		t.Fatalf("write pub: %v", err)
	}
	return pk
}

func TestRunSignProducesVerifiableSignature(t *testing.T) {
	dir := t.TempDir()
	pk := writeTestKey(t, dir)

	binaryPath := filepath.Join(dir, "myplugin")
	manifestPath := filepath.Join(dir, "manifest.yaml")
	outPath := filepath.Join(dir, "myplugin.minisig")

	binaryData := []byte("fake binary content")
	manifestData := []byte(canonicalManifestYAML)

	if err := os.WriteFile(binaryPath, binaryData, 0o755); err != nil {
		t.Fatalf("write binary: %v", err)
	}
	if err := os.WriteFile(manifestPath, manifestData, 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	fakeCmd := &cobra.Command{}
	fakeCmd.SetOut(&bytes.Buffer{})
	fakeCmd.SetErr(&bytes.Buffer{})
	fakeCmd.SetIn(strings.NewReader(""))

	keyPath := filepath.Join(dir, "signing.key")
	if err := runSign(fakeCmd, keyPath, false, binaryPath, manifestPath, outPath, "timestamp:999"); err != nil {
		t.Fatalf("runSign: %v", err)
	}

	sigData, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read .minisig: %v", err)
	}

	sig, _, err := signing.ParseSignature(sigData)
	if err != nil {
		t.Fatalf("parse signature: %v", err)
	}

	payload := signing.PluginPayload(binaryData, manifestData)
	if err := signing.Verify(pk, payload, sig, "timestamp:999"); err != nil {
		t.Errorf("verify: %v", err)
	}
}

func TestRunSignDefaultOutPath(t *testing.T) {
	dir := t.TempDir()
	writeTestKey(t, dir)

	binaryPath := filepath.Join(dir, "myplugin")
	manifestPath := filepath.Join(dir, "manifest.yaml")

	if err := os.WriteFile(binaryPath, []byte("bin"), 0o755); err != nil {
		t.Fatalf("write binary: %v", err)
	}
	if err := os.WriteFile(manifestPath, []byte(canonicalManifestYAML), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	// Change to the tempdir so the default output path lands there.
	origDir, _ := os.Getwd()
	defer os.Chdir(origDir)
	os.Chdir(dir)

	fakeCmd := &cobra.Command{}
	fakeCmd.SetOut(&bytes.Buffer{})
	fakeCmd.SetErr(&bytes.Buffer{})
	fakeCmd.SetIn(strings.NewReader(""))

	keyPath := filepath.Join(dir, "signing.key")
	// Empty out path → default to <binary-basename>.minisig
	if err := runSign(fakeCmd, keyPath, false, binaryPath, manifestPath, "", "comment"); err != nil {
		t.Fatalf("runSign: %v", err)
	}

	// Default .minisig is written in the current directory.
	expectedSigPath := filepath.Join(dir, "myplugin.minisig")
	if _, err := os.Stat(expectedSigPath); err != nil {
		t.Errorf("expected .minisig at %s: %v", expectedSigPath, err)
	}
}

func TestRunSignMissingBinary(t *testing.T) {
	dir := t.TempDir()
	writeTestKey(t, dir)

	fakeCmd := &cobra.Command{}
	fakeCmd.SetOut(&bytes.Buffer{})
	fakeCmd.SetErr(&bytes.Buffer{})
	fakeCmd.SetIn(strings.NewReader(""))

	keyPath := filepath.Join(dir, "signing.key")
	err := runSign(fakeCmd, keyPath, false, "/nonexistent/binary", "manifest.yaml", "", "")
	if err == nil {
		t.Error("expected error for missing binary, got nil")
	}
}

// writeEncryptedTestKey generates an encrypted keypair and writes the .key and
// .pub files to dir using the given passphrase. Returns the public key.
func writeEncryptedTestKey(t *testing.T, dir string, passphrase string) signing.PublicKey {
	t.Helper()
	pk, sk, err := signing.GenerateKeypair(nil)
	if err != nil {
		t.Fatalf("generate keypair: %v", err)
	}
	enc, err := signing.EncryptSecretKey(sk, []byte(passphrase), signing.KDFAlgScrypt)
	if err != nil {
		t.Fatalf("encrypt key: %v", err)
	}
	keyData := signing.MarshalSecretKey(enc, "encrypted test key")
	pubData := signing.MarshalPublicKey(pk, "test pub")
	if err := os.WriteFile(filepath.Join(dir, "signing.key"), keyData, 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "signing.pub"), pubData, 0o644); err != nil {
		t.Fatalf("write pub: %v", err)
	}
	return pk
}

// TestRunSignEncryptedKeyProducesVerifiableSignature is the regression test for
// the bug where DecryptSecretKey discarded the plaintext KeyID, causing Sign to
// embed the still-encrypted KeyID and producing signatures that fail Verify.
func TestRunSignEncryptedKeyProducesVerifiableSignature(t *testing.T) {
	const passphrase = "test-passphrase-for-encrypted-key"
	dir := t.TempDir()
	pk := writeEncryptedTestKey(t, dir, passphrase)

	binaryPath := filepath.Join(dir, "myplugin")
	manifestPath := filepath.Join(dir, "manifest.yaml")
	outPath := filepath.Join(dir, "myplugin.minisig")

	binaryData := []byte("fake binary content")
	manifestData := []byte(canonicalManifestYAML)

	if err := os.WriteFile(binaryPath, binaryData, 0o755); err != nil {
		t.Fatalf("write binary: %v", err)
	}
	if err := os.WriteFile(manifestPath, manifestData, 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	// Provide passphrase via env var (CI mode).
	t.Setenv("GLEIPNIR_PLUGIN_SIGNING_KEY_PASSPHRASE", passphrase)

	fakeCmd := &cobra.Command{}
	fakeCmd.SetOut(&bytes.Buffer{})
	fakeCmd.SetErr(&bytes.Buffer{})
	fakeCmd.SetIn(strings.NewReader(""))

	keyPath := filepath.Join(dir, "signing.key")
	if err := runSign(fakeCmd, keyPath, false, binaryPath, manifestPath, outPath, "timestamp:1234"); err != nil {
		t.Fatalf("runSign with encrypted key: %v", err)
	}

	sigData, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read .minisig: %v", err)
	}

	sig, _, err := signing.ParseSignature(sigData)
	if err != nil {
		t.Fatalf("parse signature: %v", err)
	}

	payload := signing.PluginPayload(binaryData, manifestData)
	if err := signing.Verify(pk, payload, sig, "timestamp:1234"); err != nil {
		t.Errorf("verify encrypted-key signature: %v", err)
	}
}

func TestRunSignKeyStdin(t *testing.T) {
	dir := t.TempDir()
	pk, sk, err := signing.GenerateKeypair(nil)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	keyData := signing.MarshalSecretKey(sk, "test")
	binaryPath := filepath.Join(dir, "myplugin")
	manifestPath := filepath.Join(dir, "manifest.yaml")
	outPath := filepath.Join(dir, "out.minisig")

	binaryData := []byte("binary")
	manifestData := []byte(canonicalManifestYAML)

	if err := os.WriteFile(binaryPath, binaryData, 0o755); err != nil {
		t.Fatalf("write binary: %v", err)
	}
	if err := os.WriteFile(manifestPath, manifestData, 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	fakeCmd := &cobra.Command{}
	fakeCmd.SetOut(&bytes.Buffer{})
	fakeCmd.SetErr(&bytes.Buffer{})
	fakeCmd.SetIn(bytes.NewReader(keyData))

	if err := runSign(fakeCmd, "", true, binaryPath, manifestPath, outPath, "tc"); err != nil {
		t.Fatalf("runSign --key-stdin: %v", err)
	}

	sigData, _ := os.ReadFile(outPath)
	sig, _, err := signing.ParseSignature(sigData)
	if err != nil {
		t.Fatalf("parse sig: %v", err)
	}
	payload := signing.PluginPayload(binaryData, manifestData)
	if err := signing.Verify(pk, payload, sig, "tc"); err != nil {
		t.Errorf("verify: %v", err)
	}
}
