package egress

import "testing"

func TestNewAllowlist_RejectsGrantsThatAreNotDestinations(t *testing.T) {
	tests := []struct {
		name   string
		domain string
	}{
		{"empty", ""},
		{"bare wildcard grants everything", "*."},
		{"wildcard in the middle", "api.*.slack.com"},
		{"trailing wildcard", "slack.*"},
		{"scheme", "https://slack.com"},
		{"port", "slack.com:443"},
		{"path", "slack.com/api"},
		{"credentials", "user@slack.com"},
		{"query", "slack.com?x=1"},
		{"fragment", "slack.com#frag"},
		{"space", "slack .com"},
		// A single-label name is either a typo or an intranet host, and an
		// intranet host resolves into space the proxy refuses anyway.
		{"single label", "localhost"},
		{"leading dot", ".slack.com"},
		{"trailing dot in grant", "slack.com."},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewAllowlist([]string{tc.domain}); err == nil {
				t.Errorf("NewAllowlist accepted %q", tc.domain)
			}
		})
	}
}

func TestAllowlist_Matching(t *testing.T) {
	list, err := NewAllowlist([]string{"slack.com", "*.githubusercontent.com", "API.Example.COM"})
	if err != nil {
		t.Fatalf("NewAllowlist: %v", err)
	}

	tests := []struct {
		name string
		host string
		want bool
	}{
		{"exact", "slack.com", true},
		{"exact is case-insensitive", "SLACK.com", true},
		{"grants are case-insensitive too", "api.example.com", true},
		{"trailing dot is the same host", "slack.com.", true},
		{"wildcard matches a child", "raw.githubusercontent.com", true},
		{"wildcard matches a deeper child", "a.b.githubusercontent.com", true},

		// The two that matter. A wildcard grants the children, not the parent —
		// consenting to `*.x.com` and to `x.com` are different decisions.
		{"wildcard does not match the parent", "githubusercontent.com", false},
		// And a suffix stored without its dot would admit this, which is the
		// classic allowlist bypass.
		{"wildcard does not match a look-alike", "evilgithubusercontent.com", false},

		{"unrelated host", "evil.example.net", false},
		{"exact grant does not imply subdomains", "api.slack.com", false},
		{"a host that merely contains a grant", "slack.com.evil.net", false},
		{"empty", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := list.Allows(tc.host); got != tc.want {
				t.Errorf("Allows(%q) = %v, want %v", tc.host, got, tc.want)
			}
		})
	}
}

// `egress: []` is a real configuration meaning "reaches nothing", and the zero
// value must behave the same way. "Empty means unrestricted" is how an
// allowlist quietly becomes decorative.
func TestAllowlist_EmptyGrantsNothing(t *testing.T) {
	for name, list := range map[string]Allowlist{
		"zero value":  {},
		"empty slice": mustAllowlist(t),
	} {
		t.Run(name, func(t *testing.T) {
			if !list.Empty() {
				t.Error("Empty() = false")
			}
			for _, host := range []string{"slack.com", "example.com", ""} {
				if list.Allows(host) {
					t.Errorf("Allows(%q) = true on an empty list", host)
				}
			}
		})
	}
}

func TestAllowlist_Size(t *testing.T) {
	list := mustAllowlist(t, "a.example.com", "*.b.example.com")
	if list.Size() != 2 {
		t.Errorf("Size() = %d, want 2", list.Size())
	}
	if list.Empty() {
		t.Error("Empty() = true for a populated list")
	}
}

func mustAllowlist(t *testing.T, domains ...string) Allowlist {
	t.Helper()
	list, err := NewAllowlist(domains)
	if err != nil {
		t.Fatalf("NewAllowlist(%v): %v", domains, err)
	}
	return list
}
