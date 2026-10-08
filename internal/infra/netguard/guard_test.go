package netguard

import (
	"net"
	"net/netip"
	"testing"
)

func TestListenerGuard(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	wrapped, err := Wrap(ln, netip.MustParsePrefix("10.83.0.0/16"), nil)
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}

	tests := []struct {
		name string
		l    *Listener
		want bool
	}{
		{"built by Wrap", wrapped, true},
		{"bare struct literal", &Listener{Listener: ln}, false},
		{"nil listener", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.l.Guard().Active(); got != tt.want {
				t.Errorf("Guard().Active() = %v, want %v", got, tt.want)
			}
		})
	}

	if (Guard{}).Active() {
		t.Error("zero Guard must not be active")
	}
}
