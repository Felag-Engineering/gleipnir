package container

import (
	"errors"
	"testing"
)

func validCreateOptions() CreateOptions {
	return CreateOptions{
		Name:        "plugin-abc123",
		Image:       "registry.example.com/plugin@sha256:deadbeef",
		Network:     "gleipnir-plugin-abc123",
		Volume:      VolumeMount{Name: "plugin-abc123-data", MountPath: "/data"},
		CapDrop:     []string{"ALL"},
		SecurityOpt: []string{"no-new-privileges"},
	}
}

func TestValidateCreate(t *testing.T) {
	cases := []struct {
		name     string
		mutate   func(opts CreateOptions) CreateOptions
		wantErr  bool
		wantKind ViolationKind
	}{
		{
			name:    "valid options pass",
			mutate:  func(opts CreateOptions) CreateOptions { return opts },
			wantErr: false,
		},
		{
			name: "extra bind mount rejected",
			mutate: func(opts CreateOptions) CreateOptions {
				opts.Mounts = []Mount{{Type: MountTypeBind, Source: "/etc", Target: "/host-etc"}}
				return opts
			},
			wantErr:  true,
			wantKind: ViolationExtraMount,
		},
		{
			name: "privileged rejected",
			mutate: func(opts CreateOptions) CreateOptions {
				opts.Privileged = true
				return opts
			},
			wantErr:  true,
			wantKind: ViolationPrivileged,
		},
		{
			name: "added capabilities rejected",
			mutate: func(opts CreateOptions) CreateOptions {
				opts.CapAdd = []string{"NET_ADMIN"}
				return opts
			},
			wantErr:  true,
			wantKind: ViolationAddedCapability,
		},
		{
			name: "host network rejected",
			mutate: func(opts CreateOptions) CreateOptions {
				opts.Network = "host"
				return opts
			},
			wantErr:  true,
			wantKind: ViolationHostNetwork,
		},
		{
			name: "empty network rejected",
			mutate: func(opts CreateOptions) CreateOptions {
				opts.Network = ""
				return opts
			},
			wantErr:  true,
			wantKind: ViolationNoNetwork,
		},
		{
			name: "missing cap drop rejected",
			mutate: func(opts CreateOptions) CreateOptions {
				opts.CapDrop = nil
				return opts
			},
			wantErr:  true,
			wantKind: ViolationMissingCapDrop,
		},
		{
			name: "cap drop of unrelated capabilities is not enough",
			mutate: func(opts CreateOptions) CreateOptions {
				opts.CapDrop = []string{"SYS_ADMIN"}
				return opts
			},
			wantErr:  true,
			wantKind: ViolationMissingCapDrop,
		},
		{
			// #1021 review item V2 tightened this from #958 finding 2's
			// original "at least NET_RAW" bar: dropping only NET_RAW no
			// longer satisfies the constraint, ALL is required.
			name: "dropping only NET_RAW is no longer enough",
			mutate: func(opts CreateOptions) CreateOptions {
				opts.CapDrop = []string{"NET_RAW"}
				return opts
			},
			wantErr:  true,
			wantKind: ViolationMissingCapDrop,
		},
		{
			name: "missing security opt rejected",
			mutate: func(opts CreateOptions) CreateOptions {
				opts.SecurityOpt = nil
				return opts
			},
			wantErr:  true,
			wantKind: ViolationMissingSecurityOpt,
		},
		{
			name: "security opt of something else is not enough",
			mutate: func(opts CreateOptions) CreateOptions {
				opts.SecurityOpt = []string{"seccomp=unconfined"}
				return opts
			},
			wantErr:  true,
			wantKind: ViolationMissingSecurityOpt,
		},
		{
			name: "dropping ALL with no-new-privileges satisfies the constraint",
			mutate: func(opts CreateOptions) CreateOptions {
				opts.CapDrop = []string{"ALL"}
				opts.SecurityOpt = []string{"no-new-privileges"}
				return opts
			},
			wantErr: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := tc.mutate(validCreateOptions())
			err := ValidateCreate(opts)

			if !tc.wantErr {
				if err != nil {
					t.Fatalf("ValidateCreate() = %v, want nil", err)
				}
				return
			}

			if err == nil {
				t.Fatalf("ValidateCreate() = nil, want a *ConstraintViolationError")
			}
			var violation *ConstraintViolationError
			if !errors.As(err, &violation) {
				t.Fatalf("ValidateCreate() error type = %T, want *ConstraintViolationError", err)
			}
			if violation.Kind != tc.wantKind {
				t.Errorf("violation.Kind = %q, want %q", violation.Kind, tc.wantKind)
			}
		})
	}
}

func TestConstraintViolationError_Error(t *testing.T) {
	err := &ConstraintViolationError{Kind: ViolationPrivileged, Detail: "no privileged containers"}
	got := err.Error()
	want := `container: self-constraint violated (privileged): no privileged containers`
	if got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

func TestValidateCreateNetwork(t *testing.T) {
	cases := []struct {
		name     string
		opts     NetworkOptions
		wantErr  bool
		wantKind ViolationKind
	}{
		{
			name: "internal network accepted",
			opts: NetworkOptions{Name: "gleipnir-plugin-abc123", Internal: true},
		},
		{
			name:     "external network rejected",
			opts:     NetworkOptions{Name: "gleipnir-plugin-abc123", Internal: false},
			wantErr:  true,
			wantKind: ViolationExternalNetwork,
		},
		{
			name:     "ipv6-enabled network rejected",
			opts:     NetworkOptions{Name: "gleipnir-plugin-abc123", Internal: true, EnableIPv6: true},
			wantErr:  true,
			wantKind: ViolationIPv6Enabled,
		},
		{
			name: "IPRange excluding the reserved address is accepted",
			opts: NetworkOptions{
				Name: "gleipnir-plugin-abc123", Internal: true,
				Subnet: "10.83.4.0/24", IPRange: "10.83.4.128/25",
			},
		},
		{
			// #1021 review item 3: a range that INCLUDES the reserved
			// self-attach address must be refused — that address must always
			// be outside the daemon's dynamic-allocation pool.
			name: "IPRange containing the reserved address rejected",
			opts: NetworkOptions{
				Name: "gleipnir-plugin-abc123", Internal: true,
				Subnet: "10.83.4.0/24", IPRange: "10.83.4.0/25",
			},
			wantErr:  true,
			wantKind: ViolationReservedAddrInRange,
		},
		{
			name: "IPRange with no Subnet to validate against rejected",
			opts: NetworkOptions{
				Name: "gleipnir-plugin-abc123", Internal: true,
				IPRange: "10.83.4.128/25",
			},
			wantErr:  true,
			wantKind: ViolationReservedAddrInRange,
		},
		{
			name: "malformed IPRange rejected",
			opts: NetworkOptions{
				Name: "gleipnir-plugin-abc123", Internal: true,
				Subnet: "10.83.4.0/24", IPRange: "not-a-cidr",
			},
			wantErr:  true,
			wantKind: ViolationReservedAddrInRange,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateCreateNetwork(tc.opts)

			if !tc.wantErr {
				if err != nil {
					t.Fatalf("ValidateCreateNetwork() = %v, want nil", err)
				}
				return
			}

			var violation *ConstraintViolationError
			if !errors.As(err, &violation) {
				t.Fatalf("ValidateCreateNetwork() error type = %T, want *ConstraintViolationError", err)
			}
			if violation.Kind != tc.wantKind {
				t.Errorf("violation.Kind = %q, want %q", violation.Kind, tc.wantKind)
			}
		})
	}
}
