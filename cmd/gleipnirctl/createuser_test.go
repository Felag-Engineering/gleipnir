package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/felag-engineering/gleipnir/internal/http/auth"
	"github.com/felag-engineering/gleipnir/internal/testutil"
)

func TestCreateUser_SuccessWithExplicitPassword(t *testing.T) {
	s := testutil.NewTestStore(t)
	path := storePath(t, s)
	s.Close()

	var stdout, stderr bytes.Buffer
	code := CreateUser(context.Background(), path, "bob", "operator", "explicit-password-1234", &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected exit 0, got %d; stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "created user bob with role operator") {
		t.Errorf("stdout missing confirmation: %q", stdout.String())
	}

	// Verify the password hash and role assignment.
	s2 := openForVerify(t, path)
	defer s2.Close()

	ctx := context.Background()
	user, err := s2.Queries().GetUserByUsername(ctx, "bob")
	if err != nil {
		t.Fatalf("get user: %v", err)
	}
	if err := auth.CheckPassword(user.PasswordHash, "explicit-password-1234"); err != nil {
		t.Errorf("CheckPassword failed after create: %v", err)
	}
	roles, err := s2.Queries().ListRolesByUser(ctx, user.ID)
	if err != nil {
		t.Fatalf("list roles: %v", err)
	}
	if len(roles) != 1 || roles[0] != "operator" {
		t.Errorf("expected roles [operator], got %v", roles)
	}
}

func TestCreateUser_DuplicateUsernameReturns4(t *testing.T) {
	s := testutil.NewTestStore(t)
	seedUser(t, s, "alice")
	path := storePath(t, s)
	s.Close()

	var stdout, stderr bytes.Buffer
	code := CreateUser(context.Background(), path, "alice", "admin", "some-password-1234", &stdout, &stderr)
	if code != 4 {
		t.Fatalf("expected exit 4, got %d; stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), `user "alice" already exists`) {
		t.Errorf("stderr missing already-exists message: %q", stderr.String())
	}
}

func TestCreateUser_InvalidRoleReturns2(t *testing.T) {
	s := testutil.NewTestStore(t)
	path := storePath(t, s)
	s.Close()

	var stdout, stderr bytes.Buffer
	code := CreateUser(context.Background(), path, "carol", "superuser", "valid-password-1234", &stdout, &stderr)
	if code != 2 {
		t.Fatalf("expected exit 2, got %d; stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "invalid role") {
		t.Errorf("stderr missing invalid-role message: %q", stderr.String())
	}

	// Verify role validation short-circuits before any DB write.
	s2 := openForVerify(t, path)
	defer s2.Close()
	users, err := s2.Queries().ListUsers(context.Background())
	if err != nil {
		t.Fatalf("list users: %v", err)
	}
	if len(users) != 0 {
		t.Errorf("expected no users written on invalid role, got %d", len(users))
	}
}

// TestCreateUser_GeneratedPasswordRoundtrip drives the full Cobra command with
// no --password flag, parses the "generated password: " line from stdout, and
// verifies the stored hash accepts that password.
func TestCreateUser_GeneratedPasswordRoundtrip(t *testing.T) {
	s := testutil.NewTestStore(t)
	path := storePath(t, s)
	s.Close()

	cmd := newCreateUserCmd()
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"dave", "--role", "admin", "--db-path", path})

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
	if !strings.Contains(out, "created user dave with role admin") {
		t.Errorf("confirmation line missing from stdout: %q", out)
	}

	// Verify the generated password hash stored in DB.
	s2 := openForVerify(t, path)
	defer s2.Close()
	user, err := s2.Queries().GetUserByUsername(context.Background(), "dave")
	if err != nil {
		t.Fatalf("get user: %v", err)
	}
	if err := auth.CheckPassword(user.PasswordHash, generatedPwd); err != nil {
		t.Errorf("CheckPassword rejected generated password: %v", err)
	}
}
