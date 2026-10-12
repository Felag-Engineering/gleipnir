package egress

import "testing"

func TestAllowlist_Equal(t *testing.T) {
	mk := func(domains ...string) Allowlist {
		t.Helper()
		l, err := NewAllowlist(domains)
		if err != nil {
			t.Fatalf("NewAllowlist(%v): %v", domains, err)
		}
		return l
	}
	tests := []struct {
		name string
		a, b Allowlist
		want bool
	}{
		{"both empty", mk(), mk(), true},
		{"zero value equals empty", Allowlist{}, mk(), true},
		{"order does not matter", mk("a.example.com", "*.b.example.com", "*.c.example.com"), mk("*.c.example.com", "a.example.com", "*.b.example.com"), true},
		{"extra grant", mk("a.example.com"), mk("a.example.com", "b.example.com"), false},
		{"different host", mk("a.example.com"), mk("b.example.com"), false},
		{"wildcard is not the bare host", mk("*.example.com"), mk("example.com"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.a.Equal(tt.b); got != tt.want {
				t.Errorf("Equal = %v, want %v", got, tt.want)
			}
			if got := tt.b.Equal(tt.a); got != tt.want {
				t.Errorf("reversed Equal = %v, want %v", got, tt.want)
			}
		})
	}
}
