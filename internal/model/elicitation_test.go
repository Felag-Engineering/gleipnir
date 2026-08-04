package model

import "testing"

func TestElicitationKindValid(t *testing.T) {
	tests := []struct {
		kind ElicitationKind
		want bool
	}{
		{ElicitationKindPermission, true},
		{ElicitationKindInformation, true},
		{"", false},
		{"urgent", false},
		{"Permission", false}, // case-sensitive: the DB CHECK constraint is too
	}

	for _, tc := range tests {
		t.Run(string(tc.kind), func(t *testing.T) {
			if got := tc.kind.Valid(); got != tc.want {
				t.Errorf("ElicitationKind(%q).Valid() = %v, want %v", tc.kind, got, tc.want)
			}
		})
	}
}

// The role split is the whole point of the classification (spec §6.1): a
// consent-only ask is an authorization decision, supplying values is operating
// work, and an org may deliberately keep those two people separate.
func TestElicitationKindRequiredRole(t *testing.T) {
	tests := []struct {
		name string
		kind ElicitationKind
		want Role
	}{
		{"permission needs an approver", ElicitationKindPermission, RoleApprover},
		{"information needs an operator", ElicitationKindInformation, RoleOperator},
		{"an unknown kind admits nobody but an admin", "urgent", RoleAdmin},
		{"an empty kind admits nobody but an admin", "", RoleAdmin},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.kind.RequiredRole(); got != tc.want {
				t.Errorf("ElicitationKind(%q).RequiredRole() = %q, want %q", tc.kind, got, tc.want)
			}
		})
	}
}
