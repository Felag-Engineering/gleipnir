package container

import (
	"errors"
	"testing"
)

// withSysctls points readSysctl at an in-memory table for the duration of the
// test, so CheckForwardingDisabled is exercised without touching the real
// kernel's forwarding setting.
func withSysctls(t *testing.T, values map[string]string) {
	t.Helper()
	old := readSysctl
	readSysctl = func(path string) (string, error) {
		v, ok := values[path]
		if !ok {
			return "", errors.New("no such file")
		}
		return v, nil
	}
	t.Cleanup(func() { readSysctl = old })
}

// withIPv6Present controls what pathExists(ipv6SysctlDir) reports, so a test
// can simulate a kernel with or without IPv6 support.
func withIPv6Present(t *testing.T, present bool) {
	t.Helper()
	old := pathExists
	pathExists = func(path string) bool {
		if path == ipv6SysctlDir {
			return present
		}
		return old(path)
	}
	t.Cleanup(func() { pathExists = old })
}

// allSysctlsSatisfied is a fully-passing fixture table, for tests that mutate
// one entry at a time.
func allSysctlsSatisfied() map[string]string {
	return map[string]string{
		ipv4ForwardPath:        "0\n",
		ipv4DefaultForwardPath: "0\n",
		ipv6ForwardPath:        "0\n",
		ipv6DefaultForwardPath: "0\n",
		ipv6DisableDefaultPath: "1\n",
	}
}

func TestCheckForwardingDisabled(t *testing.T) {
	tests := []struct {
		name        string
		values      map[string]string
		ipv6Present bool
		wantErr     bool
		wantPath    string
	}{
		{
			name:        "everything satisfied passes",
			values:      allSysctlsSatisfied(),
			ipv6Present: true,
			wantErr:     false,
		},
		{
			name: "ipv4 forwarding enabled refuses",
			values: func() map[string]string {
				v := allSysctlsSatisfied()
				v[ipv4ForwardPath] = "1\n"
				return v
			}(),
			ipv6Present: true,
			wantErr:     true,
			wantPath:    ipv4ForwardPath,
		},
		{
			name: "ipv4 default forwarding enabled refuses",
			values: func() map[string]string {
				v := allSysctlsSatisfied()
				v[ipv4DefaultForwardPath] = "1\n"
				return v
			}(),
			ipv6Present: true,
			wantErr:     true,
			wantPath:    ipv4DefaultForwardPath,
		},
		{
			name: "ipv6 forwarding enabled refuses",
			values: func() map[string]string {
				v := allSysctlsSatisfied()
				v[ipv6ForwardPath] = "1\n"
				return v
			}(),
			ipv6Present: true,
			wantErr:     true,
			wantPath:    ipv6ForwardPath,
		},
		{
			name: "ipv6 default forwarding enabled refuses",
			values: func() map[string]string {
				v := allSysctlsSatisfied()
				v[ipv6DefaultForwardPath] = "1\n"
				return v
			}(),
			ipv6Present: true,
			wantErr:     true,
			wantPath:    ipv6DefaultForwardPath,
		},
		{
			name: "ipv6 not disabled by default refuses",
			values: func() map[string]string {
				v := allSysctlsSatisfied()
				v[ipv6DisableDefaultPath] = "0\n"
				return v
			}(),
			ipv6Present: true,
			wantErr:     true,
			wantPath:    ipv6DisableDefaultPath,
		},
		{
			name: "unreadable ipv4 switch fails closed",
			values: func() map[string]string {
				v := allSysctlsSatisfied()
				delete(v, ipv4ForwardPath)
				return v
			}(),
			ipv6Present: true,
			wantErr:     true,
			wantPath:    ipv4ForwardPath,
		},
		{
			name: "unreadable ipv6 switch fails closed when ipv6 is present",
			values: func() map[string]string {
				v := allSysctlsSatisfied()
				delete(v, ipv6ForwardPath)
				return v
			}(),
			ipv6Present: true,
			wantErr:     true,
			wantPath:    ipv6ForwardPath,
		},
		{
			// #1021 review item 2: a kernel with no IPv6 support at all has
			// nothing for the IPv6 checks to guard, so their absence is
			// satisfied rather than a fail-closed refusal — even though none
			// of the IPv6 sysctl values are readable.
			name: "ipv6 absent entirely is satisfied",
			values: map[string]string{
				ipv4ForwardPath:        "0\n",
				ipv4DefaultForwardPath: "0\n",
			},
			ipv6Present: false,
			wantErr:     false,
		},
		{
			// IPv4 checks still fail closed even on an IPv6-less kernel.
			name: "ipv4 still enforced when ipv6 is absent",
			values: map[string]string{
				ipv4ForwardPath:        "1\n",
				ipv4DefaultForwardPath: "0\n",
			},
			ipv6Present: false,
			wantErr:     true,
			wantPath:    ipv4ForwardPath,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			withSysctls(t, tc.values)
			withIPv6Present(t, tc.ipv6Present)
			err := CheckForwardingDisabled()
			if !tc.wantErr {
				if err != nil {
					t.Fatalf("CheckForwardingDisabled() = %v, want nil", err)
				}
				return
			}
			var fwdErr *ForwardingEnabledError
			if !errors.As(err, &fwdErr) {
				t.Fatalf("CheckForwardingDisabled() error type = %T, want *ForwardingEnabledError", err)
			}
			if fwdErr.Path != tc.wantPath {
				t.Errorf("ForwardingEnabledError.Path = %q, want %q", fwdErr.Path, tc.wantPath)
			}
		})
	}
}
