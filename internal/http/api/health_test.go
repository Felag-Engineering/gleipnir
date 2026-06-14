package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/felag-engineering/gleipnir/internal/admin"
	"github.com/felag-engineering/gleipnir/internal/execution/run"
	"github.com/felag-engineering/gleipnir/internal/http/api"
	"github.com/felag-engineering/gleipnir/internal/http/auth"
	"github.com/felag-engineering/gleipnir/internal/http/sse"
	"github.com/felag-engineering/gleipnir/internal/llm"
	"github.com/felag-engineering/gleipnir/internal/mcp"
	"github.com/felag-engineering/gleipnir/internal/policy"
	"github.com/felag-engineering/gleipnir/internal/settings"
	"github.com/felag-engineering/gleipnir/internal/testutil"
	"github.com/felag-engineering/gleipnir/internal/trigger"
)

// buildHealthTestRouter mirrors buildTestRouterWithStore but lets the caller
// override the Metadata so we can flip SignatureVerificationDisabled per-test.
// Health does not depend on any handler bundle — but BuildRouter requires the
// full RouterConfig to wire middleware, so we duplicate the construction
// rather than refactor the shared helper.
func buildHealthTestRouter(t *testing.T, sigDisabled bool) http.Handler {
	t.Helper()
	store := testutil.NewTestStore(t)

	broadcaster := sse.NewBroadcaster()
	sseHandler := sse.NewHandler(broadcaster)
	registry := mcp.NewRegistry(store.Queries())
	runManager := run.NewRunManager()
	noopClient := testutil.NewNoopLLMClient()
	providerRegistry := llm.NewProviderRegistry()
	providerRegistry.Register("anthropic", noopClient)
	adminQuerier := admin.NewQuerierAdapter(store.Queries())
	systemSettings := settings.NewService(store.Queries())
	adminHandler := admin.NewHandler(adminQuerier, systemSettings, nil, []string{"anthropic"}, nil, nil, nil)
	launcher := run.NewRunLauncher(run.RunLauncherConfig{
		Store: store, Resolver: run.NewDefaultToolResolver(registry, nil, nil), Manager: runManager,
		AgentFactory: run.NewAgentFactory(providerRegistry), Publisher: broadcaster,
		DefaultFeedbackTimeout: 30 * time.Minute, ModelResolver: systemSettings,
	})
	webhookHandler := trigger.NewWebhookHandler(store, launcher, trigger.NewSecretLoader(store.Queries(), nil), systemSettings)
	openaiCompatHandler := admin.NewOpenAICompatHandler(nil, nil, providerRegistry, noopConnectionTester)
	authHandler := auth.NewHandler(store.Queries(), store.DB())
	settingsHandler := auth.NewSettingsHandler(store.Queries())
	policyService := policy.NewService(store, nil, providerRegistry, providerRegistry, systemSettings)
	policyWebhookHandler := api.NewPolicyWebhookHandler(policyService)

	return api.BuildRouter(api.RouterConfig{
		Handlers: api.HandlerBundle{
			AuthHandler: authHandler, SettingsHandler: settingsHandler,
			AdminHandler: adminHandler, OpenAICompatHandler: openaiCompatHandler,
			WebhookHandler: webhookHandler, SSEHandler: sseHandler,
			PolicyWebhookHandler: policyWebhookHandler,
		},
		Services: api.BackgroundServices{
			Store: store, Broadcaster: broadcaster, Registry: registry,
			RunManager: runManager, Launcher: launcher, ModelLister: providerRegistry,
			ProviderRegistry: providerRegistry, Settings: systemSettings,
		},
		Metadata: api.Metadata{
			Version: "test", StartTime: time.Now(), DBPath: "",
			SignatureVerificationDisabled: sigDisabled,
		},
	})
}

func TestHealth_SignedMode_OmitsSignatureField(t *testing.T) {
	r := buildHealthTestRouter(t, false)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", rec.Code)
	}

	var env struct {
		Data map[string]string `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&env); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if env.Data["status"] != "ok" {
		t.Errorf(`status field: got %q, want "ok"`, env.Data["status"])
	}
	if v, ok := env.Data["signature_verification"]; ok {
		t.Errorf("signature_verification: got %q present, want field omitted in strict mode", v)
	}
}

func TestHealth_PermissiveMode_ReportsDisabled(t *testing.T) {
	r := buildHealthTestRouter(t, true)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", rec.Code)
	}

	bodyStr := rec.Body.String()
	if !strings.Contains(bodyStr, `"signature_verification":"disabled"`) {
		t.Errorf(`body: missing signature_verification:"disabled"; got %s`, bodyStr)
	}
}
