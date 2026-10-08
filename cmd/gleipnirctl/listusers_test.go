package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/http/auth"
	"github.com/felag-engineering/gleipnir/internal/testutil"
)

func seedUserWithRoles(t *testing.T, s *db.Store, id, username string, deactivated bool, roles ...string) string {
	t.Helper()
	ctx := context.Background()
	hash, err := auth.HashPassword("placeholder-password")
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := s.Queries().CreateUser(ctx, db.CreateUserParams{
		ID: id, Username: username, PasswordHash: hash, CreatedAt: now,
	}); err != nil {
		t.Fatalf("create user %q: %v", username, err)
	}
	for _, role := range roles {
		if err := s.Queries().AssignRole(ctx, db.AssignRoleParams{UserID: id, Role: role, CreatedAt: now}); err != nil {
			t.Fatalf("assign role %q: %v", role, err)
		}
	}
	if deactivated {
		if err := s.Queries().DeactivateUser(ctx, db.DeactivateUserParams{DeactivatedAt: &now, ID: id}); err != nil {
			t.Fatalf("deactivate %q: %v", username, err)
		}
	}
	return hash
}

func TestListUsers_EmptyTable(t *testing.T) {
	s := testutil.NewTestStore(t)
	path := storePath(t, s)
	s.Close()

	var stdout, stderr bytes.Buffer
	if code := ListUsers(context.Background(), path, &stdout, &stderr); code != 0 {
		t.Fatalf("expected exit 0, got %d; stderr: %s", code, stderr.String())
	}
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected header + no-users line, got %q", stdout.String())
	}
	if !strings.HasPrefix(lines[0], "USERNAME") {
		t.Errorf("first line is not the header: %q", lines[0])
	}
	if lines[1] != "no users" {
		t.Errorf("expected %q, got %q", "no users", lines[1])
	}
}

func TestListUsers_MultipleUsersAndRoles(t *testing.T) {
	s := testutil.NewTestStore(t)
	// Inserted out of username order to prove sorting; ids sort opposite to usernames.
	hashZed := seedUserWithRoles(t, s, "01HZZZZZZZZZZZZZZZZZZZZZZA", "zed", false, "auditor")
	hashAlice := seedUserWithRoles(t, s, "01HZZZZZZZZZZZZZZZZZZZZZZB", "alice", false, "operator", "admin")
	hashBob := seedUserWithRoles(t, s, "01HZZZZZZZZZZZZZZZZZZZZZZC", "bob", true, "approver")
	seedUserWithRoles(t, s, "01HZZZZZZZZZZZZZZZZZZZZZZD", "norole", false)
	path := storePath(t, s)
	s.Close()

	var stdout, stderr bytes.Buffer
	if code := ListUsers(context.Background(), path, &stdout, &stderr); code != 0 {
		t.Fatalf("expected exit 0, got %d; stderr: %s", code, stderr.String())
	}
	out := stdout.String()

	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 5 {
		t.Fatalf("expected header + 4 rows, got %d lines: %q", len(lines), out)
	}
	wantRows := []struct{ username, roles, status string }{
		{"alice", "admin,operator", "active"},
		{"bob", "approver", "deactivated"},
		{"norole", "-", "active"},
		{"zed", "auditor", "active"},
	}
	for i, want := range wantRows {
		fields := strings.Fields(lines[i+1])
		if len(fields) != 4 {
			t.Fatalf("row %d: expected 4 columns, got %q", i, lines[i+1])
		}
		if fields[0] != want.username || fields[1] != want.roles || fields[3] != want.status {
			t.Errorf("row %d: got %v, want %+v", i, fields, want)
		}
	}

	for _, hash := range []string{hashZed, hashAlice, hashBob} {
		if strings.Contains(out, hash) {
			t.Errorf("output contains a password hash: %q", out)
		}
	}
	for _, marker := range []string{"$argon2", "$2a$", "$2b$", "password"} {
		if strings.Contains(strings.ToLower(out), strings.ToLower(marker)) {
			t.Errorf("output contains credential marker %q: %q", marker, out)
		}
	}
}

func TestListUsers_BadDBPathReturns1(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := ListUsers(context.Background(), "/nonexistent-dir/x/gleipnir.db", &stdout, &stderr)
	if code != 1 {
		t.Fatalf("expected exit 1, got %d", code)
	}
	if !strings.Contains(stderr.String(), "error:") {
		t.Errorf("stderr missing error: %q", stderr.String())
	}
}
