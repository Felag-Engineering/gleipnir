// Run attribution (issue #943): an optional per-server setting that asserts,
// via a small set of headers, which Gleipnir agent and run a tools/call (and
// each MRTR retry) is on behalf of. These are CLAIMS the server records, not
// credentials -- verified identity is out of scope (see the issue). Values
// are built only from run context, never the model or tool arguments; see
// RunAttribution's and buildAttributionHeaders's docs for the structural
// argument.
package mcp

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/felag-engineering/gleipnir/internal/infra/headervalidate"
)

// RunAttributionMode selects whether, and how, run attribution headers are
// sent to an MCP server.
type RunAttributionMode string

const (
	RunAttributionOff    RunAttributionMode = "off"
	RunAttributionRelay  RunAttributionMode = "relay"
	RunAttributionCustom RunAttributionMode = "custom"
)

// The Relay preset header names. These mirror gleipnir-relay
// relay/internal/controlplane/assertions.go verbatim.
// X-Relay-Change-Ref also exists there and is out of scope for #943.
const (
	RelayOnBehalfOfHeader  = "X-Relay-On-Behalf-Of"
	RelaySessionRefHeader  = "X-Relay-Session-Ref"
	RelayTraceparentHeader = "traceparent"
)

// RunAttributionConfig is the JSON shape stored (canonically re-marshaled)
// in mcp_servers.run_attribution. It is plaintext, like ca_cert_pem and
// call_timeout_seconds: header names are not secrets and must read back.
type RunAttributionConfig struct {
	Mode              RunAttributionMode `json:"mode"`
	OnBehalfOfHeader  string             `json:"on_behalf_of_header,omitempty"`
	SessionRefHeader  string             `json:"session_ref_header,omitempty"`
	TraceparentHeader string             `json:"traceparent_header,omitempty"`
}

// AttributionHeaderNames is the effective set of header names a Client sends
// run attribution under. An empty field means that header is not sent.
type AttributionHeaderNames struct {
	OnBehalfOf  string
	SessionRef  string
	Traceparent string
}

// IsZero reports whether no attribution header name is configured at all.
func (n AttributionHeaderNames) IsZero() bool {
	return n == AttributionHeaderNames{}
}

// list returns the non-empty names, in the fixed order on-behalf-of,
// session-ref, traceparent.
func (n AttributionHeaderNames) list() []string {
	var names []string
	for _, name := range []string{n.OnBehalfOf, n.SessionRef, n.Traceparent} {
		if name != "" {
			names = append(names, name)
		}
	}
	return names
}

// EffectiveHeaderNames returns the header names c sends: the zero value for
// off, the Relay preset constants for relay, and the configured fields for
// custom.
func (c RunAttributionConfig) EffectiveHeaderNames() AttributionHeaderNames {
	switch c.Mode {
	case RunAttributionRelay:
		return AttributionHeaderNames{
			OnBehalfOf:  RelayOnBehalfOfHeader,
			SessionRef:  RelaySessionRefHeader,
			Traceparent: RelayTraceparentHeader,
		}
	case RunAttributionCustom:
		return AttributionHeaderNames{
			OnBehalfOf:  c.OnBehalfOfHeader,
			SessionRef:  c.SessionRefHeader,
			Traceparent: c.TraceparentHeader,
		}
	default:
		return AttributionHeaderNames{}
	}
}

// ParseRunAttributionColumn decodes a stored mcp_servers.run_attribution
// value. nil, "", or all-whitespace means off. Unknown JSON fields and an
// unrecognized mode are both errors -- the caller decides how to fail;
// serverToResponse and newClientForServer both treat a parse error as off
// (with a slog.Warn), which matches runtime: nothing is sent for a value
// that no longer parses.
func ParseRunAttributionColumn(stored *string) (RunAttributionConfig, error) {
	if stored == nil || strings.TrimSpace(*stored) == "" {
		return RunAttributionConfig{Mode: RunAttributionOff}, nil
	}
	dec := json.NewDecoder(strings.NewReader(*stored))
	dec.DisallowUnknownFields()
	var cfg RunAttributionConfig
	if err := dec.Decode(&cfg); err != nil {
		return RunAttributionConfig{}, fmt.Errorf("parse run_attribution: %w", err)
	}
	switch cfg.Mode {
	case RunAttributionOff, RunAttributionRelay, RunAttributionCustom:
	default:
		return RunAttributionConfig{}, fmt.Errorf("parse run_attribution: unknown mode %q", cfg.Mode)
	}
	return cfg, nil
}

