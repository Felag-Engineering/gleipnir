package hostclient

import "context"

// CallIDHeader is the header the host endpoint reads for run correlation
// (spec §8): required for GetRunContext, and used by Log when present.
// Matches internal/plugin/hostendpoint.CallIDHeader exactly — duplicated for
// the same zero-protobuf reason as InstanceTokenEnvVar (client.go): this
// package must not import anything under internal/.
const CallIDHeader = "Gleipnir-Call-Id"

type callIDCtxKey struct{}

// WithCallID attaches a Gleipnir call id to ctx so every host-endpoint call
// made with the returned context carries it as the Gleipnir-Call-Id header
// automatically. A plugin author sets this once per tool invocation (the
// call id the host handed it) rather than threading the header through every
// individual Client method call — the same ergonomic the gRPC seam gave
// authors via CallIDFromContext propagation in the outgoing metadata.
func WithCallID(ctx context.Context, callID string) context.Context {
	if callID == "" {
		return ctx
	}
	return context.WithValue(ctx, callIDCtxKey{}, callID)
}

// CallIDFromContext returns the call id attached by WithCallID, if any.
func CallIDFromContext(ctx context.Context) (string, bool) {
	v := ctx.Value(callIDCtxKey{})
	if v == nil {
		return "", false
	}
	id, ok := v.(string)
	return id, ok && id != ""
}
