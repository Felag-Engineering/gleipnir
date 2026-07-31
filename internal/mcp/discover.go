package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/felag-engineering/gleipnir/internal/infra/version"
)

// methodServerDiscover is the JSON-RPC method name for the 2026-07-28
// protocol-discovery probe.
const methodServerDiscover = "server/discover"

// _meta reserved keys used on every 2026-07-28 request (basic/index.md
// "Per-request protocol fields").
const (
	metaKeyProtocolVersion    = "io.modelcontextprotocol/protocolVersion"
	metaKeyClientInfo         = "io.modelcontextprotocol/clientInfo"
	metaKeyClientCapabilities = "io.modelcontextprotocol/clientCapabilities"
)

// MCP reserves JSON-RPC codes -32020..-32099 for spec-defined errors; a code
// in this range is proof the peer speaks a modern revision.
const (
	errCodeMCPReservedMax             = -32020
	errCodeMCPReservedMin             = -32099
	errCodeUnsupportedProtocolVersion = -32022
)

// supportedProtocolVersions lists the modern revisions Gleipnir speaks, most
// preferred first. Legacy versions are absent by design — they are never
// negotiated through server/discover.
var supportedProtocolVersions = []string{ProtocolVersion20260728}

// modernVersionsForQuery returns supportedProtocolVersions in the []*string
// shape sqlc generates for a slice parameter bound against the nullable
// mcp_servers.protocol_version column
// (db.UpdateMCPServerProtocolVersionIfNotModernParams.ModernVersions). Used
// by Registry.refreshProtocolVersion's no-downgrade guard (Finding 1,
// security review, #737 cycle 3) — the guard itself lives in the SQL WHERE
// clause, evaluated against the row's live state, not here.
func modernVersionsForQuery() []*string {
	out := make([]*string, len(supportedProtocolVersions))
	for i := range supportedProtocolVersions {
		out[i] = &supportedProtocolVersions[i]
	}
	return out
}

// knownLegacyProtocolVersions lists the pre-2026-07-28 MCP protocolVersion
// strings a legacy `initialize` handshake may legitimately report:
// 2024-11-05, 2025-03-26, and 2025-06-18 are the released spec revisions
// that predate the 2026-07-28 modern transport this package implements.
//
// This is the allowlist half of the Finding 1 fix (security review, #737
// cycle 2): negotiatedLegacyVersion() returns whatever string the remote
// server chose to put in its initialize result — an untrusted,
// attacker-controlled value — and sanitizedLegacyVersion is the sole gate
// between that string and persistence in mcp_servers.protocol_version.
// knownLegacyProtocolVersions and supportedProtocolVersions are disjoint by
// construction (asserted by TestLegacyAllowlistDisjointFromModernVersions),
// which is what makes the discoverLegacy branch of ProbeProtocolVersion
// structurally incapable of ever emitting a value classified modern — a
// server that fails server/discover classification cannot claim to be
// modern by echoing e.g. "2026-07-28" back on the initialize handshake.
var knownLegacyProtocolVersions = []string{
	ProtocolVersionLegacy, // "2024-11-05"
	"2025-03-26",
	"2025-06-18",
}

// legacyVersionMaxLen bounds a legacy server's self-reported protocolVersion
// before any other check runs — MCP protocol versions are "YYYY-MM-DD"
// tokens (10 bytes), so this is generous headroom, not a realistic limit.
// Finding 1 confirmed a 1 MiB string was persisted verbatim before this
// bound existed.
const legacyVersionMaxLen = 32

// sanitizedLegacyVersion returns raw when it is both well-formed (bounded
// length, digits and hyphens only — the "YYYY-MM-DD" shape) AND present in
// knownLegacyProtocolVersions; otherwise it returns "". The charset check and
// the allowlist check are each independently sufficient to reject a modern
// version string like "2026-07-28" or an injection attempt like
// "2026-07-28\r\nX-Injected: 1" — both are enforced as defense in depth.
//
// The caller falls back to ProtocolVersionLegacy when this returns "". A
// modern-looking string in protocol_version can therefore only ever have
// come from the discoverModern branch of ProbeProtocolVersion — this
// function is what makes that invariant hold, and it is why #737 needs no
// new "provenance" column: the value's shape is its own provenance.
func sanitizedLegacyVersion(raw string) string {
	if raw == "" || len(raw) > legacyVersionMaxLen {
		return ""
	}
	for i := 0; i < len(raw); i++ {
		b := raw[i]
		if !(b >= '0' && b <= '9') && b != '-' {
			return ""
		}
	}
	for _, known := range knownLegacyProtocolVersions {
		if raw == known {
			return known
		}
	}
	return ""
}

