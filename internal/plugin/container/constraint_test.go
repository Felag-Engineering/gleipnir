package container

import (
	"errors"
	"testing"
)

func validCreateOptions() CreateOptions {
	return CreateOptions{
		Name:    "plugin-abc123",
		Image:   "registry.example.com/plugin@sha256:deadbeef",
		Network: "gleipnir-plugin-abc123",
		Volume:  VolumeMount{Name: "plugin-abc123-data", MountPath: "/data"},
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
