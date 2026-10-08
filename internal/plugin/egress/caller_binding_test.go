package egress

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// LocalAddr names the instance, but under the weak-host model a container on
// another instance's network can reach that address too. The peer must be
// inside the resolved instance's own /24; anything else is refused and audited
// before any grant is consulted.
func TestProxy_CallerMustBeOnResolvedInstanceNetwork(t *testing.T) {
	tests := []struct {
		name       string
		remoteAddr string
		wantStatus int
		wantDenied DenyReason
	}{
		{name: "peer inside the instance subnet passes identification", remoteAddr: "10.83.2.17:41000", wantStatus: http.StatusBadGateway},
		{name: "peer from a sibling instance subnet", remoteAddr: "10.83.1.17:41000", wantStatus: http.StatusForbidden, wantDenied: DenyCallerNotOnInstanceNetwork},
		{name: "peer outside the pool", remoteAddr: "192.168.1.5:41000", wantStatus: http.StatusForbidden, wantDenied: DenyCallerNotOnInstanceNetwork},
		{name: "IPv6 peer", remoteAddr: "[fd00::1]:41000", wantStatus: http.StatusForbidden, wantDenied: DenyCallerNotOnInstanceNetwork},
		{name: "unparseable peer", remoteAddr: "not-an-address", wantStatus: http.StatusForbidden, wantDenied: DenyCallerNotOnInstanceNetwork},
		{name: "empty peer", remoteAddr: "", wantStatus: http.StatusForbidden, wantDenied: DenyCallerNotOnInstanceNetwork},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			auditor := &recordingAuditor{}
			proxy, err := New(Config{
				Resolver: perGatewayResolver{
					"10.83.2.2": {instanceID: "inst-b", list: mustAllowlist(t, "granted.example.com")},
				},
				Auditor: auditor,
				Lookup:  fixedLookup("93.184.216.34"),
				// An allowed caller only needs to get past identification;
				// keep the upstream dial from waiting on the sandbox network.
				DialTimeout: time.Millisecond,
			})
			if err != nil {
				t.Fatalf("New: %v", err)
			}

			req := httptest.NewRequest(http.MethodGet, "http://granted.example.com/", nil)
			req.RemoteAddr = tc.remoteAddr
			local := &net.TCPAddr{IP: net.ParseIP("10.83.2.2"), Port: 3128}
			req = req.WithContext(context.WithValue(req.Context(), http.LocalAddrContextKey, net.Addr(local)))
			rec := httptest.NewRecorder()

			proxy.ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d (body %q)", rec.Code, tc.wantStatus, rec.Body.String())
			}
			_, denied := proxy.Counters()
			records := auditor.all()
			if tc.wantDenied == "" {
				if denied[DenyCallerNotOnInstanceNetwork] != 0 || len(records) != 0 {
					t.Errorf("unexpected caller-network refusal: denied=%v records=%+v", denied, records)
				}
				return
			}
			if denied[tc.wantDenied] != 1 {
				t.Errorf("denials = %v, want one %s", denied, tc.wantDenied)
			}
			want := auditRecord{instanceID: "inst-b", host: "granted.example.com", reason: tc.wantDenied}
			if len(records) != 1 || records[0] != want {
				t.Errorf("audit records = %+v, want [%+v]", records, want)
			}
		})
	}
}

func TestInstanceSubnetOf(t *testing.T) {
	tests := []struct {
		addr string
		want string
	}{
		{"10.83.2.2", "10.83.2.0/24"},
		{"127.0.0.2", "127.0.0.0/24"},
		{"fd00::2", ""},
	}
	for _, tc := range tests {
		got := InstanceSubnetOf(net.ParseIP(tc.addr))
		switch {
		case tc.want == "" && got != nil:
			t.Errorf("InstanceSubnetOf(%s) = %v, want nil", tc.addr, got)
		case tc.want != "" && (got == nil || got.String() != tc.want):
			t.Errorf("InstanceSubnetOf(%s) = %v, want %s", tc.addr, got, tc.want)
		}
	}
}
