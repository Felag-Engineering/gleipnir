package main

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/felag-engineering/gleipnir/internal/infra/crypto"
	"github.com/felag-engineering/gleipnir/internal/testutil"
)

// newCheckPath returns a migrated DB seeded with secrets under keyA and one
// active admin user.
func newCheckPath(t *testing.T) string {
	t.Helper()
	s := testutil.NewTestStore(t)
	seedDB(t, s, mustKey(keyA))
	seedPluginCredentials(t, s, mustKey(keyA), "inst-1")
	seedUserWithRoles(t, s, "u-admin", "root", false, "admin")
	path := storePath(t, s)
	s.Close()
	return path
}

func runCheck(t *testing.T, path, key string) (int, string) {
	t.Helper()
	var out bytes.Buffer
	code := Check(context.Background(), path, key, &out)
	return code, out.String()
}

func TestCheck(t *testing.T) {
	tests := []struct {
		name     string
		setup    func(t *testing.T) string
		key      string
		wantCode int
		want     []string
		notWant  []string
	}{
		{
			name:     "healthy instance",
			setup:    newCheckPath,
			key:      keyA,
			wantCode: 0,
			want: []string{
				"PASS  database reachable",
				"PASS  schema migrated",
				"PASS  encryption key present and valid",
				"PASS  encryption key decrypts stored secrets: 8 secrets",
				"PASS  active admin user: 1",
			},
			notWant: []string{"FAIL", "WARN"},
		},
		{
			name:     "missing key",
			setup:    newCheckPath,
			key:      "",
			wantCode: 1,
			want: []string{
				"FAIL  encryption key present and valid: GLEIPNIR_ENCRYPTION_KEY is not set",
				"SKIP  encryption key decrypts stored secrets",
				"PASS  active admin user",
			},
		},
		{
			name:     "malformed key",
			setup:    newCheckPath,
			key:      "tooshort",
			wantCode: 1,
			want:     []string{"FAIL  encryption key present and valid", "SKIP  encryption key decrypts stored secrets"},
		},
		{
			name:     "wrong key lists the failing rows",
			setup:    newCheckPath,
			key:      keyB,
			wantCode: 1,
			want: []string{
				"PASS  encryption key present and valid",
				"FAIL  encryption key decrypts stored secrets: 8 failed, 0 verified OK",
				"undecryptable secret: policies.webhook_secret_encrypted id=policy-with-secret",
			},
		},
		{
			name: "one corrupt ciphertext",
			setup: func(t *testing.T) string {
				path := newCheckPath(t)
				s := openForVerify(t, path)
				defer s.Close()
				other, err := crypto.Encrypt(mustKey(keyC), "x")
				if err != nil {
					t.Fatal(err)
				}
				if _, err := s.DB().Exec(`UPDATE openai_compat_providers SET api_key_encrypted = ? WHERE id = (SELECT MIN(id) FROM openai_compat_providers)`, other); err != nil {
					t.Fatal(err)
				}
				return path
			},
			key:      keyA,
			wantCode: 1,
			want:     []string{"FAIL  encryption key decrypts stored secrets: 1 failed, 7 verified OK", "openai_compat_providers.api_key_encrypted"},
		},
		{
			name: "no admin user is a warning",
			setup: func(t *testing.T) string {
				s := testutil.NewTestStore(t)
				seedDB(t, s, mustKey(keyA))
				seedUserWithRoles(t, s, "u-op", "op", false, "operator")
				seedUserWithRoles(t, s, "u-old", "old-admin", true, "admin")
				path := storePath(t, s)
				s.Close()
				return path
			},
			key:      keyA,
			wantCode: 0,
			want:     []string{"WARN  active admin user: none found", "PASS  encryption key decrypts stored secrets"},
			notWant:  []string{"FAIL"},
		},
		{
			name: "unmigrated: partial schema",
			setup: func(t *testing.T) string {
				path := filepath.Join(t.TempDir(), "old.db")
				raw, err := sql.Open("sqlite", path)
				if err != nil {
					t.Fatal(err)
				}
				defer raw.Close()
				if _, err := raw.Exec(`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
					t.Fatal(err)
				}
				return path
			},
			key:      keyA,
			wantCode: 1,
			want: []string{
				"PASS  database reachable",
				"FAIL  schema migrated:",
				"pending",
				"SKIP  encryption key decrypts stored secrets: schema not migrated",
				"SKIP  active admin user: schema not migrated",
			},
		},
		{
			name: "unmigrated: empty file",
			setup: func(t *testing.T) string {
				path := filepath.Join(t.TempDir(), "empty.db")
				if err := os.WriteFile(path, nil, 0o600); err != nil {
					t.Fatal(err)
				}
				return path
			},
			key:      keyA,
			wantCode: 1,
			want:     []string{"FAIL  schema migrated: schema not initialised"},
		},
		{
			name:     "database missing",
			setup:    func(t *testing.T) string { return filepath.Join(t.TempDir(), "nope.db") },
			key:      keyA,
			wantCode: 1,
			want:     []string{"FAIL  database reachable", "SKIP  schema migrated: database not reachable"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := tc.setup(t)
			code, out := runCheck(t, path, tc.key)
			if code != tc.wantCode {
				t.Errorf("exit = %d, want %d; output:\n%s", code, tc.wantCode, out)
			}
			for _, w := range tc.want {
				if !strings.Contains(out, w) {
					t.Errorf("output missing %q:\n%s", w, out)
				}
			}
			for _, nw := range tc.notWant {
				if strings.Contains(out, nw) {
					t.Errorf("output unexpectedly contains %q:\n%s", nw, out)
				}
			}
			if tc.name == "database missing" {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Errorf("check created %s", path)
				}
			}
		})
	}
}

// TestCheck_DoesNotModifyDatabase pins the read-only contract: an unmigrated
// DB stays unmigrated and a healthy one is byte-identical afterwards.
func TestCheck_DoesNotModifyDatabase(t *testing.T) {
	path := newCheckPath(t)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	if code, out := runCheck(t, path, keyA); code != 0 {
		t.Fatalf("exit = %d:\n%s", code, out)
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Error("check modified the database file")
	}
}
