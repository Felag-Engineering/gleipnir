package adminv2_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"testing"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
)

var fixedClock = func() time.Time { return time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC) }

func digestOf(seed string) string {
	sum := sha256.Sum256([]byte(seed))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// seedV2Plugin inserts a v2 plugin row pinning digest, with the given
// plugins.status.
func seedV2Plugin(t *testing.T, store *db.Store, pluginID, digest, status string) {
	t.Helper()
	manifest := fmt.Sprintf(`schema_version: "2"
name: test-plugin
version: "1.0.0"
package:
  registry_type: oci
  identifier: ghcr.io/acme/test-plugin@%s
  transport:
    type: streamable-http
    port: 8080
gleipnir:
  profiles:
    tool_provider: {}
`, digest)

	now := fixedClock().UTC().Format(time.RFC3339Nano)
	if _, err := store.Queries().CreatePlugin(context.Background(), db.CreatePluginParams{
		ID:               pluginID,
		Name:             "test-plugin",
		PluginVersion:    "1.0.0",
		ManifestSnapshot: manifest,
		Status:           status,
		CreatedAt:        now,
		UpdatedAt:        now,
	}); err != nil {
		t.Fatalf("seed plugin: %v", err)
	}
}
