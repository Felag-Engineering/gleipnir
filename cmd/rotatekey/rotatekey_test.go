package rotatekey_test

import (
	"bytes"
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/rapp992/gleipnir/cmd/rotatekey"
	"github.com/rapp992/gleipnir/internal/admin"
	"github.com/rapp992/gleipnir/internal/db"
	"github.com/rapp992/gleipnir/internal/testutil"
)

// mustKey parses a hex key or panics. Used only for test key constants.
func mustKey(hex string) []byte {
	k, err := admin.ParseEncryptionKey(hex)
	if err != nil {
		panic(err)
	}
	return k
}

// testKeys holds three distinct 32-byte keys for use across tests.
var (
	keyA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	keyB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	keyC = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
)

// newSeededPath creates a temp DB, seeds it with test data (all encrypted with
// encKeyHex), closes the store, and returns the file path. The caller can then
// open it separately — or let rotatekey.Run open it — without connection conflicts.
func newSeededPath(t *testing.T, encKeyHex string) string {
	t.Helper()
	s := testutil.NewTestStore(t)
	seedDB(t, s, mustKey(encKeyHex))
	path := storePath(t, s)
	// Close so rotatekey.Run can open its own connection without SQLITE_BUSY.
	s.Close()
	return path
}

// seedDB seeds the store with all three secret types for rotation tests.
//
// Seeded data:
//   - system_settings: anthropic_api_key, openai_api_key, google_api_key (encrypted with encKey)
//   - system_settings: public_url (plaintext — not an API key, must NOT be touched)
//   - openai_compat_providers: two rows with encrypted API keys
//   - policies: one with a webhook secret (encrypted), one without
func seedDB(t *testing.T, s *db.Store, encKey []byte) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC().Format(time.RFC3339)

	encryptWith := func(plaintext string) string {
		c, err := admin.Encrypt(encKey, plaintext)
		if err != nil {
			t.Fatalf("encrypt %q: %v", plaintext, err)
		}
		return c
	}

	// Provider API keys.
	for _, row := range []struct{ key, val string }{
		{"anthropic_api_key", "sk-ant-test"},
		{"openai_api_key", "sk-openai-test"},
		{"google_api_key", "sk-google-test"},
	} {
		if err := s.Queries().UpsertSystemSetting(ctx, db.UpsertSystemSettingParams{
			Key:       row.key,
			Value:     encryptWith(row.val),
			UpdatedAt: now,
		}); err != nil {
			t.Fatalf("seed system_settings %s: %v", row.key, err)
		}
	}

	// Non-API-key setting — must survive rotation untouched.
	if err := s.Queries().UpsertSystemSetting(ctx, db.UpsertSystemSettingParams{
		Key:       "public_url",
		Value:     "https://gleipnir.example.com",
		UpdatedAt: now,
	}); err != nil {
		t.Fatalf("seed public_url: %v", err)
	}

	// OpenAI-compat providers.
	for _, p := range []struct{ name, baseURL, apiKey string }{
		{"my-llm", "https://my-llm.example.com/v1", "mykey-1"},
		{"other-llm", "https://other-llm.example.com/v1", "mykey-2"},
	} {
		if _, err := s.Queries().CreateOpenAICompatProvider(ctx, db.CreateOpenAICompatProviderParams{
			Name:            p.name,
			BaseUrl:         p.baseURL,
			ApiKeyEncrypted: encryptWith(p.apiKey),
			CreatedAt:       now,
			UpdatedAt:       now,
		}); err != nil {
			t.Fatalf("seed openai_compat_providers %s: %v", p.name, err)
		}
	}

	// Policy with webhook secret.
	testutil.InsertPolicy(t, s, "policy-with-secret", "with-secret", "webhook", testutil.MinimalWebhookPolicy)
	ciphertext := encryptWith("wh-secret-value")
	if err := s.Queries().SetPolicyWebhookSecret(ctx, db.SetPolicyWebhookSecretParams{
		Ciphertext: &ciphertext,
		UpdatedAt:  now,
		ID:         "policy-with-secret",
	}); err != nil {
		t.Fatalf("seed webhook secret: %v", err)
	}

	// Policy without webhook secret.
	testutil.InsertPolicy(t, s, "policy-no-secret", "no-secret", "manual", testutil.MinimalWebhookPolicy)
}

// storePath returns the filesystem path of the Store's underlying SQLite file.
func storePath(t *testing.T, s *db.Store) string {
	t.Helper()
	var seq int
	var name, path string
	if err := s.DB().QueryRowContext(context.Background(), "PRAGMA database_list").Scan(&seq, &name, &path); err != nil {
		t.Fatalf("PRAGMA database_list: %v", err)
	}
	return path
}

