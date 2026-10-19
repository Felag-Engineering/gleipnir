package assembly

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"

	"github.com/felag-engineering/gleipnir/internal/plugin/egress"
	"github.com/felag-engineering/gleipnir/internal/plugin/hostendpoint"
)

// Read-only views of the composed substrate for suites that run it against a
// real runtime (the DooD suite, #987). Nothing here changes behavior.

// HostCall is one authenticated request to the host endpoint, as the endpoint
// saw it after token resolution.
type HostCall struct {
	InstanceID string
	// Generation is the container generation the presented token belongs to.
	Generation int64
	// Method is the JSON-RPC method; Tool is the tools/call target, or "".
	Method string
	Tool   string
}

// maxObservedBody bounds how much of a request body the observer reads to name
// the method; the host endpoint's own handlers read the full, restored body.
const maxObservedBody = 1 << 20

func observeHostCalls(next http.Handler, observe func(HostCall)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, maxObservedBody))
		if err == nil {
			r.Body = io.NopCloser(io.MultiReader(bytes.NewReader(body), r.Body))
			var req struct {
				Method string `json:"method"`
				Params struct {
					Name string `json:"name"`
				} `json:"params"`
			}
			id, ok := hostendpoint.IdentityFromContext(r.Context())
			if ok && json.Unmarshal(body, &req) == nil {
				call := HostCall{InstanceID: id.InstanceID, Generation: id.Generation, Method: req.Method}
				if req.Method == "tools/call" {
					call.Tool = req.Params.Name
				}
				observe(call)
			}
		}
		next.ServeHTTP(w, r)
	})
}

// EgressProxy returns the egress proxy, whose Counters are test-facing. nil
// while plugins are disabled.
func (a *Assembly) EgressProxy() *egress.Proxy { return a.proxy }

// HostListenerInstances lists the instances that currently have a host
// endpoint listener. Empty while plugins are disabled.
func (a *Assembly) HostListenerInstances() []string {
	if a.hostSet == nil {
		return nil
	}
	return a.hostSet.Instances()
}

// EgressListenerInstances lists the instances that currently have an egress
// proxy listener. Empty while plugins are disabled.
func (a *Assembly) EgressListenerInstances() []string {
	if a.egressSet == nil {
		return nil
	}
	return a.egressSet.Instances()
}
