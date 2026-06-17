// Package main implements the gleipnirctl local admin CLI.
//
// create_user.go contains the core user-creation logic: CreateUser inserts a new
// user row and assigns a role in a single transaction. The Cobra command wiring
// lives in createuser.go.
package main

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/http/auth"
	"github.com/felag-engineering/gleipnir/internal/model"
)

// CreateUser creates a new user with username, role, and password in the database
// at dbPath. If password is empty the caller is responsible for generating one
// before calling this function. Returns a shell exit code:
//
//	0 — success
//	1 — unexpected error (I/O, DB error, hashing failure)
//	2 — bad input (empty username, invalid role, password shorter than 8 chars)
//	4 — username already exists
func CreateUser(ctx context.Context, dbPath, username, role, password string, out, errOut io.Writer) int {
	if username == "" {
		fmt.Fprintln(errOut, "error: username must not be empty")
		return 2
	}
	if !model.Role(role).Valid() {
		fmt.Fprintf(errOut, "error: invalid role %q; must be one of: admin, operator, approver, auditor\n", role)
		return 2
	}
	if len(password) < 8 {
		fmt.Fprintln(errOut, "error: password must be at least 8 characters")
		return 2
	}

	store, err := db.Open(dbPath)
	if err != nil {
		fmt.Fprintf(errOut, "error: open db: %v\n", err)
		return 1
	}
	defer store.Close()

	// Migration is idempotent; ensures the command works against a restored
	// backup that may be on an older schema version.
	if err := store.Migrate(ctx); err != nil {
		fmt.Fprintf(errOut, "error: migrate db: %v\n", err)
		return 1
	}

	hash, err := auth.HashPassword(password)
	if err != nil {
		fmt.Fprintf(errOut, "error: hash password: %v\n", err)
		return 1
	}

	// CreateUser + AssignRole in a single transaction so a failed role assignment
	// cannot leave an orphaned user row.
	tx, err := store.DB().BeginTx(ctx, nil)
	if err != nil {
		fmt.Fprintf(errOut, "error: begin transaction: %v\n", err)
		return 1
	}
	defer tx.Rollback() //nolint:errcheck

	q := store.Queries().WithTx(tx)
	now := time.Now().UTC().Format(time.RFC3339)

	user, err := q.CreateUser(ctx, db.CreateUserParams{
		ID:           model.NewULID(),
		Username:     username,
		PasswordHash: hash,
		CreatedAt:    now,
	})
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			fmt.Fprintf(errOut, "error: user %q already exists\n", username)
			return 4
		}
		fmt.Fprintf(errOut, "error: create user %q: %v\n", username, err)
		return 1
	}

	// AssignRole is INSERT OR IGNORE; the IGNORE path is unreachable here because
	// user.ID is freshly generated and cannot already have a role row.
	if err := q.AssignRole(ctx, db.AssignRoleParams{
		UserID:    user.ID,
		Role:      role,
		CreatedAt: now,
	}); err != nil {
		fmt.Fprintf(errOut, "error: assign role %q to %q: %v\n", role, username, err)
		return 1
	}

	if err := tx.Commit(); err != nil {
		fmt.Fprintf(errOut, "error: commit transaction: %v\n", err)
		return 1
	}

	fmt.Fprintf(out, "created user %s with role %s\n", username, role)
	return 0
}