// openForVerify opens a fresh store at path. The caller must close it.
func openForVerify(t *testing.T, path string) *db.Store {
	t.Helper()
	s, err := db.Open(path)
	if err != nil {
		t.Fatalf("open for verify: %v", err)
	}
	return s
}

// TestRotate_RoundTripsAllThreeSecretTypes verifies that all three secret
// categories are re-encrypted with the new key, can no longer be decrypted
// with the old key, and that non-API-key settings are left intact.
func TestRotate_RoundTripsAllThreeSecretTypes(t *testing.T) {
	path := newSeededPath(t, keyA)

	var stdout, stderr bytes.Buffer
	code := rotatekey.Run([]string{"--old", keyA, "--new", keyB, "--db-path", path}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected exit 0, got %d; stderr: %s", code, stderr.String())
	}

	out := stdout.String()
	if !strings.Contains(out, "3 provider keys") {
		t.Errorf("summary missing '3 provider keys': %q", out)
	}
	if !strings.Contains(out, "2 openai-compat keys") {
		t.Errorf("summary missing '2 openai-compat keys': %q", out)
	}
	if !strings.Contains(out, "1 webhook secrets") {
		t.Errorf("summary missing '1 webhook secrets': %q", out)
	}

	// Re-open to inspect post-rotation state.
	s := openForVerify(t, path)
	defer s.Close()

	ctx := context.Background()
	newKey := mustKey(keyB)
	oldKey := mustKey(keyA)

	// Verify system_settings API keys decrypt with new key and not old key.
	for _, key := range []string{"anthropic_api_key", "openai_api_key", "google_api_key"} {
		row, err := s.Queries().GetSystemSetting(ctx, key)
		if err != nil {
			t.Fatalf("get %s: %v", key, err)
		}
		if _, err := admin.Decrypt(newKey, row.Value); err != nil {
			t.Errorf("%s: decrypt with new key failed: %v", key, err)
		}
		if _, err := admin.Decrypt(oldKey, row.Value); err == nil {
			t.Errorf("%s: old key should no longer decrypt ciphertext", key)
		}
	}

	// Verify public_url is byte-identical to the original seeded value.
	publicURL, err := s.Queries().GetSystemSetting(ctx, "public_url")
	if err != nil {
		t.Fatalf("get public_url: %v", err)
	}
	if publicURL.Value != "https://gleipnir.example.com" {
		t.Errorf("public_url changed: got %q", publicURL.Value)
	}

	// Verify openai_compat_providers API keys.
	compatRows, err := s.Queries().ListOpenAICompatProviders(ctx)
	if err != nil {
		t.Fatalf("list compat providers: %v", err)
	}
	if len(compatRows) != 2 {
		t.Fatalf("expected 2 compat rows, got %d", len(compatRows))
	}
	for _, row := range compatRows {
		if _, err := admin.Decrypt(newKey, row.ApiKeyEncrypted); err != nil {
			t.Errorf("compat provider %q: decrypt with new key failed: %v", row.Name, err)
		}
		if _, err := admin.Decrypt(oldKey, row.ApiKeyEncrypted); err == nil {
			t.Errorf("compat provider %q: old key should no longer decrypt ciphertext", row.Name)
		}
	}

	// Verify webhook secrets.
	webhookRows, err := s.Queries().ListPolicyWebhookSecrets(ctx)
	if err != nil {
		t.Fatalf("list webhook secrets: %v", err)
	}
	if len(webhookRows) != 1 {
		t.Fatalf("expected 1 webhook secret row, got %d", len(webhookRows))
	}
	for _, row := range webhookRows {
		if row.WebhookSecretEncrypted == nil {
			t.Fatal("webhook_secret_encrypted is nil")
		}
		if _, err := admin.Decrypt(newKey, *row.WebhookSecretEncrypted); err != nil {
			t.Errorf("policy %s: decrypt webhook secret with new key failed: %v", row.ID, err)
		}
		if _, err := admin.Decrypt(oldKey, *row.WebhookSecretEncrypted); err == nil {
			t.Errorf("policy %s: old key should no longer decrypt webhook secret", row.ID)
		}
	}
}