// Column returns the canonical stored form of c: nil for off, the marshal of
// {"mode":"relay"} for relay (the preset names are Go constants, never
// stored, so a preset fix never needs a data migration), and the full
// marshal for custom. The handler always stores this canonical re-marshal
// rather than the caller's raw JSON, so string equality in the client cache
// key (cache.go) is stable.
func (c RunAttributionConfig) Column() (*string, error) {
	switch c.Mode {
	case RunAttributionOff, "":
		return nil, nil
	case RunAttributionRelay:
		b, err := json.Marshal(RunAttributionConfig{Mode: RunAttributionRelay})
		if err != nil {
			return nil, fmt.Errorf("marshal run_attribution: %w", err)
		}
		s := string(b)
		return &s, nil
	case RunAttributionCustom:
		b, err := json.Marshal(c)
		if err != nil {
			return nil, fmt.Errorf("marshal run_attribution: %w", err)
		}
		s := string(b)
		return &s, nil
	default:
		return nil, fmt.Errorf("marshal run_attribution: unknown mode %q", c.Mode)
	}
}

// ValidateRunAttribution normalises c (TrimSpace on every header name; for
// off and relay, any supplied names are IGNORED rather than rejected, so a
// UI that round-trips the read-back response object never gets a 400) and,
// for custom, validates the effective header names against D3: RFC 7230
// token syntax plus the reserved-name list (headervalidate.ValidateName),
// the [A-Za-z0-9-] allowlist and denylist this package also applies to
// x-mcp-header names (headerparams.go), mutual distinctness, and no
// collision with authHeaderNames (the server's ADR-039 auth header names).
// Every failure names the offending field ("on_behalf_of_header: ...").
//
// Mode matching is strict and case-sensitive: only the exact lowercase
// strings "off", "relay", and "custom" are recognized. "Off" or "RELAY" is
// an unknown mode, not a case-insensitive match of a known one -- this
// mirrors the wire contract, which only ever emits and accepts lowercase
// mode values.
//
// authHeaderNames is checked against EffectiveHeaderNames() for BOTH relay
// and custom: an operator who names an auth header "traceparent" (or any of
// the other two Relay preset constants) and then selects the Relay preset
// must see the collision at save time, not learn about it later from a
// silent drop at client build (registry.go's dropUnsafeAttributionNames).
// The names themselves are trusted constants for relay, so only the
// collision check applies there -- the reserved/denylist/allowlist checks
// below are for custom's operator-supplied names.
func ValidateRunAttribution(c RunAttributionConfig, authHeaderNames []string) (RunAttributionConfig, error) {
	switch c.Mode {
	case RunAttributionOff:
		return RunAttributionConfig{Mode: RunAttributionOff}, nil
	case RunAttributionRelay:
		normalized := RunAttributionConfig{Mode: RunAttributionRelay}
		for _, f := range attributionFields(normalized) {
			if err := checkAuthHeaderCollision(f.field, f.name, authHeaderNames); err != nil {
				return RunAttributionConfig{}, err
			}
		}
		return normalized, nil
	case RunAttributionCustom:
		// validated below
	default:
		return RunAttributionConfig{}, fmt.Errorf("mode: unknown mode %q, must be one of off, relay, custom", c.Mode)
	}

	normalized := RunAttributionConfig{
		Mode:              RunAttributionCustom,
		OnBehalfOfHeader:  strings.TrimSpace(c.OnBehalfOfHeader),
		SessionRefHeader:  strings.TrimSpace(c.SessionRefHeader),
		TraceparentHeader: strings.TrimSpace(c.TraceparentHeader),
	}
	if normalized.EffectiveHeaderNames().IsZero() {
		return RunAttributionConfig{}, fmt.Errorf("mode custom requires at least one header name")
	}

	var configured []string // canonical names already validated, for the mutual-distinctness check
	for _, f := range attributionFields(normalized) {
		if f.name == "" {
			continue
		}
		if err := headervalidate.ValidateName(f.name); err != nil {
			return RunAttributionConfig{}, fmt.Errorf("%s: %w", f.field, err)
		}
		// See hasNonAllowlistedHeaderNameByte's doc (headerparams.go): this
		// must run before the canonical-name comparisons below, since
		// http.CanonicalHeaderKey does not fold every RFC 7230 token
		// character the way some backends' header-to-env-var mapping does.
		if hasNonAllowlistedHeaderNameByte(f.name) {
			return RunAttributionConfig{}, fmt.Errorf(`%s: header name must consist only of letters, digits, and "-"`, f.field)
		}
		canonical := http.CanonicalHeaderKey(f.name)
		if isDeniedHeaderParamName(canonical) {
			return RunAttributionConfig{}, fmt.Errorf("%s: header name is not permitted for run attribution (hop-by-hop/proxy-control header, or a header that can carry an identity or origin the operator did not grant)", f.field)
		}
		for _, existing := range configured {
			if existing == canonical {
				return RunAttributionConfig{}, fmt.Errorf("%s: header name must be distinct from the other run attribution header names", f.field)
			}
		}
		if err := checkAuthHeaderCollision(f.field, f.name, authHeaderNames); err != nil {
			return RunAttributionConfig{}, err
		}
		configured = append(configured, canonical)
	}

	return normalized, nil
}

