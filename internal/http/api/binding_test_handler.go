package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/felag-engineering/gleipnir/internal/http/httputil"
	"github.com/felag-engineering/gleipnir/internal/plugin/binding"
	"github.com/felag-engineering/gleipnir/internal/plugin/configvalidate"
)

// BindingTestHandler serves POST /api/v1/admin/plugin-instances/{iid}/event-kinds/{kind}/test-binding.
// It is a read-only endpoint: the client sends back payloads it already fetched
// from the plugin-instances list (stateless; avoids hot-reload drift).
type BindingTestHandler struct {
	snap *configvalidate.Snapshotter
}

// NewBindingTestHandler creates a BindingTestHandler. snap must be the same
// *configvalidate.Snapshotter pointer used by AudienceHandler so the
// content-hash cache stays coherent across both handlers.
func NewBindingTestHandler(snap *configvalidate.Snapshotter) *BindingTestHandler {
	return &BindingTestHandler{snap: snap}
}

type bindingTestRequest struct {
	Binding  map[string]any   `json:"binding"`
	Payloads []map[string]any `json:"payloads"`
}

type bindingTestResult struct {
	Match bool   `json:"match"`
	Error string `json:"error,omitempty"`
}

type bindingTestResponse struct {
	Results []bindingTestResult `json:"results"`
}

// Test handles POST /api/v1/admin/plugin-instances/{iid}/event-kinds/{kind}/test-binding.
// It compiles the binding against the manifest's binding_schema and evaluates
// each client-supplied payload, returning per-payload match results in input order.
func (h *BindingTestHandler) Test(w http.ResponseWriter, r *http.Request) {
	iid := chi.URLParam(r, "iid")
	kind := chi.URLParam(r, "kind")
	ctx := r.Context()

	var req bindingTestRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}

	mf, err := h.snap.ForInstanceID(ctx, iid)
	if err != nil {
		httputil.WriteError(w, http.StatusNotFound, "plugin instance not found", iid)
		return
	}

	// Locate the event kind and compile the binding against its schema.
	var cb *binding.CompiledBinding
	for _, ek := range mf.EventKinds {
		if ek.Kind != kind {
			continue
		}
		compiled, compileErr := binding.Compile(req.Binding, ek.BindingSchema)
		if compileErr != nil {
			if errors.Is(compileErr, binding.ErrInvalidRegex) ||
				errors.Is(compileErr, binding.ErrUnknownField) ||
				errors.Is(compileErr, binding.ErrUnsupportedOperator) {
				httputil.WriteError(w, http.StatusBadRequest, "binding compile error", compileErr.Error())
				return
			}
			slog.Error("binding test: compile", "instance_id", iid, "kind", kind, "err", compileErr)
			httputil.WriteError(w, http.StatusInternalServerError, "internal error", "")
			return
		}
		cb = compiled
		break
	}

	if cb == nil {
		httputil.WriteError(w, http.StatusNotFound, "event kind not found", kind)
		return
	}

	results := make([]bindingTestResult, 0, len(req.Payloads))
	for _, payload := range req.Payloads {
		match, evalErr := cb.Evaluate(payload)
		res := bindingTestResult{Match: match}
		if evalErr != nil {
			res.Error = evalErr.Error()
		}
		results = append(results, res)
	}

	httputil.WriteJSON(w, http.StatusOK, bindingTestResponse{Results: results})
}
