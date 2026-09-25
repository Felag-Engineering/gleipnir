package container

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"testing"
)

const (
	testSelfID   = "4a1f9e2c8b7d6a5f3e2d1c0b9a8f7e6d5c4b3a2918f7e6d5c4b3a29187654321"
	testOtherID  = "9e8d7c6b5a4f3e2d1c0b9a8f7e6d5c4b3a2918f7e6d5c4b3a291876543210ab"
	testHostname = "shared-hostname"
)

// --- candidate-ID extraction: pure, fixture-driven ---------------------------

func TestSelfContainerIDFromContainerenv(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{
			name: "podman containerenv",
			content: `engine="podman-4.9.3"
name="upbeat_darwin"
id="` + testSelfID + `"
image="docker.io/library/alpine:latest"
rootless=1
`,
			want: testSelfID,
		},
		{
			name:    "no id line",
			content: "engine=\"podman-4.9.3\"\nname=\"x\"\n",
			want:    "",
		},
		{
			name:    "empty file",
			content: "",
			want:    "",
		},
		{
			name:    "id line too short is not matched",
			content: "id=\"deadbeef\"\n",
			want:    "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := selfContainerIDFromContainerenv(tc.content); got != tc.want {
				t.Errorf("selfContainerIDFromContainerenv(%q) = %q, want %q", tc.content, got, tc.want)
			}
		})
	}
}

func TestSelfContainerIDFromMountinfo(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{
			name: "docker hostname bind mount",
			content: "657 656 253:1 /var/lib/docker/containers/" + testSelfID + "/hostname " +
				"/etc/hostname rw,relatime - ext4 /dev/mapper/root rw\n",
			want: testSelfID,
		},
		{
			name: "docker resolv.conf bind mount",
			content: "658 656 253:1 /var/lib/docker/containers/" + testSelfID + "/resolv.conf " +
				"/etc/resolv.conf rw,relatime - ext4 /dev/mapper/root rw\n",
			want: testSelfID,
		},
		{
			name:    "no matching mount",
			content: "657 656 253:1 / / rw,relatime - ext4 /dev/mapper/root rw\n",
			want:    "",
		},
		{
			name:    "empty file",
			content: "",
			want:    "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := selfContainerIDFromMountinfo(tc.content); got != tc.want {
				t.Errorf("selfContainerIDFromMountinfo(%q) = %q, want %q", tc.content, got, tc.want)
			}
		})
	}
}

// --- ResolveSelfContainerID: fixture files + a stub Inspect ------------------

// stubInspectRuntime answers Inspect from a fixed table keyed by ContainerID,
// so a test can corroborate (or fail to corroborate) a candidate ID without a
// real socket and without depending on Fake's own ID-minting scheme (which
// never produces a 64-hex ID matching a containerenv/mountinfo fixture).
type stubInspectRuntime struct {
	Runtime
	infos map[ContainerID]ContainerInfo
}

func (s *stubInspectRuntime) Inspect(_ context.Context, id ContainerID) (ContainerInfo, error) {
	info, ok := s.infos[id]
	if !ok {
		return ContainerInfo{}, errors.New("no such container")
	}
	return info, nil
}

// writeFixture writes content to a fresh temp file and returns its path.
func writeFixture(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writing fixture %s: %v", name, err)
	}
	return path
}

// withFixturePaths points the package's injectable file/hostname seams at
// fixture values for the duration of the test.
func withFixturePaths(t *testing.T, containerenv, mountinfo, hostname string) {
	t.Helper()
	oldContainerenv, oldMountinfo, oldHostname := containerenvPath, procSelfMountinfoPath, osHostname
	containerenvPath, procSelfMountinfoPath = containerenv, mountinfo
	osHostname = func() (string, error) { return hostname, nil }
	t.Cleanup(func() {
		containerenvPath, procSelfMountinfoPath, osHostname = oldContainerenv, oldMountinfo, oldHostname
	})
}

