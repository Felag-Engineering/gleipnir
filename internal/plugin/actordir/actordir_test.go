package actordir_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/model"
	"github.com/felag-engineering/gleipnir/internal/plugin/actordir"
	"github.com/felag-engineering/gleipnir/internal/testutil"
)

func now() string { return time.Now().UTC().Format(time.RFC3339Nano) }

func insertInstance(t *testing.T, q *db.Queries, id string) {
	t.Helper()
	ctx := context.Background()
	if _, err := q.CreatePlugin(ctx, db.CreatePluginParams{
		ID: "plugin-" + id, Name: id, PluginVersion: "1.0.0", ManifestSnapshot: "{}",
		TrustedPubkey: "pk", Status: "active", CreatedAt: now(), UpdatedAt: now(),
	}); err != nil {
		t.Fatalf("CreatePlugin: %v", err)
	}
	detail := ""
	if _, err := q.CreatePluginInstance(ctx, db.CreatePluginInstanceParams{
		ID: id, PluginID: "plugin-" + id, InstanceName: id, ConfigJson: "{}",
		SubscriptionScopeJson: "{}", HandshakeVersions: "{}", HealthState: "healthy",
		HealthDetail: &detail, CreatedAt: now(), UpdatedAt: now(),
	}); err != nil {
		t.Fatalf("CreatePluginInstance: %v", err)
	}
}

func insertUser(t *testing.T, q *db.Queries, id string, roles ...model.Role) {
	t.Helper()
	ctx := context.Background()
	if _, err := q.CreateUser(ctx, db.CreateUserParams{ID: id, Username: id, PasswordHash: "x", CreatedAt: now()}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	for _, r := range roles {
		if err := q.AssignRole(ctx, db.AssignRoleParams{UserID: id, Role: string(r), CreatedAt: now()}); err != nil {
			t.Fatalf("AssignRole: %v", err)
		}
	}
}

func link(t *testing.T, q *db.Queries, instanceID, userID, externalID string) {
	t.Helper()
	if err := q.SetAdminIdentity(context.Background(), db.SetAdminIdentityParams{
		InstanceID: instanceID, UserID: userID, ExternalUserID: externalID, CreatedAt: now(),
	}); err != nil {
		t.Fatalf("SetAdminIdentity(%s, %s, %s): %v", instanceID, userID, externalID, err)
	}
}

func TestDBResolve(t *testing.T) {
	store := testutil.NewTestStore(t)
	q := store.Queries()
	insertInstance(t, q, "inst-a")
	insertInstance(t, q, "inst-b")
	insertUser(t, q, "ann", model.RoleApprover, model.RoleOperator)
	insertUser(t, q, "bob", model.RoleApprover)
	insertUser(t, q, "gone", model.RoleApprover)
	insertUser(t, q, "roleless")

	link(t, q, "inst-a", "ann", "U1")
	// The same actor id names a different person on another instance.
	link(t, q, "inst-b", "bob", "U1")
	link(t, q, "inst-a", "gone", "U-GONE")
	link(t, q, "inst-a", "roleless", "U-NOROLE")
	deactivated := now()
	if err := q.DeactivateUser(context.Background(), db.DeactivateUserParams{ID: "gone", DeactivatedAt: &deactivated}); err != nil {
		t.Fatalf("DeactivateUser: %v", err)
	}

	dir := actordir.DB{Querier: q}
	tests := []struct {
		name      string
		instance  string
		actor     string
		wantFound bool
		wantUser  string
		wantRoles []model.Role
	}{
		{"mapped actor resolves with every role", "inst-a", "U1", true, "ann", []model.Role{model.RoleApprover, model.RoleOperator}},
		{"same actor id on another instance is another person", "inst-b", "U1", true, "bob", []model.Role{model.RoleApprover}},
		{"actor unknown on this instance", "inst-b", "U-GONE", false, "", nil},
		{"deactivated user does not resolve", "inst-a", "U-GONE", false, "", nil},
		{"user with no roles does not resolve", "inst-a", "U-NOROLE", false, "", nil},
		{"empty instance resolves nothing", "", "U1", false, "", nil},
		{"empty actor resolves nothing", "inst-a", "", false, "", nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res, found, err := dir.Resolve(context.Background(), tc.instance, tc.actor)
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if found != tc.wantFound {
				t.Fatalf("found = %v, want %v (resolution %+v)", found, tc.wantFound, res)
			}
			if !found {
				return
			}
			roles := slices.Clone(res.Roles)
			slices.Sort(roles)
			if res.UserID != tc.wantUser || !slices.Equal(roles, tc.wantRoles) {
				t.Errorf("resolution = %+v, want user %s with roles %v", res, tc.wantUser, tc.wantRoles)
			}
		})
	}
}

func TestSetAdminIdentity(t *testing.T) {
	store := testutil.NewTestStore(t)
	q := store.Queries()
	insertInstance(t, q, "inst-a")
	insertUser(t, q, "ann", model.RoleApprover)
	insertUser(t, q, "bob", model.RoleApprover)
	dir := actordir.DB{Querier: q}

	link(t, q, "inst-a", "ann", "U1")
	// Re-linking a user on the same instance replaces the mapping: the old
	// actor id stops naming them.
	link(t, q, "inst-a", "ann", "U2")
	if _, found, _ := dir.Resolve(context.Background(), "inst-a", "U1"); found {
		t.Error("the replaced actor id still resolves")
	}
	if res, found, _ := dir.Resolve(context.Background(), "inst-a", "U2"); !found || res.UserID != "ann" {
		t.Errorf("Resolve(U2) = %+v, %v; want ann", res, found)
	}

	// An actor id already mapped to someone else on the instance is refused.
	err := q.SetAdminIdentity(context.Background(), db.SetAdminIdentityParams{
		InstanceID: "inst-a", UserID: "bob", ExternalUserID: "U2", CreatedAt: now(),
	})
	if err == nil {
		t.Fatal("mapping an actor id already linked to another user on the same instance succeeded")
	}
}

// Deleting the instance or the user removes the mapping with it.
func TestIdentityCascade(t *testing.T) {
	store := testutil.NewTestStore(t)
	q := store.Queries()
	insertInstance(t, q, "inst-a")
	insertUser(t, q, "ann", model.RoleApprover)
	link(t, q, "inst-a", "ann", "U1")

	if _, err := store.DB().ExecContext(context.Background(), `DELETE FROM plugin_instances WHERE id = 'inst-a'`); err != nil {
		t.Fatalf("delete instance: %v", err)
	}
	rows, err := q.ListIdentitiesByUser(context.Background(), "ann")
	if err != nil {
		t.Fatalf("ListIdentitiesByUser: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("identities after instance delete = %+v, want none", rows)
	}
}

type failingQuerier struct{}

func (failingQuerier) GetUserByExternalIdentity(context.Context, db.GetUserByExternalIdentityParams) ([]db.GetUserByExternalIdentityRow, error) {
	return nil, errors.New("db down")
}

func TestDBResolvePropagatesErrors(t *testing.T) {
	if _, _, err := (actordir.DB{Querier: failingQuerier{}}).Resolve(context.Background(), "inst-a", "U1"); err == nil {
		t.Error("a query error was swallowed; it must not read as an unmapped actor")
	}
}
