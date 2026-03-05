// Package trigger implements run trigger handlers. v0.1 supports webhook only;
// cron and poll are planned for v0.3.
package trigger

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/rapp992/gleipnir/internal/db"
)

// WebhookHandler handles POST /api/v1/webhooks/{policyID}.
// It validates the policy exists, applies the concurrency policy, creates a
// run record, and launches the agent in a goroutine.
type WebhookHandler struct {
	store *db.Store
}

// NewWebhookHandler returns a WebhookHandler backed by store.
func NewWebhookHandler(store *db.Store) *WebhookHandler {
	return &WebhookHandler{store: store}
}

// Handle is the chi-compatible HTTP handler for webhook-triggered runs.
// Responds 202 Accepted with {"run_id": "..."} on success.
// Responds 404 if the policy does not exist.
// Responds 409 if the concurrency policy is skip and a run is already active.
func (h *WebhookHandler) Handle(w http.ResponseWriter, r *http.Request) {
	policyID := chi.URLParam(r, "policyID")

	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20)) // 1 MiB limit
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}

	// Validate that the body is JSON; store it as the trigger payload.
	if !json.Valid(body) {
		http.Error(w, "request body must be valid JSON", http.StatusBadRequest)
		return
	}

	// TODO:
	// 1. Load policy by policyID from store; 404 if not found
	// 2. Parse the policy YAML
	// 3. Apply concurrency policy (skip / queue / parallel / replace)
	// 4. Create run record with status = pending
	// 5. Resolve tools via mcp.Registry
	// 6. Construct AuditWriter and BoundAgent
	// 7. Launch agent.Run in a goroutine; update run status on completion
	// 8. Respond 202 with run_id

	_ = policyID
	_ = body
	http.Error(w, "not implemented", http.StatusNotImplemented)
}