// attributionField pairs a wire field name with the header name (possibly
// empty) c.EffectiveHeaderNames() resolved it to.
type attributionField struct {
	field string
	name  string
}

// attributionFields returns c's three fields in fixed order, so the
// relay and custom branches of ValidateRunAttribution can share the same
// per-field loop shape.
func attributionFields(c RunAttributionConfig) []attributionField {
	names := c.EffectiveHeaderNames()
	return []attributionField{
		{"on_behalf_of_header", names.OnBehalfOf},
		{"session_ref_header", names.SessionRef},
		{"traceparent_header", names.Traceparent},
	}
}

// checkAuthHeaderCollision returns an error naming field when name -- an
// already-safe header name, either the Relay preset's own constant or an
// operator-supplied custom name that has already passed the reserved/
// denylist/allowlist checks -- canonically matches one of authHeaderNames.
// Returns nil for an empty name (nothing configured for that field) or no
// collision.
func checkAuthHeaderCollision(field, name string, authHeaderNames []string) error {
	if name == "" {
		return nil
	}
	canonical := http.CanonicalHeaderKey(name)
	for _, authName := range authHeaderNames {
		if http.CanonicalHeaderKey(authName) == canonical {
			return fmt.Errorf("%s: header name collides with this server's auth headers", field)
		}
	}
	return nil
}

// AttributionNamesCollide reports whether headerName -- an ADR-039 auth
// header name being written via SetAuthHeader -- canonically matches one of
// stored's effective run attribution header names. A stored value that
// fails to parse is treated as "no attribution names configured": there is
// nothing to collide with, and refusing an unrelated auth-header write
// because a run_attribution column is already unparseable would be the
// wrong failure mode.
func AttributionNamesCollide(stored *string, headerName string) (bool, error) {
	cfg, err := ParseRunAttributionColumn(stored)
	if err != nil {
		return false, nil
	}
	canonical := http.CanonicalHeaderKey(headerName)
	for _, name := range cfg.EffectiveHeaderNames().list() {
		if http.CanonicalHeaderKey(name) == canonical {
			return true, nil
		}
	}
	return false, nil
}