func TestResolveSelfContainerID_PodmanContainerenvCorroborated(t *testing.T) {
	containerenv := writeFixture(t, "containerenv", `id="`+testSelfID+`"`+"\n")
	missingMountinfo := filepath.Join(t.TempDir(), "does-not-exist")
	withFixturePaths(t, containerenv, missingMountinfo, testHostname)

	stub := &stubInspectRuntime{infos: map[ContainerID]ContainerInfo{
		ContainerID(testSelfID): {ID: testSelfID, Hostname: testHostname},
	}}

	got, err := ResolveSelfContainerID(context.Background(), stub)
	if err != nil {
		t.Fatalf("ResolveSelfContainerID: %v", err)
	}
	if got != ContainerID(testSelfID) {
		t.Errorf("ResolveSelfContainerID() = %q, want %q", got, testSelfID)
	}
}

func TestResolveSelfContainerID_DockerMountinfoCorroborated(t *testing.T) {
	missingContainerenv := filepath.Join(t.TempDir(), "does-not-exist")
	mountinfo := writeFixture(t, "mountinfo",
		"657 656 253:1 /var/lib/docker/containers/"+testSelfID+"/hostname /etc/hostname rw,relatime - ext4 /dev/mapper/root rw\n")
	withFixturePaths(t, missingContainerenv, mountinfo, testHostname)

	stub := &stubInspectRuntime{infos: map[ContainerID]ContainerInfo{
		ContainerID(testSelfID): {ID: testSelfID, Hostname: testHostname},
	}}

	got, err := ResolveSelfContainerID(context.Background(), stub)
	if err != nil {
		t.Fatalf("ResolveSelfContainerID: %v", err)
	}
	if got != ContainerID(testSelfID) {
		t.Errorf("ResolveSelfContainerID() = %q, want %q", got, testSelfID)
	}
}

func TestResolveSelfContainerID_NoCandidateIsEmpty(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	withFixturePaths(t, missing, missing, testHostname)

	got, err := ResolveSelfContainerID(context.Background(), NewFake())
	if err != nil {
		t.Fatalf("ResolveSelfContainerID: %v", err)
	}
	if got != "" {
		t.Errorf("ResolveSelfContainerID() = %q, want empty (not containerized)", got)
	}
}

func TestResolveSelfContainerID_InspectFailureFailsClosed(t *testing.T) {
	containerenv := writeFixture(t, "containerenv", `id="`+testSelfID+`"`+"\n")
	missingMountinfo := filepath.Join(t.TempDir(), "does-not-exist")
	withFixturePaths(t, containerenv, missingMountinfo, testHostname)

	// An empty Fake has no container at all, so Inspect(candidate) errors.
	got, err := ResolveSelfContainerID(context.Background(), NewFake())
	if err != nil {
		t.Fatalf("ResolveSelfContainerID: %v", err)
	}
	if got != "" {
		t.Errorf("ResolveSelfContainerID() = %q, want empty on an Inspect failure", got)
	}
}

func TestResolveSelfContainerID_HostnameMismatchFailsClosed(t *testing.T) {
	containerenv := writeFixture(t, "containerenv", `id="`+testSelfID+`"`+"\n")
	missingMountinfo := filepath.Join(t.TempDir(), "does-not-exist")
	withFixturePaths(t, containerenv, missingMountinfo, testHostname)

	stub := &stubInspectRuntime{infos: map[ContainerID]ContainerInfo{
		// The candidate ID inspects fine, but the daemon's hostname for it
		// does not match what this process calls itself — a corroboration
		// mismatch, not a resolution.
		ContainerID(testSelfID): {ID: testSelfID, Hostname: "some-other-hostname"},
	}}

	got, err := ResolveSelfContainerID(context.Background(), stub)
	if err != nil {
		t.Fatalf("ResolveSelfContainerID: %v", err)
	}
	if got != "" {
		t.Errorf("ResolveSelfContainerID() = %q, want empty on a hostname mismatch", got)
	}
}

