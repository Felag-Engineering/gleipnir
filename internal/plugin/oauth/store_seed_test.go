package oauth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
	sdkmanifest "github.com/felag-engineering/gleipnir/plugin-sdk/manifest"
)

// #572 regression: a freshly-created plugin instance has CredentialsEncrypted
// NULL, so the stored credential blob has no Strategy. The first non-OAuth
// credential write must SEED the strategy (mirroring SetOAuthClient /
// SeedOAuthToken) rather than reject with ErrWrongStrategy — otherwise the
// instance is permanently stuck at unhealthy/credentials_missing and the
// channel/tool plugin can never be configured. Before the fix this blocked the
// ntfy reference plugin (static_api_key) end to end.
func TestDBStore_SetStaticAPIKey_SeedsStrategyOnFreshInstance(t *testing.T) {
	baseTime := time.Unix(1000000, 0)
	q := &fakeOAuthQuerier{
		instance: db.PluginInstance{ID: "inst-1", HealthState: "unhealthy", Version: 0}, // CredentialsEncrypted nil → fresh
	}
	store := NewDBStore(q, noopEncrypt, noopDecrypt, q, func() time.Time { return baseTime })
	ctx := context.Background()

	if err := store.SetStaticAPIKey(ctx, "inst-1", "Authorization", "Bearer", "tok_123"); err != nil {
		t.Fatalf("SetStaticAPIKey on fresh instance: %v", err)
	}

	creds, _, err := store.LoadCredentials(ctx, "inst-1")
	if err != nil {
		t.Fatalf("LoadCredentials: %v", err)
	}
	if creds.Strategy != sdkmanifest.AuthStrategyStaticAPIKey {
		t.Errorf("strategy not seeded: got %q, want %q", creds.Strategy, sdkmanifest.AuthStrategyStaticAPIKey)
	}
	if creds.StaticAPIKey == nil || creds.StaticAPIKey.APIKey != "tok_123" {
		t.Errorf("api key not stored: %+v", creds.StaticAPIKey)
	}
}

func TestDBStore_SetBasicAuth_SeedsStrategyOnFreshInstance(t *testing.T) {
	baseTime := time.Unix(1000000, 0)
	q := &fakeOAuthQuerier{instance: db.PluginInstance{ID: "inst-1", Version: 0}}
	store := NewDBStore(q, noopEncrypt, noopDecrypt, q, func() time.Time { return baseTime })
	ctx := context.Background()

	if err := store.SetBasicAuth(ctx, "inst-1", "user", "pass"); err != nil {
		t.Fatalf("SetBasicAuth on fresh instance: %v", err)
	}
	creds, _, err := store.LoadCredentials(ctx, "inst-1")
	if err != nil {
		t.Fatalf("LoadCredentials: %v", err)
	}
	if creds.Strategy != sdkmanifest.AuthStrategyBasicAuth {
		t.Errorf("strategy not seeded: got %q, want %q", creds.Strategy, sdkmanifest.AuthStrategyBasicAuth)
	}
}

// Seeding must only happen when the stored strategy is empty. Once a strategy is
// recorded, a credential write for a different strategy is still rejected.
func TestDBStore_SetStaticAPIKey_RejectsMismatchedStrategy(t *testing.T) {
	baseTime := time.Unix(1000000, 0)
	q := &fakeOAuthQuerier{instance: db.PluginInstance{ID: "inst-1", Version: 0}}
	store := NewDBStore(q, noopEncrypt, noopDecrypt, q, func() time.Time { return baseTime })
	ctx := context.Background()

	// First write seeds basic_auth.
	if err := store.SetBasicAuth(ctx, "inst-1", "u", "p"); err != nil {
		t.Fatalf("SetBasicAuth (seed): %v", err)
	}
	// A static-api-key write against a basic_auth-strategy instance must reject.
	if err := store.SetStaticAPIKey(ctx, "inst-1", "Authorization", "Bearer", "tok"); !errors.Is(err, ErrWrongStrategy) {
		t.Errorf("expected ErrWrongStrategy, got %v", err)
	}
}
