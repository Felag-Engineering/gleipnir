package hostendpoint

import (
	"context"
	"net/http"
	"strings"

	"github.com/felag-engineering/gleipnir/internal/plugin/generation"
)

// The three gRPC host-RPC interceptors ported to HTTP middleware (#876),
// semantics unchanged, chained in the same order hostsvc chains them:
// token, then generation, then call-id (main.go's `[token, generation,
// call-id]` via hostwire.Options.ServerInterceptors).
//
// hostsvc's RejectIfDetached is deliberately NOT ported. It existed for
// future call-scope-bound RPCs, and the one RPC that ever flirted with call
// scope — WriteAuditStep — authenticates by request-ownership instead and is
// removed from the §8 inventory entirely (sequencing in #880). Carrying an
// unused guard across a transport rewrite would preserve the code and lose
// the reason; if a call-scope-bound tool ever appears, build the guard
// against that tool's actual contract.

// CallIDHeader carries the correlation id gRPC carried as the
// gleipnir-call-id metadata key. Same name, HTTP header casing.
const CallIDHeader = "Gleipnir-Call-Id"

// Identity is what token resolution establishes about a caller: which plugin
// instance it is, and — when the resolver knows generations — which
// generation of it. Generation is 0 for resolvers without the concept (the
// v1.1 identity.Registry adapter).
type Identity struct {
	InstanceID string
	Generation int64
}

// TokenResolver authenticates a bearer token. ok=false is an ordinary
// authentication failure (unknown or revoked token); err is reserved for the
// resolver itself failing (a database error), which must surface as a server
// fault rather than masquerade as a 401 — telling a healthy plugin its token
// is bad sends the operator debugging credential delivery instead of the
// host.
type TokenResolver interface {
	ResolveToken(ctx context.Context, token string) (id Identity, ok bool, err error)
}

type identityCtxKey struct{}

// IdentityFromContext returns the caller identity attached by
// RequireInstanceToken. ("", false)-shaped zero when the middleware did not
// run or rejected — mirrors hostsvc.InstanceIDFromTokenContext.
func IdentityFromContext(ctx context.Context) (Identity, bool) {
	v := ctx.Value(identityCtxKey{})
	if v == nil {
		return Identity{}, false
	}
	id, ok := v.(Identity)
	return id, ok
}

type callIDCtxKey struct{}

// CallIDFromContext returns the Gleipnir-Call-Id attached by WithCallID,
// mirroring hostsvc.CallIDFromContext: ("", false) when the header was
// absent, empty, or ambiguous.
func CallIDFromContext(ctx context.Context) (string, bool) {
	v := ctx.Value(callIDCtxKey{})
	if v == nil {
		return "", false
	}
	id, ok := v.(string)
	return id, ok
}

// RequireInstanceToken is the port of hostsvc.UnaryInstanceTokenInterceptor
// (#202): every request must carry the per-generation instance token as a
// bearer header (spec §8), and rejection happens before the handler runs.
// The two failure messages are kept distinct, as they were on gRPC — a
// plugin author debugging "missing" (the SDK never attached it) is in a
// different bug than one debugging "unknown or revoked" (rotation retired
// their generation).
//
// No audit event on rejection, for the same reason the gRPC interceptor
// wrote none: the instance identity is unknown at this point, and an audit
// row with a NULL instance is noise, not evidence.
func RequireInstanceToken(next http.Handler, resolver TokenResolver) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := bearerToken(r.Header.Get("Authorization"))
		if !ok {
			http.Error(w, "missing instance token", http.StatusUnauthorized)
			return
		}
		id, found, err := resolver.ResolveToken(r.Context(), token)
		if err != nil {
			http.Error(w, "token resolution failed", http.StatusInternalServerError)
			return
		}
		if !found {
			http.Error(w, "unknown or revoked instance token", http.StatusUnauthorized)
			return
		}
		ctx := context.WithValue(r.Context(), identityCtxKey{}, id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// bearerToken extracts the credential from an Authorization header,
// accepting exactly the Bearer scheme (case-insensitive per RFC 7235) with a
// non-empty credential.
func bearerToken(header string) (string, bool) {
	const prefix = "bearer "
	if len(header) <= len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return "", false
	}
	token := strings.TrimSpace(header[len(prefix):])
	return token, token != ""
}

// RequireGenerationSlot is the port of
// hostsvc.UnaryGenerationRefcountInterceptor (#294): each request acquires a
// refcount slot under the instance's current generation before the handler
// runs, and releases it after, so a rotation can drain in-flight
// host-endpoint calls before the new generation takes traffic.
//
// The drain behaviour carries over unchanged, and the acquire-before-handler
// ordering is the part that makes it true: in-flight requests continue under
// the wrapped cancellable context and are force-cancelled only after the
// grace period; NEW requests arriving during the pause window block in
// Acquire and turn into 503 when their own request context expires first.
//
// An unregistered instance also rejects 503 rather than passing through: on
// gRPC the controller was only consulted for registered instances by
// construction, and "no generation state" here means the reconciler has not
// (or no longer) declared this instance — traffic for it has nowhere valid
// to go.
//
// A request with no identity in context passes through untouched, exactly as
// the gRPC interceptor did, so a mis-composed chain surfaces the auth
// layer's error rather than masking it behind a generation failure.
func RequireGenerationSlot(next http.Handler, c *generation.Controller) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := IdentityFromContext(r.Context())
		if !ok {
			next.ServeHTTP(w, r)
			return
		}
		wrappedCtx, release, _, err := c.Acquire(r.Context(), id.InstanceID)
		if err != nil {
			http.Error(w, "plugin generation draining", http.StatusServiceUnavailable)
			return
		}
		defer release()
		next.ServeHTTP(w, r.WithContext(wrappedCtx))
	})
}

// WithCallID is the port of hostsvc.UnaryCallIDInterceptor: it attaches a
// present, unambiguous Gleipnir-Call-Id header value to the context and
// never rejects on its own — a missing call id means the handler sees
// ("", false), and any call-scope enforcement belongs to the handler that
// needs it. Multiple values are ambiguous and treated as absent rather than
// guessed between, same as the gRPC rule.
func WithCallID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		vals := r.Header.Values(CallIDHeader)
		if len(vals) == 1 && vals[0] != "" {
			ctx := context.WithValue(r.Context(), callIDCtxKey{}, vals[0])
			r = r.WithContext(ctx)
		}
		next.ServeHTTP(w, r)
	})
}

// Chain applies the full host-endpoint middleware stack to handler in the
// canonical order — token, then generation, then call-id — matching the
// gRPC interceptor chain it replaces. This is what a ListenerSet should be
// constructed with; a bare Server behind a listener is a host endpoint whose
// every caller is anonymous.
func Chain(handler http.Handler, resolver TokenResolver, c *generation.Controller) http.Handler {
	return RequireInstanceToken(RequireGenerationSlot(WithCallID(handler), c), resolver)
}
