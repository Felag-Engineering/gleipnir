package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/rapp992/gleipnir/internal/db"
	"github.com/rapp992/gleipnir/internal/http/auth"
	"github.com/rapp992/gleipnir/internal/testutil"
)

// seedUser inserts a user with a placeholder password hash and returns the user ID.
func seedUser(t *testing.T, s *db.Store, username string) string {
	t.Helper()
	const testUserID = "01HZZZZZZZZZZZZZZZZZZZZZZZ"
	hash, err := auth.HashPassword("placeholder-password")
	if err != nil {
		t.Fatalf("hash placeholder password: %v", err)
	}
	_, err = s.Queries().CreateUser(context.Background(), db.CreateUserParams{
		ID:           testUserID,
		Username:     username,
		PasswordHash: hash,
		CreatedAt:    time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("seed user %q: %v", username, err)
	}
	return testUserID
}

func TestResetPassword_SuccessWithExplicitPassword(t *testing.T) {
	s := testutil.NewTestStore(t)
	seedUser(t, s, "alice")
	path := storePath(t, s)
	s.Close()

	var stdout, stderr bytes.Buffer
	code := ResetPassword(context.Background(), path, "alice", "new-password-1234", &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected exit 0, got %d; stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "password reset for user alice") {
		t.Errorf("stdout missing confirmation: %q", stdout.String())
	}

	// Verify the new password hash is accepted.
	s2 := openForVerify(t, path)
	defer s2.Close()
	user, err := s2.Queries().GetUserByUsername(context.Background(), "alice")
	if err != nil {
		t.Fatalf("get user: %v", err)
	}
	if err := auth.CheckPassword(user.PasswordHash, "new-password-1234"); err != nil {
		t.Errorf("CheckPassword failed after reset: %v", err)
	}
}

func TestResetPassword_UnknownUserReturns4(t *testing.T) {
	s := testutil.NewTestStore(t)
	path := storePath(t, s)
	s.Close()

	var stdout, stderr bytes.Buffer
	code := ResetPassword(context.Background(), path, "ghost", "some-password", &stdout, &stderr)
	if code != 4 {
		t.Fatalf("expected exit 4, got %d; stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), `user "ghost" not found`) {
		t.Errorf("stderr missing user-not-found message: %q", stderr.String())
	}
}

func TestResetPassword_ShortPasswordRejected(t *testing.T) {
	s := testutil.NewTestStore(t)
	seedUser(t, s, "alice")
	path := storePath(t, s)
	s.Close()

	var stdout, stderr bytes.Buffer
	code := ResetPassword(context.Background(), path, "alice", "short", &stdout, &stderr)
	if code != 2 {
		t.Fatalf("expected exit 2, got %d; stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "at least 8 characters") {
		t.Errorf("stderr missing minimum-length message: %q", stderr.String())
	}
}

// TestResetPassword_GeneratedPasswordRoundtrip exercises the full Cobra wiring:
// no --password flag, so a random password is generated, printed to stdout, and
// the resulting hash must be accepted by auth.CheckPassword.
func TestResetPassword_GeneratedPasswordRoundtrip(t *testing.T) {
	s := testutil.NewTestStore(t)
	seedUser(t, s, "alice")
	path := storePath(t, s)
	s.Close()

	cmd := newResetPasswordCmd()
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"alice", "--db-path", path})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("command failed: %v; stderr: %s", err, stderr.String())
	}

	out := stdout.String()
	// Extract the generated password from the "generated password: <pwd>" line.
	var generatedPwd string
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "generated password: ") {
			generatedPwd = strings.TrimPrefix(line, "generated password: ")
			break
		}
	}
	if generatedPwd == "" {
		t.Fatalf("generated password line not found in stdout: %q", out)
	}
	if !strings.Contains(out, "password reset for user alice") {
		t.Errorf("confirmation line missing from stdout: %q", out)
	}

	// Verify the generated password hash stored in DB.
	s2 := openForVerify(t, path)
	defer s2.Close()
	user, err := s2.Queries().GetUserByUsername(context.Background(), "alice")
	if err != nil {
		t.Fatalf("get user: %v", err)
	}
	if err := auth.CheckPassword(user.PasswordHash, generatedPwd); err != nil {
		t.Errorf("CheckPassword rejected generated password: %v", err)
	}
}
