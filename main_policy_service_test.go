package main

import (
	"strings"
	"testing"

	"github.com/felag-engineering/gleipnir/internal/llm"
	"github.com/felag-engineering/gleipnir/internal/mcp"
	"github.com/felag-engineering/gleipnir/internal/policy"
	"github.com/felag-engineering/gleipnir/internal/settings"
	"github.com/felag-engineering/gleipnir/internal/testutil"
)

// TestNewPolicyService_ProductionConstructor drives the same constructor run()
// uses, so a collaborator dropped from the real wiring fails here rather than
// shipping as a silently skipped check (#871).
func TestNewPolicyService_ProductionConstructor(t *testing.T) {
	store := testutil.NewTestStore(t)
	providerRegistry := llm.NewProviderRegistry()
	systemSettings := settings.NewService(store.Queries())
	validator := policy.NewSubscribedBindingValidator(&pluginInstanceResolver{q: store.Queries()}, nil)
	registry := mcp.NewRegistry(store.Queries())

	t.Run("fully wired without an encryption key is complete", func(t *testing.T) {
		svc, err := newPolicyService(store, registry, providerRegistry, systemSettings, validator, nil)
		if err != nil {
			t.Fatalf("newPolicyService: %v", err)
		}
		if err := svc.RequireComplete(); err != nil {
			t.Fatalf("RequireComplete: %v", err)
		}
	})

	t.Run("fully wired with an encryption key is complete", func(t *testing.T) {
		enc := &webhookSecretEncrypterAdapter{key: make([]byte, 32)}
		if _, err := newPolicyService(store, registry, providerRegistry, systemSettings, validator, enc); err != nil {
			t.Fatalf("newPolicyService: %v", err)
		}
	})

	t.Run("a missing required collaborator is refused and named", func(t *testing.T) {
		_, err := newPolicyService(store, registry, providerRegistry, systemSettings, nil, nil)
		if err == nil {
			t.Fatal("newPolicyService accepted a nil subscribed validator")
		}
		if !strings.Contains(err.Error(), "subscribedValidator") {
			t.Errorf("error does not name the missing collaborator: %v", err)
		}
	})
}