// RunAttribution carries the run-context values a Client turns into
// attribution header values on one CallTool. Every field is host-owned,
// populated only by BoundAgent from the parsed policy's name, the run ID,
// the authenticated triggering user (manual runs only), and the system
// public_url snapshot taken at launch -- never from the model or tool
// arguments. buildAttributionHeaders, which turns this into wire headers,
// takes no access to a tool's input at all, which is the structural half of
// that invariant; the other half is that nothing upstream of here ever
// populates these fields FROM input either (see agent.go's runAttribution
// method).
type RunAttribution struct {
	AgentName   string
	RunID       string
	TriggeredBy string
	PublicURL   string
}

// IsZero reports whether there is no run to attribute a call to. RunID is
// the only field checked: it is the join key, always set for a real run,
// and empty for every call site with no run context (poll, discovery,
// probes), which is exactly when nothing should be sent regardless of
// whatever names a server is configured with.
func (a RunAttribution) IsZero() bool {
	return a.RunID == ""
}

// maxAttributionValueBytes bounds every attribution header value, mirroring
// Relay's audit.MaxAssertedFieldBytes
// (relay/internal/audit/attribution.go) so a long policy name or run URL
// never makes Relay reject the call with a 400.
const maxAttributionValueBytes = 256

// sanitizeControl replaces every C0/C1 control character or DEL with a
// space (mirroring Relay's isControl exactly), repairs invalid UTF-8, and
// trims the result -- everything sanitizeAttributionValue does except the
// length cap. Split out so sessionRefValue can measure the sanitized,
// untruncated length of the with-URL form before deciding whether to keep
// or drop the URL (see its doc): truncating first would risk cutting into
// the run ID.
func sanitizeControl(s string) string {
	s = strings.ToValidUTF8(s, "�")
	s = strings.Map(func(r rune) rune {
		if isAttributionControlRune(r) {
			return ' '
		}
		return r
	}, s)
	return strings.TrimSpace(s)
}

// isAttributionControlRune mirrors Relay's isControl
// (relay/internal/audit/attribution.go) exactly: a C0/C1 control character
// or DEL. Everything else, including any printable non-ASCII rune, is left
// alone -- a team or agent name may legitimately be non-ASCII.
func isAttributionControlRune(r rune) bool {
	return r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f)
}