// TestResolveSelfContainerID_HostnameCollisionDoesNotMisdirect is the direct
// test of #958 finding 3: nothing about resolution is steerable by which
// OTHER container happens to share this process's hostname. The candidate ID
// comes from the unspoofable source (containerenv) alone; a same-hostname
// impostor container existing elsewhere in the daemon's inventory must have
// no effect, because resolution never looks anything up BY hostname.
func TestResolveSelfContainerID_HostnameCollisionDoesNotMisdirect(t *testing.T) {
	containerenv := writeFixture(t, "containerenv", `id="`+testSelfID+`"`+"\n")
	missingMountinfo := filepath.Join(t.TempDir(), "does-not-exist")
	withFixturePaths(t, containerenv, missingMountinfo, testHostname)

	stub := &stubInspectRuntime{infos: map[ContainerID]ContainerInfo{
		ContainerID(testSelfID): {ID: testSelfID, Hostname: testHostname},
		// testOtherID is a wholly different, unrelated container that HAPPENS
		// to report the exact same hostname. If resolution ever used hostname
		// as a lookup key, this is what it could be tricked into returning.
		ContainerID(testOtherID): {ID: testOtherID, Hostname: testHostname},
	}}

	got, err := ResolveSelfContainerID(context.Background(), stub)
	if err != nil {
		t.Fatalf("ResolveSelfContainerID: %v", err)
	}
	if got != ContainerID(testSelfID) {
		t.Errorf("ResolveSelfContainerID() = %q, want the unspoofable-source ID %q regardless of the hostname collision", got, testSelfID)
	}
}

// --- SelfAttacher -------------------------------------------------------------

func managedNetwork(instanceID, subnet string) NetworkInfo {
	return NetworkInfo{
		ID:       "net-1",
		Name:     "gleipnir-plugin-" + instanceID,
		Internal: true,
		Subnet:   subnet,
		Labels: map[string]string{
			"gleipnir.managed":         "true",
			"gleipnir.plugin.instance": instanceID,
		},
	}
}

func newTestSelfAttacher(rt Runtime, pool netip.Prefix) *SelfAttacher {
	return NewSelfAttacher(SelfAttacherConfig{
		Runtime:           rt,
		ContainerID:       "self-1",
		ManagedLabelKey:   "gleipnir.managed",
		ManagedLabelValue: "true",
		InstanceLabelKey:  "gleipnir.plugin.instance",
		Pool:              pool,
	})
}

func TestSelfAttacher_Validate(t *testing.T) {
	pool := netip.MustParsePrefix("10.83.0.0/16")

	tests := []struct {
		name       string
		target     AttachTarget
		wantKind   ViolationKind
		wantRefuse bool
	}{
		{
			name:       "valid target passes",
			target:     AttachTarget{Network: managedNetwork("i1", "10.83.4.0/24"), InstanceID: "i1"},
			wantRefuse: false,
		},
		{
			name: "unmanaged network refused",
			target: AttachTarget{
				Network:    NetworkInfo{ID: "net-1", Name: "someone-elses", Internal: true, Subnet: "10.83.4.0/24"},
				InstanceID: "i1",
			},
			wantRefuse: true,
			wantKind:   ViolationUnmanagedNetwork,
		},
		{
			name: "non-internal network refused",
			target: func() AttachTarget {
				n := managedNetwork("i1", "10.83.4.0/24")
				n.Internal = false
				return AttachTarget{Network: n, InstanceID: "i1"}
			}(),
			wantRefuse: true,
			wantKind:   ViolationExternalNetwork,
		},
		{
			name:       "instance label mismatch refused",
			target:     AttachTarget{Network: managedNetwork("i1", "10.83.4.0/24"), InstanceID: "i2"},
			wantRefuse: true,
			wantKind:   ViolationInstanceMismatch,
		},
		{
			name:       "subnet outside the configured pool refused",
			target:     AttachTarget{Network: managedNetwork("i1", "192.168.9.0/24"), InstanceID: "i1"},
			wantRefuse: true,
			wantKind:   ViolationSubnetMismatch,
		},
		{
			name: "subnet not matching the instance's allocated subnet refused",
			target: AttachTarget{
				Network:        managedNetwork("i1", "10.83.4.0/24"),
				InstanceID:     "i1",
				ExpectedSubnet: "10.83.9.0/24",
			},
			wantRefuse: true,
			wantKind:   ViolationSubnetMismatch,
		},
		{
			name: "subnet matching the instance's allocated subnet passes",
			target: AttachTarget{
				Network:        managedNetwork("i1", "10.83.4.0/24"),
				InstanceID:     "i1",
				ExpectedSubnet: "10.83.4.0/24",
			},
			wantRefuse: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			self := newTestSelfAttacher(NewFake(), pool)
			err := self.validate(tc.target)
			if !tc.wantRefuse {
				if err != nil {
					t.Fatalf("validate() = %v, want nil", err)
				}
				return
			}
			var violation *ConstraintViolationError
			if !errors.As(err, &violation) {
				t.Fatalf("validate() error type = %T, want *ConstraintViolationError", err)
			}
			if violation.Kind != tc.wantKind {
				t.Errorf("violation.Kind = %q, want %q", violation.Kind, tc.wantKind)
			}
		})
	}
}

