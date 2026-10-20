package assembly

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/felag-engineering/gleipnir/internal/plugin/hostendpoint"
)

type fixedResolver struct{ id hostendpoint.Identity }

func (f fixedResolver) ResolveToken(context.Context, string) (hostendpoint.Identity, bool, error) {
	return f.id, true, nil
}

func TestObserveHostCalls(t *testing.T) {
	tests := []struct {
		name string
		body string
		want HostCall
	}{
		{
			name: "tool call carries its tool name",
			body: `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"host/log"}}`,
			want: HostCall{InstanceID: "inst-1", Generation: 3, Method: "tools/call", Tool: "host/log"},
		},
		{
			name: "non tool method has no tool",
			body: `{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{"name":"ignored"}}`,
			want: HostCall{InstanceID: "inst-1", Generation: 3, Method: "server/discover"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got []HostCall
			var downstreamBody string
			inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				b, _ := io.ReadAll(r.Body)
				downstreamBody = string(b)
			})
			h := hostendpoint.RequireInstanceToken(
				observeHostCalls(inner, func(c HostCall) { got = append(got, c) }),
				fixedResolver{hostendpoint.Identity{InstanceID: "inst-1", Generation: 3}})

			req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(tt.body))
			req.Header.Set("Authorization", "Bearer tok")
			h.ServeHTTP(httptest.NewRecorder(), req)

			if len(got) != 1 || got[0] != tt.want {
				t.Fatalf("observed %+v, want [%+v]", got, tt.want)
			}
			if downstreamBody != tt.body {
				t.Errorf("downstream handler read %q, want the body untouched", downstreamBody)
			}
		})
	}

	t.Run("unauthenticated request is not observed", func(t *testing.T) {
		called := false
		h := hostendpoint.RequireInstanceToken(
			observeHostCalls(http.NotFoundHandler(), func(HostCall) { called = true }),
			fixedResolver{})
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{}`)))
		if called {
			t.Error("observer ran for a request that failed authentication")
		}
	})
}