// TestRotate_DryRunRollsBack verifies that --dry-run performs the validation
// round-trip but does not persist any changes.
func TestRotate_DryRunRollsBack(t *testing.T) {
	// Snapshot original ciphertexts before closing seeding store.
	s := testutil.NewTestStore(t)
	seedDB(t, s, mustKey(keyA))
	path := storePath(t, s)

	ctx := context.Background()
	originalRows, err := s.Queries().ListAPIKeySystemSettings(ctx)
	if err != nil {
		t.Fatalf("list api key settings: %v", err)
	}
	origByKey := make(map[string]string, len(originalRows))
	for _, r := range originalRows {
		origByKey[r.Key] = r.Value
	}

	// Close so Run can take the write lock.
	s.Close()

	var stdout, stderr bytes.Buffer
	code := rotatekey.Run([]string{"--old", keyA, "--new", keyB, "--dry-run", "--db-path", path}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected exit 0, got %d; stderr: %s", code, stderr.String())
	}

	if !strings.Contains(stdout.String(), "(dry-run; no changes written)") {
		t.Errorf("summary missing dry-run notice: %q", stdout.String())
	}

	// Re-open to verify ciphertexts are unchanged.
	s2 := openForVerify(t, path)
	defer s2.Close()

	oldKey := mustKey(keyA)
	afterRows, err := s2.Queries().ListAPIKeySystemSettings(ctx)
	if err != nil {
		t.Fatalf("list api key settings after dry-run: %v", err)
	}
	for _, row := range afterRows {
		if _, err := admin.Decrypt(oldKey, row.Value); err != nil {
			t.Errorf("%s: ciphertext changed after dry-run (should still decrypt with old key): %v", row.Key, err)
		}
		if origByKey[row.Key] != row.Value {
			t.Errorf("%s: ciphertext bytes changed after dry-run", row.Key)
		}
	}
}

// TestRotate_WrongOldKeyFailsWithoutPartialWrites verifies that if any secret
// fails to decrypt, the transaction rolls back and no partial writes are committed.
func TestRotate_WrongOldKeyFailsWithoutPartialWrites(t *testing.T) {
	s := testutil.NewTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Format(time.RFC3339)

	// Encrypt anthropic with keyA, openai with keyB — mixing keys so keyA alone
	// cannot decrypt the openai row.
	antCipher, err := admin.Encrypt(mustKey(keyA), "sk-ant-test")
	if err != nil {
		t.Fatalf("encrypt anthropic: %v", err)
	}
	openaiCipher, err := admin.Encrypt(mustKey(keyB), "sk-openai-test")
	if err != nil {
		t.Fatalf("encrypt openai: %v", err)
	}

	if err := s.Queries().UpsertSystemSetting(ctx, db.UpsertSystemSettingParams{Key: "anthropic_api_key", Value: antCipher, UpdatedAt: now}); err != nil {
		t.Fatalf("seed anthropic: %v", err)
	}
	if err := s.Queries().UpsertSystemSetting(ctx, db.UpsertSystemSettingParams{Key: "openai_api_key", Value: openaiCipher, UpdatedAt: now}); err != nil {
		t.Fatalf("seed openai: %v", err)
	}

	path := storePath(t, s)
	s.Close()

	var stdout, stderr bytes.Buffer
	// Try to rotate with keyA as old key — will fail when it hits openai_api_key.
	code := rotatekey.Run([]string{"--old", keyA, "--new", keyC, "--db-path", path}, &stdout, &stderr)
	if code == 0 {
		t.Fatal("expected non-zero exit code on decrypt failure")
	}

	// Rollback must have held — anthropic_api_key must still decrypt with keyA.
	s2 := openForVerify(t, path)
	defer s2.Close()

	row, err := s2.Queries().GetSystemSetting(ctx, "anthropic_api_key")
	if err != nil {
		t.Fatalf("get anthropic_api_key: %v", err)
	}
	if _, err := admin.Decrypt(mustKey(keyA), row.Value); err != nil {
		t.Errorf("anthropic_api_key should still decrypt with keyA after rollback: %v", err)
	}
}

// TestRotate_RejectsInvalidKeys verifies that malformed key strings produce
// exit code 2 with a clear error message.
func TestRotate_RejectsInvalidKeys(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		wantMsg string
	}{
		{
			name:    "bad hex in old",
			args:    []string{"--old", "not-a-valid-key", "--new", keyB},
			wantMsg: "parse --old key",
		},
		{
			name:    "too short old key",
			args:    []string{"--old", "aabbcc", "--new", keyB},
			wantMsg: "parse --old key",
		},
		{
			name:    "bad new key",
			args:    []string{"--old", keyA, "--new", "not-valid"},
			wantMsg: "parse --new key",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := rotatekey.Run(tc.args, &stdout, &stderr)
			if code != 2 {
				t.Errorf("expected exit 2, got %d", code)
			}
			if !strings.Contains(stderr.String(), tc.wantMsg) {
				t.Errorf("expected %q in stderr, got: %q", tc.wantMsg, stderr.String())
			}
		})
	}
}

