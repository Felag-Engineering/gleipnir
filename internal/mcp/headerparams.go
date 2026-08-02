package mcp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/net/http/httpguts"

	"github.com/felag-engineering/gleipnir/internal/infra/headervalidate"
)

// headerParamAnnotation is the SEP-2243 JSON Schema keyword that marks a
// tool-input-schema property as also carrying an HTTP header on the wire
// (spec §11).
const headerParamAnnotation = "x-mcp-header"

// maxHeaderParams bounds how many x-mcp-header-annotated properties one
// tool call may resolve to an outbound header. This is a post-loop count
// over already-resolved params, not a bound on the parsing work itself --
// that bound is maxHeaderParamSchemaBytes below, which is what actually
// keeps a server's schema from making extractHeaderParams do unbounded
// work. Same bounded-everything posture as maxServerInfoFieldLen /
// maxErrorBodyBytes in this package.
const maxHeaderParams = 32

// maxHeaderParamSchemaBytes bounds how large a schema extractHeaderParams
// will parse looking for x-mcp-header annotations. Unlike #744's
// ArgValidator, which compiles a schema once per tool at run start,
// extractHeaderParams runs on EVERY CallTool -- and
// ResolvedTool.SchemaForHeaderParams falls back to the raw, uncanonicalized
// schema exactly when schemanorm normalization failed (one way that
// happens: the schema exceeded schemanorm's own 1 MiB MaxBytes), so the
// fallback schema can legitimately be large precisely in the case where it
// is used. Repeating a multi-MB unmarshal on every call would contradict
// this package's bounded-everything posture. Over the bound there is
// nothing safely extractable, so extraction is skipped -- same outcome as
// an unparseable or boolean schema below ("no annotations to honor", not
// "reject this call"): the schema is the server's own declaration, not
// agent-controlled input, so there is no smuggling attempt to fail closed
// against here.
const maxHeaderParamSchemaBytes = 4 << 20 // 4 MiB

// maxHeaderParamFieldLen bounds Property and HeaderName on a constructed
// HeaderParamError. Both originate from the remote server's schema and are
// untrusted (same class as ServerInfo, meta.go); this keeps a pathological
// property or annotation name out of the audit trail and operator-facing
// tool_result.
const maxHeaderParamFieldLen = 128

// maxHeaderParamReasonLen bounds HeaderParamError.Reason. Reason is usually
// one of the short constant strings below, but headervalidate.ValidateName's
// own error text re-embeds the (untruncated) header name via %q -- and that
// name is itself untrusted, server-controlled schema content. Without this
// bound, a single pathological x-mcp-header annotation could inflate Reason,
// and therefore Error(), to roughly the size of the schema itself: Error()
// reaches an error run_step, a tool_result run_step, and the LLM's message
// history, none of which cap it independently. 256 is ample for every
// legitimate Reason this file produces.
const maxHeaderParamReasonLen = 256

// maxHeaderParamValueLen bounds an x-mcp-header value's length before it is
// even considered for the wire. This value is LLM-chosen, not the remote
// server's, and net/http places no length limit of its own on a header
// value -- 4 KiB is generous for any legitimate use (an API key, a token, a
// short identifier) while making the bound explicit instead of relying on
// whatever the transport or a downstream proxy happens to enforce.
const maxHeaderParamValueLen = 4 << 10 // 4 KiB