// truncateForLog bounds s to at most max bytes, appending an ellipsis marker
// when truncation occurred. Shared by every place this package formats an
// untrusted, server-controlled string into an error or log line, so there is
// exactly one truncate-with-ellipsis implementation (Finding 3, security
// review, #737 cycle 3, generalizing the truncation summarizeAdvertised
// already did per-entry).
func truncateForLog(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

// maxAdvertisedVersionsForLog and maxAdvertisedVersionLen bound how much of
// an untrusted server's advertised protocol-version list is ever formatted
// into an error or log line (Finding 4, security review, #737 cycle 2): a
// 200 response whose supportedVersions held 20,000 forty-char entries
// produced an 820 KB error string, emitted as one slog record on every
// create/refresh, before this cap existed. summarizeAdvertised is the sole
// place cls.Advertised is formatted for output; supportedProtocolVersions is
// our own small, fixed, trusted list and needs no such bound.
const (
	maxAdvertisedVersionsForLog = 10
	maxAdvertisedVersionLen     = 64
)

// maxModernErrMessageLen bounds how much of an untrusted server's -32xxx
// JSON-RPC error Message is ever formatted into an error or log line (see
// the boundedModernErr construction in ProbeProtocolVersion). More generous
// than maxAdvertisedVersionLen because a diagnostic error message is
// legitimately longer than a "YYYY-MM-DD" version token.
const maxModernErrMessageLen = 256

// summarizeAdvertised returns a bounded, log-safe rendering of an untrusted
// server's advertised protocol-version list: at most
// maxAdvertisedVersionsForLog entries, each truncated to
// maxAdvertisedVersionLen bytes, with a trailing "(and N more)" indicator
// when the list is longer than that.
func summarizeAdvertised(advertised []string) string {
	shown := len(advertised)
	if shown > maxAdvertisedVersionsForLog {
		shown = maxAdvertisedVersionsForLog
	}
	entries := make([]string, shown)
	for i := 0; i < shown; i++ {
		entries[i] = truncateForLog(advertised[i], maxAdvertisedVersionLen)
	}
	out := fmt.Sprintf("%v", entries)
	if len(advertised) > shown {
		out = fmt.Sprintf("%s (and %d more)", out, len(advertised)-shown)
	}
	return out
}

// discoverResult is the wire shape of a successful server/discover response
// (spec A2). Note the field is "supportedVersions", NOT "supported" — the
// latter is only used in the -32022 error's data (see unsupportedVersionData).
type discoverResult struct {
	ResultType        string   `json:"resultType"` // absent ⇒ "complete"
	SupportedVersions []string `json:"supportedVersions"`
}

// unsupportedVersionData is the "data" payload of a -32022
// UnsupportedProtocolVersion error (spec A3). Note the field is "supported",
// NOT "supportedVersions" — the two error/result shapes disagree on purpose
// per the spec.
type unsupportedVersionData struct {
	Supported []string `json:"supported"`
	Requested string   `json:"requested"`
}

// ProtocolEra identifies which MCP revision family a ProbeResult's Version
// came from.
type ProtocolEra string

const (
	EraModern ProtocolEra = "modern"
	EraLegacy ProtocolEra = "legacy"
)

// ProbeResult is the outcome of a protocol-version probe against one server.
type ProbeResult struct {
	Version         string      // version to pin; always non-empty on success
	Era             ProtocolEra // where Version came from
	ServerSupported []string    // raw advertised list, server order; diagnostic only
}

// ErrNoCompatibleProtocolVersion reports a server that is definitively
// modern — it answered server/discover with a recognized modern response —
// but shares no protocol version with Gleipnir. The probe surfaces this
// rather than downgrading to the legacy initialize handshake; see the plan's
// Key decisions section. Mirrors the existing ErrToolNamespaceConflict
// sentinel precedent in this package.
var ErrNoCompatibleProtocolVersion = errors.New("no mutually supported MCP protocol version")

// discoverOutcome is the pure classification of a server/discover response,
// independent of what the probe does with it.
type discoverOutcome int

const (
	discoverUnclassified discoverOutcome = iota // no era signal: pin nothing
	discoverModern
	discoverLegacy
)

// discoverClassification is the result of classifyDiscoverResponse.
type discoverClassification struct {
	Outcome    discoverOutcome
	Advertised []string      // supportedVersions, or -32022 data.supported
	ModernErr  *JSONRPCError // the recognized MCP-reserved error, if any
}

// classifyDiscoverResponse implements the HTTP era-detection algorithm from
// the 2026-07-28 streamable-HTTP transport spec (context.md A5).
func classifyDiscoverResponse(status int, payload []byte) discoverClassification {
	switch status {
	case http.StatusOK, http.StatusAccepted, http.StatusBadRequest, http.StatusNotFound, http.StatusMethodNotAllowed:
		// Proceed to inspect the body — the spec instructs a client to check
		// these statuses for a recognized modern JSON-RPC error before
		// concluding the server is legacy.
	default:
		// Any other status (401/403/429/5xx/…) carries no era signal we can
		// trust — a modern server can return those for reasons unrelated to
		// protocol negotiation (auth, rate limiting, an unrelated outage).
		// Leave the classification open rather than guessing.
		return discoverClassification{Outcome: discoverUnclassified}
	}

	var envelope jsonrpcResponse
	if err := json.Unmarshal(payload, &envelope); err != nil {
		// Not a JSON-RPC envelope at all — the hallmark of a legacy
		// HTTP+SSE server's opaque 4xx.
		return discoverClassification{Outcome: discoverLegacy}
	}

	if envelope.Error != nil {
		code := envelope.Error.Code
		if code < errCodeMCPReservedMin || code > errCodeMCPReservedMax {
			// Only the MCP-reserved range is unambiguously modern; -32602/
			// -32601 are equally producible by a legacy framework, and our
			// probe is A1-compliant so a compliant modern server never
			// answers -32602. This is the only rule that cannot mistake a
			// legacy server for a modern one.
			return discoverClassification{Outcome: discoverLegacy}
		}
		cls := discoverClassification{Outcome: discoverModern, ModernErr: envelope.Error}
		if code == errCodeUnsupportedProtocolVersion {
			var data unsupportedVersionData
			if err := json.Unmarshal(envelope.Error.Data, &data); err == nil {
				cls.Advertised = data.Supported
			}
		}
		return cls
	}

	if envelope.Result != nil {
		var result discoverResult
		// Per A2, an absent resultType means "complete" — we don't need to
		// read it; a populated supportedVersions list is definitive on its
		// own. A result without supportedVersions (e.g. a generic
		// tools/list-shaped fake, or "{}") gives us nothing to pin, so it is
		// classified legacy rather than unclassified.
		if err := json.Unmarshal(envelope.Result, &result); err == nil && len(result.SupportedVersions) > 0 {
			return discoverClassification{Outcome: discoverModern, Advertised: result.SupportedVersions}
		}
		return discoverClassification{Outcome: discoverLegacy}
	}

	return discoverClassification{Outcome: discoverLegacy}
}

// selectProtocolVersion returns the first entry of supportedProtocolVersions
// that also appears in advertised, or "" when there is no overlap.
// Preference is ours; the server's ordering is advisory.
func selectProtocolVersion(advertised []string) string {
	for _, ours := range supportedProtocolVersions {
		for _, theirs := range advertised {
			if ours == theirs {
				return ours
			}
		}
	}
	return ""
}

// ProbeProtocolVersion determines which MCP protocol revision the server
// speaks and returns the version to pin. It sends one spec-shaped
// server/discover POST and, when the server is NOT confirmed modern, falls
// back to the legacy initialize handshake. A confirmed-modern server that
// shares no version with us is an error, not a legacy server. It writes
// nothing.
func (c *Client) ProbeProtocolVersion(ctx context.Context) (ProbeResult, error) {
	// The MCP-Protocol-Version header value MUST match _meta's
	// protocolVersion (A4); write it from one variable so they cannot drift.
	const requestedVersion = ProtocolVersion20260728

	body, err := json.Marshal(jsonrpcRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  methodServerDiscover,
		Params: map[string]any{
			"_meta": map[string]any{
				metaKeyProtocolVersion: requestedVersion,
				metaKeyClientInfo: map[string]any{
					"name":    "gleipnir",
					"version": version.Version,
				},
				metaKeyClientCapabilities: map[string]any{},
			},
		},
	})
	if err != nil {
		return ProbeResult{}, fmt.Errorf("marshal server/discover request: %w", err)
	}

	// sessionID stays empty (sessions are gone in this revision) and
	// Mcp-Name is never set — it does not apply to server/discover.
	resp, postErr := c.post(ctx, body, postOptions{
		protocolVersion: requestedVersion,
		rpcMethod:       methodServerDiscover,
	})

	var (
		status    int
		payload   []byte
		statusErr *HTTPStatusError
	)
	switch {
	case postErr == nil:
		defer resp.Body.Close()
		status = resp.StatusCode
		payload, err = readJSONRPCPayload(resp)
		if err != nil {
			return ProbeResult{}, fmt.Errorf("server/discover probe: read response: %w", err)
		}
	case errors.As(postErr, &statusErr):
		status = statusErr.StatusCode
		payload = statusErr.Body
	default:
		// Transport error with no HTTP response at all.
		return ProbeResult{}, fmt.Errorf("server/discover probe: %w", postErr)
	}

	cls := classifyDiscoverResponse(status, payload)

	switch cls.Outcome {
	case discoverModern:
		// A modern classification is definitive: the server is not legacy,
		// so the legacy handshake is never attempted from this branch.
		if v := selectProtocolVersion(cls.Advertised); v != "" {
			return ProbeResult{Version: v, Era: EraModern, ServerSupported: cls.Advertised}, nil
		}
		// Confirmed modern, nothing in common. basic/versioning.md §Protocol
		// Version Negotiation gives exactly two options — retry with a
		// mutually supported version, or surface an error when none exists —
		// and §Backward Compatibility says a recognized modern error means
		// the client retries "rather than falling back". Pin nothing;
		// re-probe next refresh.
		if cls.ModernErr != nil {
			// Finding 3 (security review, #737 cycle 3): cls.ModernErr is an
			// untrusted, server-controlled error whose Message
			// JSONRPCError.Error() interpolates verbatim — an 8 MiB Message
			// produced an 8,388,774-byte error string here, emitted as one
			// slog.Warn on every create/refresh. Wrap a bounded copy
			// (Message truncated, Data dropped) rather than cls.ModernErr
			// itself so the formatted text stays bounded; the copy still
			// satisfies errors.As(*JSONRPCError) for callers that only need
			// Code (see TestProbeProtocolVersion_VersionMismatchErrorWrapsJSONRPCError).
			boundedModernErr := &JSONRPCError{
				Code:    cls.ModernErr.Code,
				Message: truncateForLog(cls.ModernErr.Message, maxModernErrMessageLen),
			}
			return ProbeResult{}, fmt.Errorf(
				"server/discover rejected by modern server (advertised %s, gleipnir speaks %v): %w: %w",
				summarizeAdvertised(cls.Advertised), supportedProtocolVersions, ErrNoCompatibleProtocolVersion, boundedModernErr)
		}
		return ProbeResult{}, fmt.Errorf(
			"server/discover advertised %s, gleipnir speaks %v: %w",
			summarizeAdvertised(cls.Advertised), supportedProtocolVersions, ErrNoCompatibleProtocolVersion)

	case discoverLegacy:
		// Reached ONLY from discoverLegacy. Using ensureSession (not
		// initialize) means the handshake is cached on this client, so a
		// probe+DiscoverTools pair on the same client costs one handshake.
		if _, err := c.ensureSession(ctx); err != nil {
			return ProbeResult{}, fmt.Errorf("legacy initialize fallback: %w", err)
		}
		// sanitizedLegacyVersion is the Finding 1 gate: it accepts the
		// server's self-reported protocolVersion only when it is a
		// well-formed, allowlisted legacy version, so this branch can never
		// emit a value that appears in supportedProtocolVersions.
		v := sanitizedLegacyVersion(c.negotiatedLegacyVersion())
		if v == "" {
			v = ProtocolVersionLegacy
		}
		return ProbeResult{Version: v, Era: EraLegacy}, nil

	default: // discoverUnclassified
		return ProbeResult{}, inconclusiveProbeError(status, statusErr)
	}
}

// inconclusiveProbeError builds the error returned for a discoverUnclassified
// outcome. Finding 6 (security review, #737 cycle 2): statusErr is
// guaranteed non-nil here today — discoverUnclassified is only reached via
// the non-2xx (errors.As) branch above, since the postErr == nil branch can
// only carry status 200/202, both of which classifyDiscoverResponse always
// resolves to modern or legacy. A future classifier edit could break that
// invariant; formatting a nil *HTTPStatusError with %w calls its Error()
// method on a nil receiver and panics, so this guards explicitly rather than
// relying on the invariant holding forever.
func inconclusiveProbeError(status int, statusErr *HTTPStatusError) error {
	if statusErr == nil {
		return fmt.Errorf("server/discover probe inconclusive: status %d", status)
	}
	return fmt.Errorf("server/discover probe inconclusive: %w", statusErr)
}
