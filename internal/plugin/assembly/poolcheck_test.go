//go:build substratev2

package assembly

import (
	"context"
	"net"
	"net/netip"
	"strings"
	"testing"

	"github.com/felag-engineering/gleipnir/internal/infra/netguard"
	"github.com/felag-engineering/gleipnir/internal/plugin/container"
	"github.com/felag-engineering/gleipnir/internal/plugin/reconciler"
)

var poolCheckPool = netip.MustParsePrefix("10.83.0.0/16")

func dockerDetect() container.DetectConfig {
	return container.DetectConfig{Mode: "docker", SocketOverride: "/does/not/matter.sock"}
}

func newSelf(t *testing.T, fake *container.Fake) container.ContainerID {
	t.Helper()
	id, err := fake.Create(context.Background(), container.CreateOptions{
		Name: "gleipnir", Image: "gleipnir/gleipnir:test", Network: "gleipnir-self-net",
		CapDrop: []string{"ALL"}, SecurityOpt: []string{"no-new-privileges"}, Sysctls: container.RequiredSysctls(),
	})
	if err != nil {
		t.Fatalf("creating self container: %v", err)
	}
	return id
}

func upperHalf(subnet string) string {
	b := netip.MustParsePrefix(subnet).Addr().As4()
	b[3] = 128
	return netip.PrefixFrom(netip.AddrFrom4(b), 25).String()
}

func createNetwork(t *testing.T, fake *container.Fake, name, subnet string, labels map[string]string) container.NetworkID {
	t.Helper()
	netID, err := fake.CreateNetwork(context.Background(), container.NetworkOptions{
		Name: name, Labels: labels, Subnet: subnet, Internal: true, IPRange: upperHalf(subnet),
	})
	if err != nil {
		t.Fatalf("CreateNetwork(%s): %v", name, err)
	}
	return netID
}

// attach creates a network and joins self to it, pinned to addr ("" = unpinned).
func attach(t *testing.T, fake *container.Fake, self container.ContainerID, name, subnet string, labels map[string]string, addr string) {
	t.Helper()
	netID := createNetwork(t, fake, name, subnet, labels)
	var pin net.IP
	if addr != "" {
		pin = net.ParseIP(addr)
	}
	if err := fake.ConnectNetwork(context.Background(), netID, self, pin); err != nil {
		t.Fatalf("ConnectNetwork(%s): %v", name, err)
	}
}

func managed(instance string) map[string]string {
	return map[string]string{reconciler.LabelManaged: reconciler.ManagedValue, reconciler.LabelInstance: instance}
}

func exclusion(subnet, addr string) netguard.ExcludedAttachment {
	return netguard.ExcludedAttachment{Subnet: netip.MustParsePrefix(subnet), Addr: netip.MustParseAddr(addr)}
}