// deniedHeaderParamNames blocks x-mcp-header names that headervalidate.ValidateName
// alone lets through, because it validates only against
// headervalidate.ReservedHeaderNames -- a DIFFERENT and deliberately
// separate list. This list is NOT added to ReservedHeaderNames: that list is
// shared with internal/plugin/oauth and the ADR-039 admin write path, where
// e.g. Authorization is a legitimate OPERATOR choice for authenticating to a
// gateway. The threat model here is the opposite direction: a REMOTE MCP
// SERVER, via its own tool schema, choosing a header name whose semantics
// act on OTHER headers rather than overriding them, or that carries an
// identity or origin the operator never granted. Grouped by threat class:
//   - Hop-by-hop / proxy-control (Connection, Proxy-Connection, Keep-Alive,
//     Upgrade, HTTP2-Settings, TE, Trailer, Transfer-Encoding, Expect,
//     Proxy-Authorization): RFC 9112 §9.1 requires a conforming intermediary
//     to strip any header NAMED in Connection before forwarding, so
//     `Connection: X-Api-Key` deletes an ADR-039 auth header without ever
//     "overriding" its value -- post's set-first ordering cannot neutralize
//     this, because the attack is the header's PRESENCE, not which layer's
//     value wins. Upgrade/HTTP2-Settings additionally enable h2c-smuggling
//     past a reverse proxy's path/method ACLs, reaching endpoints the
//     policy never granted (ADR-001).
//   - Identity, routing, and method override (Authorization, Cookie,
//     Forwarded, the X-Forwarded-* family, X-Original-URL, X-Rewrite-URL,
//     X-HTTP-Method-Override, X-HTTP-Method, X-Method-Override): these
//     differ in NAME from any ADR-039 auth header the operator configured,
//     so ordering never even engages -- the LLM's own value simply rides
//     alongside the operator's, letting a gateway that prefers one of these
//     headers authenticate as an identity or origin the operator never
//     granted, or (X-HTTP-Method / X-Method-Override, honored by OData/
//     ASP.NET and several Node/Java middlewares, same as the
//     X-HTTP-Method-Override they sit beside) turn Gleipnir's POST into a
//     different verb entirely with the operator's credential still attached
//     (ADR-001).
//   - Client-IP / origin spoofing (X-Real-IP, X-Client-IP, True-Client-IP,
//     CF-Connecting-IP, X-Cluster-Client-IP, Client-IP, X-Originating-IP):
//     a shared gateway doing IP allowlisting or rate-limiting on whichever
//     of these its proxy sets (e.g. X-Real-IP for nginx) becomes steerable
//     by the model -- the operator granted the tool, not an arbitrary
//     source IP.
//   - Client identification (User-Agent): net/http gives an explicit
//     Header["User-Agent"] entry priority over its own default, so an
//     x-mcp-header annotation can forge the UA seen by the remote server or,
//     with an empty value, suppress it outright.
var deniedHeaderParamNames = []string{
	"Connection",
	"Proxy-Connection",
	"Keep-Alive",
	"Upgrade",
	"HTTP2-Settings",
	"TE",
	"Trailer",
	"Transfer-Encoding",
	"Expect",
	"Proxy-Authorization",

	"Authorization",
	"Cookie",
	"Forwarded",
	"X-Original-URL",
	"X-Rewrite-URL",
	"X-HTTP-Method-Override",
	"X-HTTP-Method",
	"X-Method-Override",

	"X-Real-IP",
	"X-Client-IP",
	"True-Client-IP",
	"CF-Connecting-IP",
	"X-Cluster-Client-IP",
	"Client-IP",
	"X-Originating-IP",

	"User-Agent",
}

// xForwardedHeaderPrefix denies the entire X-Forwarded-* family (For, Host,
// Proto, Port, ...) by prefix rather than enumerating every member. The bare
// "X-Forwarded" name (no "-" suffix, occasionally used by proxies as a
// shorthand for X-Forwarded-For) is checked separately in
// isDeniedHeaderParamName since it has no "-" for this prefix to match (S8).
const xForwardedHeaderPrefix = "X-Forwarded-"

// isDeniedHeaderParamName reports whether canonical -- already run through
// http.CanonicalHeaderKey -- is on deniedHeaderParamNames, the bare
// "X-Forwarded" name, or in the X-Forwarded-* family. Comparison is
// case-insensitive by construction: both canonical and each entry below are
// canonicalized before comparing.
func isDeniedHeaderParamName(canonical string) bool {
	if canonical == "X-Forwarded" || strings.HasPrefix(canonical, xForwardedHeaderPrefix) {
		return true
	}
	for _, denied := range deniedHeaderParamNames {
		if canonical == http.CanonicalHeaderKey(denied) {
			return true
		}
	}
	return false
}

