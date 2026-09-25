package manifest

import (
	"strings"
	"testing"
)

// TestReadV1_RefusesV2Bytes is the defence-in-depth half of the #950 security
// review fix: readV1 itself must fail closed on schema_version-2 bytes, not
// merely rely on Read's own dispatch never calling it with them. This test
// calls the unexported readV1 directly (in-package, unlike reader_test.go's
// external manifest_test package) so it exercises the function's own guard
// rather than Read's routing.
func TestReadV1_RefusesV2Bytes(t *testing.T) {
	const v2Manifest = `
schema_version: "2"
name: acme-notify
version: 1.0.0
package:
  registry_type: oci
  identifier: ghcr.io/acme/notify@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
  transport:
    type: streamable-http
    port: 8080
gleipnir:
  profiles:
    tool_provider: {}
  auth:
    strategy: oauth2_authcode
    oauth_defaults:
      authorization_url: https://acme.example.com/oauth/authorize
      token_url: https://acme.example.com/oauth/token
      scopes: [read, write]
`
	snap, err := readV1([]byte(v2Manifest))
	if err == nil {
		t.Fatalf("readV1(v2 bytes): expected an error, got a Snapshot: %+v", snap)
	}
	if !strings.Contains(err.Error(), "refusing to read schema_version") {
		t.Errorf("readV1(v2 bytes) error = %q, want it to explain the refusal", err.Error())
	}
	// The zero Snapshot must come back alongside the error -- a caller that
	// only checks err != nil defensively and still reads the Snapshot must not
	// find it holding real v2 data under a nil-looking guise.
	if snap.Version != 0 || snap.Auth.Strategy != "" || len(snap.Tier2Capabilities) != 0 {
		t.Errorf("readV1(v2 bytes): want a zero Snapshot on error, got %+v", snap)
	}
}