func TestSelfAttachmentExclusions(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, fake *container.Fake, self container.ContainerID)
		want  []netguard.ExcludedAttachment
	}{
		{
			name: "attached managed networks are excluded",
			setup: func(t *testing.T, fake *container.Fake, self container.ContainerID) {
				attach(t, fake, self, "plugin-a", "10.83.4.0/24", managed("a"), "10.83.4.2")
				attach(t, fake, self, "plugin-b", "10.83.5.0/24", managed("b"), "10.83.5.2")
			},
			want: []netguard.ExcludedAttachment{
				exclusion("10.83.4.0/24", "10.83.4.2"),
				exclusion("10.83.5.0/24", "10.83.5.2"),
			},
		},
		{
			name: "unmanaged networks are ignored",
			setup: func(t *testing.T, fake *container.Fake, self container.ContainerID) {
				attach(t, fake, self, "lan", "10.83.4.0/24", map[string]string{"owner": "operator"}, "10.83.4.2")
			},
		},
		{
			name: "a managed network gleipnir is not attached to is ignored",
			setup: func(t *testing.T, fake *container.Fake, self container.ContainerID) {
				createNetwork(t, fake, "plugin-a", "10.83.4.0/24", managed("a"))
			},
		},
		{
			name: "an address other than the reserved one is dropped",
			setup: func(t *testing.T, fake *container.Fake, self container.ContainerID) {
				attach(t, fake, self, "plugin-a", "10.83.4.0/24", managed("a"), "10.83.4.5")
			},
		},
		{
			name: "an attachment with no reported address is dropped",
			setup: func(t *testing.T, fake *container.Fake, self container.ContainerID) {
				attach(t, fake, self, "plugin-a", "10.83.4.0/24", managed("a"), "")
			},
		},
		{
			name: "a subnet outside the pool is dropped",
			setup: func(t *testing.T, fake *container.Fake, self container.ContainerID) {
				attach(t, fake, self, "plugin-a", "10.99.4.0/24", managed("a"), "10.99.4.2")
			},
		},
		{
			name: "a managed network without an instance label is dropped",
			setup: func(t *testing.T, fake *container.Fake, self container.ContainerID) {
				attach(t, fake, self, "plugin-a", "10.83.4.0/24",
					map[string]string{reconciler.LabelManaged: reconciler.ManagedValue}, "10.83.4.2")
			},
		},
		{
			name: "a good attachment survives a bad neighbour",
			setup: func(t *testing.T, fake *container.Fake, self container.ContainerID) {
				attach(t, fake, self, "plugin-a", "10.83.4.0/24", managed("a"), "10.83.4.5")
				attach(t, fake, self, "plugin-b", "10.83.5.0/24", managed("b"), "10.83.5.2")
			},
			want: []netguard.ExcludedAttachment{exclusion("10.83.5.0/24", "10.83.5.2")},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := container.NewFake()
			self := newSelf(t, fake)
			tt.setup(t, fake, self)

			got := SelfAttachmentExclusions(context.Background(), poolCheckPool,
				WithDetectConfig(dockerDetect()), WithRuntime(fake), withSelfContainerID(self))

			if len(got) != len(tt.want) {
				t.Fatalf("exclusions = %+v, want %+v", got, tt.want)
			}
			wantSet := map[netguard.ExcludedAttachment]bool{}
			for _, w := range tt.want {
				wantSet[w] = true
			}
			for _, g := range got {
				if !wantSet[g] {
					t.Errorf("unexpected exclusion %+v (want %+v)", g, tt.want)
				}
			}
			// The hook's output must never be something the overlap check
			// rejects as invalid input, or startup would fail on it.
			if err := netguard.CheckPoolOverlapExcluding(poolCheckPool, got); err != nil && strings.Contains(err.Error(), "excluded") {
				t.Errorf("CheckPoolOverlapExcluding rejected the hook's output: %v", err)
			}
		})
	}
}

func TestSelfAttachmentExclusions_NoRuntimeYieldsNothing(t *testing.T) {
	fake := container.NewFake()
	self := newSelf(t, fake)
	attach(t, fake, self, "plugin-a", "10.83.4.0/24", managed("a"), "10.83.4.2")

	noSocket := container.DetectConfig{Mode: "auto", DockerSocketPath: "/nope", PodmanSocketPath: "/nope2"}
	tests := []struct {
		name string
		opts []Option
	}{
		{"manual posture", []Option{WithDetectConfig(container.DetectConfig{Mode: "manual"}), WithRuntime(fake), withSelfContainerID(self)}},
		{"no socket anywhere", []Option{WithDetectConfig(noSocket), WithRuntime(fake), withSelfContainerID(self)}},
		{"not running in a container", []Option{WithDetectConfig(dockerDetect()), WithRuntime(fake), withSelfContainerID("")}},
		{"self container unknown to the runtime", []Option{WithDetectConfig(dockerDetect()), WithRuntime(fake), withSelfContainerID("missing")}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := SelfAttachmentExclusions(context.Background(), poolCheckPool, tt.opts...); len(got) != 0 {
				t.Errorf("exclusions = %+v, want none", got)
			}
		})
	}
}