// plainHeaderNameByte reports whether b is in "[A-Za-z0-9-]", the only
// character class this package allows in an x-mcp-header name.
func plainHeaderNameByte(b byte) bool {
	return b >= 'A' && b <= 'Z' || b >= 'a' && b <= 'z' || b >= '0' && b <= '9' || b == '-'
}

// hasNonAllowlistedHeaderNameByte reports whether name contains any byte
// outside "[A-Za-z0-9-]". Any x-mcp-header name failing this allowlist is
// rejected outright (S6, widened by S11) rather than normalized -- the
// simpler and strictly safer of the two options: many CGI/WSGI/PHP/servlet
// backends map an inbound header to an environment variable by upper-casing
// it and turning EVERY non-alphanumeric character into "_" (the classic
// HTTP_X_API_KEY collapse), so e.g. "X-Api-Key", "X-Api_Key", and
// "X.Api.Key" all reach the same env var even though HTTP treats them as
// three distinct header names. http.CanonicalHeaderKey only capitalizes the
// letter after a "-" -- it does not fold any other RFC 7230 token character
// -- so every check in this file (isDeniedHeaderParamName, the ADR-039
// collision check, the duplicate check) sees each of those as a different
// name and lets the non-hyphen twins through. Worse, Go's Header.writeSubset
// sorts header keys byte-wise before writing them, and "-" (0x2D) sorts
// before most of the other RFC 7230 token separator characters -- notably
// "." (0x2E), "^" (0x5E), "`" (0x60), "|" (0x7C), and "~" (0x7E) -- so a twin
// built from any of those is always written LAST on the wire and therefore
// wins that backend's env-var mapping, reproducing the S2 authentication-
// substitution scenario against the very checks added to close it, silently.
// Rather than enumerate every non-alphanumeric RFC 7230 token character that
// could sort after "-", this allowlists the one character class every
// legitimate header name in this file actually uses. Costs nothing: nginx
// drops underscore headers by default, and none of this package's denied or
// legitimate names use any character outside the allowlist.
func hasNonAllowlistedHeaderNameByte(name string) bool {
	for i := 0; i < len(name); i++ {
		if !plainHeaderNameByte(name[i]) {
			return true
		}
	}
	return false
}

// headerNameConfiguredAsAuthHeader reports whether canonical -- already run
// through http.CanonicalHeaderKey -- matches one of the server's ADR-039
// auth headers. Used to fail x-mcp-header extraction closed on a collision
// (S2(b)) rather than silently relying on post's set-first ordering to make
// the admin value win: an error surfaces the collision to the operator, a
// silent override never does.
func headerNameConfiguredAsAuthHeader(canonical string, authHeaders []AuthHeader) bool {
	for _, h := range authHeaders {
		if http.CanonicalHeaderKey(h.Name) == canonical {
			return true
		}
	}
	return false
}

// headerParam is one x-mcp-header-annotated tool parameter resolved to a
// wire-ready header name/value pair. Unexported: the only consumer is
// postOptions in this package. Deliberately not named AuthHeader -- that
// type is the ADR-039 operator-configured concept, and conflating an
// agent-supplied value with an operator-configured one would blur exactly
// the distinction this feature depends on.
type headerParam struct {
	Name  string
	Value string
}

// HeaderParamError reports that a tool's x-mcp-header annotation could not
// be honored -- either the declaration itself is unusable (reserved name,
// duplicate header, malformed annotation) or the agent-supplied value could
// not be turned into a header value. Exported so internal/execution/agent
// and the external mcp_test package can recover it with errors.As.
type HeaderParamError struct {
	Property   string // the schema property that carries the annotation
	HeaderName string // the header name the annotation declared
	Reason     string // bounded to maxHeaderParamReasonLen; see newHeaderParamError
}