// sanitizeAttributionValue shapes s to pass Relay's audit.ValidateAsserted
// rule (len(bytes) <= 256, no control rune) and, as a consequence,
// httpguts.ValidHeaderFieldValue. Gleipnir composes these values end to
// end, so shaping them to fit is the right move here in a way it is not for
// extractHeaderParams's LLM-supplied x-mcp-header values (refuse, don't
// rewrite, because there the value belongs to the model and a silent
// rewrite could hide a prompt-injection attempt) -- these values are
// host-composed, so truncating one is simply bounding a legitimate string.
func sanitizeAttributionValue(s string) string {
	s = sanitizeControl(s)
	if len(s) <= maxAttributionValueBytes {
		return s
	}
	// Cut back to the last rune boundary at or below the byte limit, never
	// mid-rune.
	cut := maxAttributionValueBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// onBehalfOfValue composes the on-behalf-of header value: the agent (policy)
// name, plus the triggering user for a manual run.
func onBehalfOfValue(a RunAttribution) string {
	name := sanitizeAttributionValue(a.AgentName)
	triggeredBy := sanitizeAttributionValue(a.TriggeredBy)
	value := name
	if triggeredBy != "" {
		value = fmt.Sprintf("%s (triggered by %s)", name, triggeredBy)
	}
	return sanitizeAttributionValue(value)
}

// sessionRefValue composes the session-ref header value: "gleipnir run
// <RunID>", plus the run's URL in parentheses when PublicURL is set.
//
// The run ID -- the join key -- is never truncated away: if the with-URL
// form does not fit within maxAttributionValueBytes once sanitized, the URL
// is dropped entirely and the plain form is returned, rather than truncating
// the composed string and risking cutting into the run ID itself.
func sessionRefValue(a RunAttribution) string {
	plain := sanitizeAttributionValue(fmt.Sprintf("gleipnir run %s", a.RunID))

	publicURL := sanitizeAttributionValue(a.PublicURL)
	if publicURL == "" {
		return plain
	}

	runURL := strings.TrimRight(publicURL, "/") + "/runs/" + a.RunID
	withURL := sanitizeControl(fmt.Sprintf("gleipnir run %s (%s)", a.RunID, runURL))
	if len(withURL) > maxAttributionValueBytes {
		return plain
	}
	return withURL
}

// traceIDFor deterministically derives a W3C trace-id from a run ID: the
// lower-hex first 16 bytes of sha256("gleipnir-run/" + runID). Deterministic
// and one code path, so it works for any run ID string (including
// non-ULIDs, as in tests). On the theoretical all-zero draw, the last byte
// is forced to 1 -- W3C forbids an all-zero trace-id.
func traceIDFor(runID string) string {
	sum := sha256.Sum256([]byte("gleipnir-run/" + runID))
	id := sum[:16]
	if allZero(id) {
		id[15] = 1
	}
	return hex.EncodeToString(id)
}

func allZero(b []byte) bool {
	for _, v := range b {
		if v != 0 {
			return false
		}
	}
	return true
}

// newSpanID draws a fresh 8-byte W3C span-id for one CallTool (and
// therefore one per MRTR retry round, since each round is a separate HTTP
// request). A package var, not a plain function, so tests can pin it --
// this is a randomness seam, not a time seam, so it does not reuse the
// package's timeNow clock (cache.go).
var newSpanID = func() [8]byte {
	for {
		var b [8]byte
		if _, err := rand.Read(b[:]); err != nil {
			// crypto/rand.Read on the standard reader does not fail in
			// practice; retrying is safer than emitting a low-entropy or
			// all-zero span-id if it somehow does.
			continue
		}
		if !allZero(b[:]) {
			return b
		}
	}
}

// traceparentValue formats a W3C traceparent header value: version "00", the
// run's trace-id, the given span-id, and flags "01". Always exactly 55 bytes
// of lower hex, so it never needs sanitizing -- it is still asserted
// verbatim in tests.
func traceparentValue(runID string, span [8]byte) string {
	return fmt.Sprintf("00-%s-%s-01", traceIDFor(runID), hex.EncodeToString(span[:]))
}

// attributionHeader is one resolved run-attribution header name/value pair.
// Deliberately not AuthHeader (operator-configured, encrypted) or
// headerParam (model-chosen, from tool arguments): this is host-asserted,
// from run context only, and belongs to neither existing category.
type attributionHeader struct {
	Name  string
	Value string
}

// buildAttributionHeaders resolves names against a and span into the
// headers one CallTool should send, or nil when there is nothing to send:
// no attribution names configured for this server (names.IsZero()), or no
// run to attribute (a.IsZero()) -- so poll and discovery, which always pass
// a zero RunAttribution, never send anything even against a server
// configured with names.
//
// Deliberately takes no access to a tool's input/arguments: this is the
// structural half of the "never from the model or tool arguments"
// invariant. There is nothing here a hostile argument could reach.
func buildAttributionHeaders(names AttributionHeaderNames, a RunAttribution, span [8]byte) []attributionHeader {
	if names.IsZero() || a.IsZero() {
		return nil
	}
	var out []attributionHeader
	if names.OnBehalfOf != "" {
		out = append(out, attributionHeader{Name: names.OnBehalfOf, Value: onBehalfOfValue(a)})
	}
	if names.SessionRef != "" {
		out = append(out, attributionHeader{Name: names.SessionRef, Value: sessionRefValue(a)})
	}
	if names.Traceparent != "" {
		out = append(out, attributionHeader{Name: names.Traceparent, Value: traceparentValue(a.RunID, span)})
	}
	return out
}