// TestRotate_RejectsEqualKeys verifies that supplying the same key for --old
// and --new exits with code 2 and a "nothing to do" message.
func TestRotate_RejectsEqualKeys(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := rotatekey.Run([]string{"--old", keyA, "--new", keyA}, &stdout, &stderr)
	if code != 2 {
		t.Errorf("expected exit 2, got %d", code)
	}
	if !strings.Contains(stderr.String(), "nothing to do") {
		t.Errorf("expected 'nothing to do' in stderr, got: %q", stderr.String())
	}
}

// TestRotate_ServerHoldingDBIsRefused verifies that rotate-key exits with code
// 3 when another connection holds the write lock, and succeeds after that
// connection is released.
func TestRotate_ServerHoldingDBIsRefused(t *testing.T) {
	// Create and fully close the seeded store so the file exists on disk.
	s := testutil.NewTestStore(t)
	path := storePath(t, s)
	s.Close()

	// Open a second connection simulating the live server.
	holder, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open holder db: %v", err)
	}
	defer holder.Close()

	ctx := context.Background()

	// Begin a transaction and perform an actual write so the WAL write lock is held.
	// A bare BEGIN (DEFERRED) does not acquire the write lock until the first write.
	holderTx, err := holder.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin holder tx: %v", err)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	_, err = holderTx.ExecContext(ctx,
		`INSERT INTO system_settings(key, value, updated_at) VALUES ('probe_lock', 'x', ?)`, now,
	)
	if err != nil {
		t.Fatalf("holder INSERT: %v", err)
	}
	// Do NOT commit or roll back yet — the write lock must be held during Run.

	var stdout, stderr bytes.Buffer
	code := rotatekey.Run([]string{"--old", keyA, "--new", keyB, "--db-path", path}, &stdout, &stderr)
	if code != 3 {
		t.Errorf("expected exit 3 while DB is held, got %d; stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "stop the server first") {
		t.Errorf("expected 'stop the server first' in stderr, got: %q", stderr.String())
	}

	// Release the holder.
	_ = holderTx.Rollback()
	holder.Close()

	// Now rotate-key should succeed on an empty DB (no secrets to rotate).
	stdout.Reset()
	stderr.Reset()
	code = rotatekey.Run([]string{"--old", keyA, "--new", keyB, "--db-path", path}, &stdout, &stderr)
	if code != 0 {
		t.Errorf("expected exit 0 after releasing holder, got %d; stderr: %s", code, stderr.String())
	}
}

// TestRotate_ReadsKeysFromStdin verifies that "--old -" and "--new -" cause
// the command to read key material from the supplied stdin reader.
func TestRotate_ReadsKeysFromStdin(t *testing.T) {
	path := newSeededPath(t, keyA)

	// Supply both keys on stdin, one per line.
	stdinContent := keyA + "\n" + keyB + "\n"
	stdinReader := strings.NewReader(stdinContent)

	var stdout, stderr bytes.Buffer
	code := rotatekey.RunWithIO([]string{"--old", "-", "--new", "-", "--db-path", path}, stdinReader, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected exit 0, got %d; stderr: %s", code, stderr.String())
	}

	// Verify rotation took effect: API keys now decrypt with keyB, not keyA.
	s := openForVerify(t, path)
	defer s.Close()

	ctx := context.Background()
	row, err := s.Queries().GetSystemSetting(ctx, "anthropic_api_key")
	if err != nil {
		t.Fatalf("get anthropic_api_key: %v", err)
	}
	if _, err := admin.Decrypt(mustKey(keyB), row.Value); err != nil {
		t.Errorf("anthropic_api_key should decrypt with keyB after stdin rotation: %v", err)
	}
	if _, err := admin.Decrypt(mustKey(keyA), row.Value); err == nil {
		t.Error("anthropic_api_key should no longer decrypt with keyA after stdin rotation")
	}
}

// TestRotate_StorePath uses a custom --db-path to verify the flag is respected.
func TestRotate_StorePath(t *testing.T) {
	// Use a separate temp dir so there is no pre-existing store.
	dir := t.TempDir()
	customPath := filepath.Join(dir, "custom.db")

	// Open and migrate a fresh store at the custom path, then close it.
	cs, err := db.Open(customPath)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	if err := cs.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	cs.Close()

	// No secrets seeded — summary should show 0 for all counts.
	var stdout, stderr bytes.Buffer
	code := rotatekey.Run([]string{"--old", keyA, "--new", keyB, "--db-path", customPath}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected exit 0, got %d; stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "0 provider keys, 0 openai-compat keys, 0 webhook secrets") {
		t.Errorf("unexpected summary: %q", stdout.String())
	}
}
