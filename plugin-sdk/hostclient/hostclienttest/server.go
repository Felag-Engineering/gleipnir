package hostclienttest

import (
	"net/http/httptest"
	"testing"

	"github.com/felag-engineering/gleipnir/plugin-sdk/hostclient"
)

// NewServer builds a fake Server (see New), wraps it in an httptest.Server,
// registers t.Cleanup(Close), and sets GLEIPNIR_HOST_ENDPOINT_URL /
// GLEIPNIR_INSTANCE_TOKEN via t.Setenv — the same two environment variables
// a real plugin subprocess receives from the host at spawn time — so
// hostclient.New() with no options talks to the fake automatically.
//
// The returned *Server is the configuration/inspection handle (Calls,
// WaitForCall, Logs, Metrics, HealthReports); its URL method reports the
// httptest.Server's address.
func NewServer(t testing.TB, opts ...Option) *Server {
	t.Helper()
	s := New(opts...)
	httpSrv := httptest.NewServer(s)
	t.Cleanup(httpSrv.Close)
	t.Setenv(hostclient.HostEndpointURLEnvVar, httpSrv.URL)
	t.Setenv(hostclient.InstanceTokenEnvVar, s.Token())
	s.httpServer = httpSrv
	return s
}

// URL returns the fake's base URL when constructed via NewServer, or "" for
// a bare Server built with New (which has no httptest.Server of its own).
func (s *Server) URL() string {
	if s.httpServer == nil {
		return ""
	}
	return s.httpServer.URL
}
