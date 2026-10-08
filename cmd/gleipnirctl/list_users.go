package main

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/felag-engineering/gleipnir/internal/db"
)

// ListUsers prints a table of all users (username, roles, created_at, status)
// from the database at dbPath, ordered by username. It uses the ListUsers and
// ListAllUserRoles queries, neither of which selects password_hash, so no
// credential material is ever loaded. Returns a shell exit code:
//
//	0 — success (including an empty user table)
//	1 — unexpected error (I/O, DB error)
func ListUsers(ctx context.Context, dbPath string, out, errOut io.Writer) int {
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

	q := store.Queries()
	users, err := q.ListUsers(ctx)
	if err != nil {
		fmt.Fprintf(errOut, "error: list users: %v\n", err)
		return 1
	}
	roleRows, err := q.ListAllUserRoles(ctx)
	if err != nil {
		fmt.Fprintf(errOut, "error: list user roles: %v\n", err)
		return 1
	}

	// ListAllUserRoles is ordered by (user_id, role), so each user's roles are sorted.
	rolesByUser := make(map[string][]string)
	for _, r := range roleRows {
		rolesByUser[r.UserID] = append(rolesByUser[r.UserID], r.Role)
	}

	sort.Slice(users, func(i, j int) bool { return users[i].Username < users[j].Username })

	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "USERNAME\tROLES\tCREATED_AT\tSTATUS")
	for _, u := range users {
		roles := "-"
		if rs := rolesByUser[u.ID]; len(rs) > 0 {
			roles = strings.Join(rs, ",")
		}
		status := "active"
		if u.DeactivatedAt != nil {
			status = "deactivated"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", u.Username, roles, u.CreatedAt, status)
	}
	if err := tw.Flush(); err != nil {
		fmt.Fprintf(errOut, "error: write table: %v\n", err)
		return 1
	}

	if len(users) == 0 {
		fmt.Fprintln(out, "no users")
	}
	return 0
}