func TestSelfAttacher_AttachAndDetachManagedNetwork(t *testing.T) {
	fake := NewFake()
	ctx := context.Background()

	selfID, err := fake.Create(ctx, CreateOptions{
		Name: "gleipnir", Image: "gleipnir/test", Network: "ignored",
		CapDrop: []string{"ALL"}, SecurityOpt: []string{"no-new-privileges"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	netID, err := fake.CreateNetwork(ctx, NetworkOptions{Name: "instance-net", Internal: true, Subnet: "10.83.4.0/24"})
	if err != nil {
		t.Fatalf("CreateNetwork: %v", err)
	}
	netInfo := NetworkInfo{
		ID: netID, Name: "instance-net", Internal: true, Subnet: "10.83.4.0/24",
		Labels: map[string]string{"gleipnir.managed": "true", "gleipnir.plugin.instance": "i1"},
	}

	self := NewSelfAttacher(SelfAttacherConfig{
		Runtime: fake, ContainerID: selfID,
		ManagedLabelKey: "gleipnir.managed", ManagedLabelValue: "true",
		InstanceLabelKey: "gleipnir.plugin.instance",
	})
	target := AttachTarget{Network: netInfo, InstanceID: "i1"}
	pin := net.ParseIP("10.83.4.2")

	if err := self.Attach(ctx, target, pin); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	info, err := fake.Inspect(ctx, selfID)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if !SelfAttached(info, netInfo) {
		t.Fatalf("SelfAttached() = false after Attach, want true")
	}
	if got, ok := fake.PinnedAddress(selfID, netID); !ok || !got.Equal(pin) {
		t.Fatalf("PinnedAddress() = (%v, %v), want (%v, true)", got, ok, pin)
	}

	if err := self.Detach(ctx, target); err != nil {
		t.Fatalf("Detach: %v", err)
	}
	info, err = fake.Inspect(ctx, selfID)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if SelfAttached(info, netInfo) {
		t.Fatalf("SelfAttached() = true after Detach, want false")
	}
}

func TestSelfAttacher_ContainerIDIsFixedAtConstruction(t *testing.T) {
	self := NewSelfAttacher(SelfAttacherConfig{Runtime: NewFake(), ContainerID: "the-one-true-self"})
	if got := self.ContainerID(); got != "the-one-true-self" {
		t.Errorf("ContainerID() = %q, want %q", got, "the-one-true-self")
	}
}

func TestPoolContains(t *testing.T) {
	pool := netip.MustParsePrefix("10.83.0.0/16")
	tests := []struct {
		name   string
		subnet netip.Prefix
		want   bool
	}{
		{"inside the pool", netip.MustParsePrefix("10.83.4.0/24"), true},
		{"outside the pool", netip.MustParsePrefix("192.168.1.0/24"), false},
		{"a shorter prefix than the pool carves is not contained", netip.MustParsePrefix("10.83.0.0/8"), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := poolContains(pool, tc.subnet); got != tc.want {
				t.Errorf("poolContains(%s, %s) = %v, want %v", pool, tc.subnet, got, tc.want)
			}
		})
	}
}