// Error deliberately never includes the header VALUE: it is agent-supplied
// and may carry a credential or injected content, and this message can
// reach the audit trail and the operator-facing tool_result.
func (e *HeaderParamError) Error() string {
	return fmt.Sprintf("x-mcp-header %q on tool parameter %q is not usable: %s", e.HeaderName, e.Property, e.Reason)
}

// newHeaderParamError bounds property, headerName, and reason through
// truncateForLog before constructing the error. property and headerName come
// from the remote server's schema and are untrusted; reason is usually one
// of this file's short constant strings, but headervalidate.ValidateName's
// own error re-embeds an untruncated (and therefore equally untrusted)
// header name, so it needs the same bound (S3 of the security review).
func newHeaderParamError(property, headerName, reason string) *HeaderParamError {
	return &HeaderParamError{
		Property:   truncateForLog(property, maxHeaderParamFieldLen),
		HeaderName: truncateForLog(headerName, maxHeaderParamFieldLen),
		Reason:     truncateForLog(reason, maxHeaderParamReasonLen),
	}
}

// headerParamSchema is the subset of a JSON Schema object extractHeaderParams
// reads: just enough to walk "properties" without interpreting anything else
// about the schema.
type headerParamSchema struct {
	Properties map[string]json.RawMessage `json:"properties"`
}

