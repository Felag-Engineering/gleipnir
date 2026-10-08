package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/infra/crypto"
	"github.com/felag-engineering/gleipnir/internal/testutil"
)

// seedPluginCredentials adds one plugin instance holding encrypted credentials.
func seedPluginCredentials(t *testing.T, s *db.Store, encKey []byte, instanceID string) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := s.Queries().CreatePlugin(ctx, db.CreatePluginParams{
		ID: "plugin-" + instanceID, Name: "plugin-" + instanceID, PluginVersion: "1.0.0",
		ManifestSnapshot: "{}", TrustedPubkey: "pk", Status: "active", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("seed plugin: %v", err)
	}
	ct, err := crypto.Encrypt(encKey, `{"strategy":"static_api_key","api_key":"plugin-secret"}`)
	if err != nil {
		t.Fatalf("encrypt plugin credentials: %v", err)
	}
	if _, err := s.Queries().CreatePluginInstance(ctx, db.CreatePluginInstanceParams{
		ID: instanceID, PluginID: "plugin-" + instanceID, InstanceName: "inst-" + instanceID,
		ConfigJson: "{}", SubscriptionScopeJson: "{}", CredentialsEncrypted: &ct,
		HandshakeVersions: "{}", HealthState: "pending_key_approval", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("seed plugin instance: %v", err)
	}
}

// seededSecretCount is the number of secrets written by seedDB (3 provider
// keys, 2 openai-compat keys, 1 webhook secret, 1 MCP header set) plus the one
// plugin credential seeded by newVerifyPath.
const seededSecretCount = 8

func newVerifyPath(t *testing.T, encKeyHex string) string {
	t.Helper()
	s := testutil.NewTestStore(t)
	seedDB(t, s, mustKey(encKeyHex))
	seedPluginCredentials(t, s, mustKey(encKeyHex), "inst-1")
	path := storePath(t, s)
	s.Close()
	return path
}

func TestVerifyKeys_AllValid(t *testing.T) {
	path := newVerifyPath(t, keyA)

	var stdout, stderr bytes.Buffer
	code := VerifyKeys(context.Background(), path, mustKey(keyA), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, stderr: %s", code, stderr.String())
	}
	if got, want := strings.TrimSpace(stdout.String()), "verified 8 secrets OK"; got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}
}

func TestVerifyKeys_WrongKeyReportsEveryRow(t *testing.T) {
	path := newVerifyPath(t, keyA)

	var stdout, stderr bytes.Buffer
	code := VerifyKeys(context.Background(), path, mustKey(keyB), &stdout, &stderr)
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if n := strings.Count(stderr.String(), "FAILED "); n != seededSecretCount {
		t.Errorf("FAILED lines = %d, want %d; stderr:\n%s", n, seededSecretCount, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout should be empty on failure, got %q", stdout.String())
	}
}

func TestVerifyKeys_OneCorruptRow(t *testing.T) {
	path := newVerifyPath(t, keyA)

	// Corrupt a single MCP header ciphertext, leaving its bytes valid base64 so
	// only authentication fails.
	s := openForVerify(t, path)
	other, err := crypto.Encrypt(mustKey(keyC), "x")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`UPDATE mcp_servers SET auth_headers_encrypted = ? WHERE id = 'mcp-with-headers'`, other); err != nil {
		t.Fatal(err)
	}
	s.Close()

	var stdout, stderr bytes.Buffer
	code := VerifyKeys(context.Background(), path, mustKey(keyA), &stdout, &stderr)
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	errText := stderr.String()
	if !strings.Contains(errText, "FAILED mcp_servers.auth_headers_encrypted id=mcp-with-headers") {
		t.Errorf("missing failing row in stderr:\n%s", errText)
	}
	if n := strings.Count(errText, "FAILED "); n != 1 {
		t.Errorf("FAILED lines = %d, want 1; stderr:\n%s", n, errText)
	}
	if strings.Contains(errText, other) {
		t.Error("stderr leaked ciphertext")
	}
}

func TestVerifyKeys_EmptyDB(t *testing.T) {
	s := testutil.NewTestStore(t)
	path := storePath(t, s)
	s.Close()

	var stdout, stderr bytes.Buffer
	code := VerifyKeys(context.Background(), path, mustKey(keyA), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, stderr: %s", code, stderr.String())
	}
	if got, want := strings.TrimSpace(stdout.String()), "verified 0 secrets OK"; got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}
}

func TestVerifyKeys_MissingDBIsNotCreated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nope.db")

	var stdout, stderr bytes.Buffer
	code := VerifyKeys(context.Background(), path, mustKey(keyA), &stdout, &stderr)
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("verify-keys created %s (stat err: %v)", path, err)
	}
}

func TestVerifyKeysCmd_KeyFromEnv(t *testing.T) {
	path := newVerifyPath(t, keyA)

	tests := []struct {
		name    string
		env     string
		set     bool
		wantErr string
	}{
		{name: "missing", set: false, wantErr: "GLEIPNIR_ENCRYPTION_KEY is not set"},
		{name: "invalid", env: "not-a-key", set: true, wantErr: "parse GLEIPNIR_ENCRYPTION_KEY"},
		{name: "valid", env: keyA, set: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.set {
				t.Setenv(encryptionKeyEnv, tc.env)
			} else {
				t.Setenv(encryptionKeyEnv, "")
			}
			var stdout, stderr bytes.Buffer
			cmd := newVerifyKeysCmd()
			cmd.SetOut(&stdout)
			cmd.SetErr(&stderr)
			cmd.SetArgs([]string{"--db-path", path})
			err := cmd.Execute()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if !strings.Contains(stdout.String(), "verified 8 secrets OK") {
					t.Errorf("stdout = %q", stdout.String())
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v, want containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestVerifyKeysCmd_HasNoKeyFlag(t *testing.T) {
	if newVerifyKeysCmd().Flags().Lookup("key") != nil {
		t.Error("verify-keys must not accept the key as a flag")
	}
}

// TestRotate_RotatesPluginCredentials guards the shared enumeration: plugin
// credentials must be re-encrypted by rotate-key, not stranded under the old key.
func TestRotate_RotatesPluginCredentials(t *testing.T) {
	path := newVerifyPath(t, keyA)

	var stdout, stderr bytes.Buffer
	if code := Rotate(context.Background(), path, mustKey(keyA), mustKey(keyB), false, &stdout, &stderr); code != 0 {
		t.Fatalf("rotate exit = %d, stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "1 plugin credential sets") {
		t.Errorf("summary = %q", stdout.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := VerifyKeys(context.Background(), path, mustKey(keyB), &stdout, &stderr); code != 0 {
		t.Fatalf("verify with new key exit = %d, stderr: %s", code, stderr.String())
	}
}
