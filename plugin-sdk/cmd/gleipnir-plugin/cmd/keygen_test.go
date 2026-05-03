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

func TestKeygenCreatesFiles(t *testing.T) {
	dir := t.TempDir()
	fakeCmd := &cobra.Command{}
	var out, errOut bytes.Buffer
	fakeCmd.SetOut(&out)
	fakeCmd.SetErr(&errOut)
	fakeCmd.SetIn(strings.NewReader("testpass\n"))

	err := runKeygen(fakeCmd, dir, "signing", "scrypt", false, true, false)
	if err != nil {
		t.Fatalf("runKeygen: %v", err)
	}

	keyPath := filepath.Join(dir, "signing.key")
	pubPath := filepath.Join(dir, "signing.pub")

	// Check files exist.
	keyInfo, err := os.Stat(keyPath)
	if err != nil {
		t.Fatalf("key file missing: %v", err)
	}
	pubInfo, err := os.Stat(pubPath)
	if err != nil {
		t.Fatalf("pub file missing: %v", err)
	}

	// Check permissions.
	if keyInfo.Mode().Perm() != 0o600 {
		t.Errorf("key file mode: got %v, want 0600", keyInfo.Mode().Perm())
	}
	if pubInfo.Mode().Perm() != 0o644 {
		t.Errorf("pub file mode: got %v, want 0644", pubInfo.Mode().Perm())
	}

	// Public key block should be in stdout.
	if !strings.Contains(out.String(), "untrusted comment:") {
		t.Errorf("expected public key block in stdout, got: %q", out.String())
	}
}

func TestKeygenScryptRoundTrip(t *testing.T) {
	testKeygenRoundTrip(t, "scrypt")
}

func TestKeygenArgon2RoundTrip(t *testing.T) {
	testKeygenRoundTrip(t, "argon2")
}

func testKeygenRoundTrip(t *testing.T, kdfFlag string) {
	t.Helper()
	dir := t.TempDir()
	passphrase := "correcthorsebatterystaple"

	// Set passphrase via env var.
	t.Setenv("GLEIPNIR_PLUGIN_SIGNING_KEY_PASSPHRASE", passphrase)

	fakeCmd := &cobra.Command{}
	fakeCmd.SetOut(&bytes.Buffer{})
	fakeCmd.SetErr(&bytes.Buffer{})
	fakeCmd.SetIn(strings.NewReader(""))

	if err := runKeygen(fakeCmd, dir, "mykey", kdfFlag, false, false, false); err != nil {
		t.Fatalf("runKeygen (%s): %v", kdfFlag, err)
	}

	keyPath := filepath.Join(dir, "mykey.key")
	pubPath := filepath.Join(dir, "mykey.pub")

	keyData, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("read key: %v", err)
	}
	pubData, err := os.ReadFile(pubPath)
	if err != nil {
		t.Fatalf("read pub: %v", err)
	}

	sk, _, err := signing.ParseSecretKey(keyData)
	if err != nil {
		t.Fatalf("parse secret key: %v", err)
	}
	pk, _, err := signing.ParsePublicKey(pubData)
	if err != nil {
		t.Fatalf("parse public key: %v", err)
	}

	raw, keyID, err := signing.DecryptSecretKey(sk, []byte(passphrase))
	if err != nil {
		t.Fatalf("decrypt key (%s): %v", kdfFlag, err)
	}

	// Confirm the key pair works together.
	payload := []byte("test payload")
	sig, err := signing.Sign(raw, keyID, payload, "test comment")
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if err := signing.Verify(pk, payload, sig, "test comment"); err != nil {
		t.Errorf("verify: %v", err)
	}
}

func TestKeygenUnencrypted(t *testing.T) {
	dir := t.TempDir()

	fakeCmd := &cobra.Command{}
	fakeCmd.SetOut(&bytes.Buffer{})
	fakeCmd.SetErr(&bytes.Buffer{})
	fakeCmd.SetIn(strings.NewReader(""))

	if err := runKeygen(fakeCmd, dir, "signing", "scrypt", false, false, true); err != nil {
		t.Fatalf("runKeygen --unencrypted: %v", err)
	}

	keyData, _ := os.ReadFile(filepath.Join(dir, "signing.key"))
	sk, _, err := signing.ParseSecretKey(keyData)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if sk.KDFAlg != ([2]byte{}) {
		t.Errorf("expected unencrypted key (KDFAlg=0), got %v", sk.KDFAlg)
	}
}

func TestKeygenForceOverwrite(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "signing.key")

	// Create existing file.
	if err := os.WriteFile(keyPath, []byte("old content"), 0o600); err != nil {
		t.Fatalf("write existing: %v", err)
	}

	fakeCmd := &cobra.Command{}
	fakeCmd.SetOut(&bytes.Buffer{})
	fakeCmd.SetErr(&bytes.Buffer{})
	fakeCmd.SetIn(strings.NewReader(""))

	// Without --force should fail.
	if err := runKeygen(fakeCmd, dir, "signing", "scrypt", false, false, true); err == nil {
		t.Error("expected error without --force, got nil")
	}

	// With --force should succeed.
	if err := runKeygen(fakeCmd, dir, "signing", "scrypt", true, false, true); err != nil {
		t.Fatalf("runKeygen --force: %v", err)
	}

	content, _ := os.ReadFile(keyPath)
	if string(content) == "old content" {
		t.Error("file not overwritten with --force")
	}
}

func TestKeygenInvalidKDF(t *testing.T) {
	dir := t.TempDir()
	fakeCmd := &cobra.Command{}
	fakeCmd.SetOut(&bytes.Buffer{})
	fakeCmd.SetErr(&bytes.Buffer{})
	fakeCmd.SetIn(strings.NewReader(""))

	err := runKeygen(fakeCmd, dir, "signing", "invalid", false, false, false)
	if err == nil {
		t.Error("expected error for invalid KDF, got nil")
	}
	if !strings.Contains(err.Error(), "unknown --kdf") {
		t.Errorf("expected 'unknown --kdf' in error, got: %v", err)
	}
}
