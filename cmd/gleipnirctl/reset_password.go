// Package main implements the gleipnirctl local admin CLI.
//
// reset_password.go contains the core password-reset logic: ResetPassword looks
// up a user by username, hashes the new password with the same bcrypt cost as
// the server, and writes it in a single UPDATE. The Cobra command wiring lives
// in resetpassword.go.
package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"io"

	"github.com/rapp992/gleipnir/internal/db"
	"github.com/rapp992/gleipnir/internal/http/auth"
)

// ResetPassword resets the password for username in the database at dbPath.
// If password is empty the caller is responsible for generating one before
// calling this function. Returns a shell exit code:
//
//	0 — success
//	1 — unexpected error (I/O, DB error, hashing failure)
//	2 — bad input (empty username, password shorter than 8 chars)
//	4 — user not found
func ResetPassword(ctx context.Context, dbPath, username, password string, out, errOut io.Writer) int {
	if username == "" {
		fmt.Fprintln(errOut, "error: username must not be empty")
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

	user, err := store.Queries().GetUserByUsername(ctx, username)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			fmt.Fprintf(errOut, "error: user %q not found\n", username)
			return 4
		}
		fmt.Fprintf(errOut, "error: look up user %q: %v\n", username, err)
		return 1
	}

	hash, err := auth.HashPassword(password)
	if err != nil {
		fmt.Fprintf(errOut, "error: hash password: %v\n", err)
		return 1
	}

	// No lockout columns exist in the users table today (see schemas/sql_schemas.sql);
	// this is a no-op placeholder for future lockout state.
	if err := store.Queries().UpdateUserPassword(ctx, db.UpdateUserPasswordParams{
		PasswordHash: hash,
		ID:           user.ID,
	}); err != nil {
		fmt.Fprintf(errOut, "error: update password for %q: %v\n", username, err)
		return 1
	}

	fmt.Fprintf(out, "password reset for user %s\n", username)
	return 0
}

// generateRandomPassword returns a 24-character URL-safe base64 string derived
// from 18 random bytes (144 bits of entropy). URL-safe base64 without padding
// avoids characters (+, /, =) that require shell quoting.
func generateRandomPassword() (string, error) {
	buf := make([]byte, 18)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate random password: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