// extractHeaderParams reads schema for SEP-2243 x-mcp-header annotations and
// resolves each annotated property present in input to a headerParam. The
// annotated property is NOT removed from input by this function -- it stays
// in the JSON-RPC arguments object too (see CallTool).
//
// authHeaders is the server's ADR-039 auth headers (Client.authHeaders),
// passed in so a declared x-mcp-header name that collides with one of them
// can be rejected here rather than left to post's set-first ordering to
// resolve silently (S2(b) of the security review): the wire outcome would be
// identical either way -- the admin value wins -- but a silent override
// never surfaces the collision to the operator, while an error does. Pass
// nil when the server has none configured.
//
// Name validation (headervalidate.ValidateName, the [A-Za-z0-9-] allowlist,
// the x-mcp-header-specific denylist below, the ADR-039 collision check, and
// duplicate detection) runs for EVERY annotated property regardless of
// whether input supplies a value: rejection must be a deterministic property
// of the tool's declaration, not a function of what the model happened to
// send, so a smuggling attempt cannot lie dormant until some future call
// happens to fill the field.
//
// Value validation (type coercion, a length bound, then a CRLF/control-character
// check) is deliberately NOT delegated to headervalidate: that package
// validates header NAMES only, by design, and is shared with
// internal/plugin/oauth, which has no concept of a per-call agent-supplied
// value.
func extractHeaderParams(schema json.RawMessage, input map[string]any, authHeaders []AuthHeader) ([]headerParam, error) {
	if len(schema) > maxHeaderParamSchemaBytes {
		return nil, nil
	}
	if len(bytes.TrimSpace(schema)) == 0 {
		return nil, nil
	}

	var parsed headerParamSchema
	if err := json.Unmarshal(schema, &parsed); err != nil {
		// A boolean schema ("true"/"false") is legal JSON Schema declaring no
		// properties, and any other value that fails to unmarshal into this
		// shape has nothing to extract either.
		return nil, nil
	}

	names := make([]string, 0, len(parsed.Properties))
	for name := range parsed.Properties {
		names = append(names, name)
	}
	sort.Strings(names) // deterministic iteration, so wire bytes are deterministic too

	seen := make(map[string]bool, len(names)) // canonicalized header name -> declared
	var out []headerParam
	for _, propName := range names {
		var propSchema map[string]json.RawMessage
		if err := json.Unmarshal(parsed.Properties[propName], &propSchema); err != nil {
			// A non-object property schema (e.g. `"foo": true`) cannot carry
			// the annotation.
			continue
		}
		rawAnnotation, ok := propSchema[headerParamAnnotation]
		if !ok {
			continue
		}

		var headerName string
		if err := json.Unmarshal(rawAnnotation, &headerName); err != nil {
			return nil, newHeaderParamError(propName, string(rawAnnotation), "annotation must be a JSON string")
		}

		if err := headervalidate.ValidateName(headerName); err != nil {
			return nil, newHeaderParamError(propName, headerName, err.Error())
		}

		// See hasNonAllowlistedHeaderNameByte: this must run before the
		// denylist and collision checks below, since both compare
		// canonicalized names and http.CanonicalHeaderKey does not fold any
		// RFC 7230 token character other than the letter after a "-" the
		// way some backends' env-var mapping does (S6, S11).
		if hasNonAllowlistedHeaderNameByte(headerName) {
			return nil, newHeaderParamError(propName, headerName, `header name must consist only of letters, digits, and "-" (other characters collapse with "-" in some backends' header-to-env-var mapping)`)
		}

		canonical := http.CanonicalHeaderKey(headerName)

		// headervalidate.ValidateName only gates headervalidate.ReservedHeaderNames
		// -- a name an OPERATOR must not override. This is the separate,
		// x-mcp-header-specific gate against a REMOTE SERVER choosing a name
		// whose semantics act on other headers, or that carries an identity
		// the operator never granted. See deniedHeaderParamNames for the full
		// rationale.
		if isDeniedHeaderParamName(canonical) {
			return nil, newHeaderParamError(propName, headerName, "header name is not permitted for x-mcp-header (hop-by-hop/proxy-control header, or a header that can carry an identity or origin the operator did not grant)")
		}

		if headerNameConfiguredAsAuthHeader(canonical, authHeaders) {
			return nil, newHeaderParamError(propName, headerName, "header name collides with an auth header configured for this server")
		}

		if seen[canonical] {
			return nil, newHeaderParamError(propName, headerName, "declared by more than one tool parameter")
		}
		seen[canonical] = true

		v, ok := input[propName]
		if !ok {
			// Absent from input: no header to send. An absent-but-required
			// property is already rejected upstream by the pre-dispatch
			// ArgValidator, so this is not a hole.
			continue
		}

		value, err := formatHeaderParamValue(v)
		if err != nil {
			return nil, newHeaderParamError(propName, headerName, err.Error())
		}
		if len(value) > maxHeaderParamValueLen {
			return nil, newHeaderParamError(propName, headerName, fmt.Sprintf("value exceeds the %d-byte limit for an x-mcp-header value", maxHeaderParamValueLen))
		}
		if !httpguts.ValidHeaderFieldValue(value) {
			// Unlike Mcp-Name (client.go), which relies on net/http's
			// outbound check because it originates from the server, this
			// value originates from the LLM and is the likeliest
			// prompt-injection carrier: check it here so the agent gets a
			// correctable error instead of an opaque "http do" failure. The
			// value itself is never echoed into the error.
			return nil, newHeaderParamError(propName, headerName, "value contains characters that are not permitted in an HTTP header field value")
		}

		out = append(out, headerParam{Name: headerName, Value: value})
	}

	if len(out) > maxHeaderParams {
		return nil, newHeaderParamError("", "", fmt.Sprintf("tool resolves %d x-mcp-header parameters, exceeds limit of %d", len(out), maxHeaderParams))
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// formatHeaderParamValue converts an x-mcp-header-annotated argument value
// into its wire header-value form. Poll-trigger YAML inputs decode straight
// into Go int/int64 (no JSON round trip), while every other input path in
// this codebase produces string/bool/float64/json.Number -- both families
// are handled explicitly so neither path silently drops a legal value.
func formatHeaderParamValue(v any) (string, error) {
	switch tv := v.(type) {
	case string:
		return tv, nil
	case bool:
		return strconv.FormatBool(tv), nil
	case float64:
		// 'f', -1 avoids "1e+06" for integral float64 values like 1000000.
		return strconv.FormatFloat(tv, 'f', -1, 64), nil
	case json.Number:
		return tv.String(), nil
	case int:
		return strconv.FormatInt(int64(tv), 10), nil
	case int64:
		return strconv.FormatInt(tv, 10), nil
	default:
		return "", fmt.Errorf("value must be a string, number, or boolean")
	}
}
