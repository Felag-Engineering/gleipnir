// TTL cache-hint parsing and the Registry's per-server client and
// tool-catalog caches (mcp-realignment-spec.md §11).
package mcp

import (
	"context"
	"encoding/json"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
)

// timeNow is package mcp's first injectable clock (CLAUDE.md "Testing
// time-dependent code"). Every TTL read in this package goes through this
// var — clientForServer and discoverToolsCached below are the only callers,
// comparing timeNow() against a cachedToolList's expiresAt. Tests swap it via
// t.Cleanup and must not use t.Parallel(): mutating a package-level var
// across parallel subtests races.
//
// Three direct time.Now() calls remain in this package's non-_test.go files.
// None of them is a TTL read, so none is rerouted through timeNow — this
// list is enumerated here, rather than left for a future reader to
// rediscover by grepping, precisely so a "why isn't this one routed through
// the clock too?" question has an answer on file. Referenced by symbol/file,
// not line number — line numbers rot; maxCacheHintTTL's doc below already
// follows the durable constantName (file.go) convention, and this list does
// the same:
//   - RefreshTools's created_at/last_discovered_at RFC3339Nano string
//     (registry.go), written on every upsert. A persisted wall-clock
//     timestamp, not a cache-freshness comparison; freezing it would corrupt
//     the audit-visible discovery time, not make anything more testable.
//   - The Prometheus request-duration histogram observation in CallTool
//     (client.go). A frozen clock would make every observation report a
//     duration of 0, silently breaking the metric rather than making it
//     deterministic.
//   - The created_at string RegisterServerForTest writes when seeding a test
//     server row (registertest.go). That file has no _test.go suffix (it is
//     compiled into the package so other packages' tests can call it), so it
//     surfaces in a package-wide grep for time.Now() even though it never
//     runs in production; like RefreshTools's timestamp above, it writes a
//     persisted timestamp, not a TTL read.
var timeNow = func() time.Time { return time.Now() }

// cacheScope is the 2026-07-28 schema's CacheableResult.cacheScope enum:
// "private" (the response is specific to this client/session) or "public"
// (safe to share more broadly).
//
// parseCacheHint parses and returns "private" verbatim rather than treating
// it as absent — it reports what the server actually said, nothing more.
// The caching decision itself lives at the store site, discoverToolsCached
// below, which refuses to install a cache entry for a hint scoped "private":
// see its doc for why. Do not "simplify" this by having parseCacheHint
// reject private outright — a future caller that only wants to know what the
// server advertised (e.g. a diagnostics surface) would lose that information.
type cacheScope string

const (
	cacheScopePrivate cacheScope = "private"
	cacheScopePublic  cacheScope = "public"
)

// cacheHint is the parsed, host-clamped form of a tools/list result's
// ttlMs/cacheScope fields. The zero value (Present == false) is "no usable
// hint" — callers must treat it exactly like a legacy server: always
// re-fetch, never cache.
//
// Present && TTL == 0 is the spec's "immediately stale" case (ttlMs: 0 means
// the result must not be cached at all, not "cache it for zero seconds").
// Consumers must express freshness as timeNow().Before(expiresAt), with
// expiresAt computed once at store time as timeNow().Add(hint.TTL) — never
// infer freshness from Present alone — so a zero TTL can never serve a cache
// hit.
type cacheHint struct {
	Present bool
	TTL     time.Duration
	Scope   cacheScope
}

// maxCacheHintTTL ceilings every cache hint this package honors, regardless
// of what a server advertises. This is a deliberate product decision, not an
// arbitrary safety number:
//
//   - Why a ceiling at all: ttlMs is server-controlled and unbounded above in
//     the schema. This package already applies the same bounded-untrusted-value
//     discipline to other server-controlled values — maxResultTypeLen
//     (client.go), maxServerInfoFieldLen (meta.go), legacyVersionMaxLen
//     (discover.go), maxErrorBodyBytes (client.go) — and a cache hint is no
//     different. The clamp also removes the ms→time.Duration overflow: int64
//     nanoseconds overflow above roughly 9.2e12 ms, and a hostile or merely
//     buggy server can send any int64 value in ttlMs. The clamp is applied to
//     the integer millisecond value BEFORE multiplying by time.Millisecond
//     (see maxCacheHintTTLMs and parseCacheHint) specifically so the overflow
//     can never happen in the first place, rather than happening and then
//     being corrected.
//   - Why it is legitimate to clamp downward: ttlMs is a max-freshness hint
//     ("you may cache this for up to this long"), not a minimum-refetch
//     mandate. A client that fetches more often than the advertised TTL is
//     always spec-compliant, so a host-side ceiling below a server's
//     advertised value never violates the caching contract.
//   - Why 60 seconds specifically: the only production caller of the cached
//     discovery path today is the operator's explicit Discover action
//     (MCPHandler.Discover → Registry.RefreshTools); there is no periodic
//     rediscovery loop anywhere in this repo. 60 seconds is short enough that
//     a cache hit is operationally indistinguishable from a fresh fetch — an
//     operator who presses Discover, changes something on the server, and
//     presses again is never left staring at stale data — while still
//     absorbing accidental double-presses and two operators discovering the
//     same server at the same time.
//   - Revisit trigger: this ceiling is deliberately conservative and should
//     be revisited upward only if a periodic rediscovery loop, or another
//     high-frequency consumer of RefreshTools, is ever added. Until then,
//     leave it at 60 seconds rather than guessing at a "better" number with
//     no caller to justify it.
const maxCacheHintTTL = 60 * time.Second

// maxCacheHintTTLMs is maxCacheHintTTL expressed as the integer millisecond
// value ttlMs is clamped against. parseCacheHint compares the raw ttlMs
// integer to this constant, and only then converts the (already-clamped)
// value to a time.Duration — see maxCacheHintTTL's doc for why the clamp
// must happen on the integer, before the multiply.
const maxCacheHintTTLMs = int64(maxCacheHintTTL / time.Millisecond)

// parseCacheHint parses a tools/list result's ttlMs/cacheScope fields into a
// cacheHint, or returns the zero value (Present == false) — meaning "behave
// exactly as if no hint had ever existed" — when ANY of the following holds:
//
//   - modern is false. This is the same isModernProtocol() predicate
//     requestMeta (meta.go) and the x-mcp-header extraction (client.go) gate
//     on, so a legacy or never-probed server that happens to emit ttlMs (a
//     coincidence, or a proxy injecting it) changes nothing about this
//     package's behavior toward it. Contrast this deliberately with
//     normalizeResultType (client.go), which is intentionally NOT gated on
//     modern, because "absent ⇒ complete" exists FOR legacy servers; a cache
//     hint has no such pre-2026 meaning to preserve.
//   - rawTTLMs is empty (the field was absent from the response) or is the
//     JSON literal null. Both are checked explicitly rather than left to the
//     unmarshal-into-int64 error path below, because
//     json.Unmarshal([]byte("null"), &int64Var) SUCCEEDS and leaves the
//     variable at its zero value 0 — silently indistinguishable from a
//     genuine "ttlMs: 0" hint if this case were not special-cased first.
//   - rawTTLMs fails to unmarshal into an int64 (rejects a fractional number
//     like 1.5, a numeric string like "60000", an object, or an array) or
//     unmarshals to a negative value.
//   - rawScope fails to unmarshal into a string, or the string is not
//     exactly "private" or "public" — the CacheableResult schema marks
//     cacheScope required, so a missing or invalid value means the server is
//     not actually speaking the caching contract, and this package fails
//     closed toward today's always-fetch behavior rather than guessing a
//     scope.
//
// This tolerant-decode discipline mirrors decodeResultType's recorded
// rationale (client.go) and parseServerInfo's (meta.go): a non-compliant
// value from an untrusted server must never fail the whole tools/list call,
// only disable the optimization that value would have enabled.
//
// When none of the above applies, the returned hint clamps the millisecond
// value to maxCacheHintTTLMs before converting to a time.Duration (see
// maxCacheHintTTL's doc for why downward clamping is spec-legal) and carries
// the parsed Scope through unchanged.
func parseCacheHint(modern bool, rawTTLMs, rawScope json.RawMessage) cacheHint {
	if !modern {
		return cacheHint{}
	}
	if len(rawTTLMs) == 0 || string(rawTTLMs) == "null" {
		return cacheHint{}
	}
	var ttlMs int64
	if err := json.Unmarshal(rawTTLMs, &ttlMs); err != nil || ttlMs < 0 {
		return cacheHint{}
	}
	var scope string
	if err := json.Unmarshal(rawScope, &scope); err != nil {
		return cacheHint{}
	}
	parsedScope := cacheScope(scope)
	if parsedScope != cacheScopePrivate && parsedScope != cacheScopePublic {
		return cacheHint{}
	}

	if ttlMs > maxCacheHintTTLMs {
		ttlMs = maxCacheHintTTLMs
	}
	return cacheHint{Present: true, TTL: time.Duration(ttlMs) * time.Millisecond, Scope: parsedScope}
}

// serverConfig is the comparable snapshot of exactly the four db.McpServer
// columns newClientForServer reads (registry.go): name, url, protocol
// version, and the encrypted auth headers. It is the cache's invalidation
// mechanism — a serverConfig read fresh from the DB on every resolve/refresh
// (via GetMCPServer) that no longer equals the cached entry's serverConfig
// means the cached *Client or tool catalog was built from stale
// configuration and must be rebuilt — chosen over explicit Invalidate(id)
// calls threaded through every mutating handler (Update, SetAuthHeader,
// DeleteAuthHeader) because mcp_servers has no updated_at or version column
// (verified: schemas/sql_schemas.sql), so there is no cheaper "did this row
// change" signal to key off, and an explicit-call scheme can silently rot
// the day a future mutation path forgets to call it. Comparable via plain
// `==`, not a SHA-256 fingerprint: it needs no new imports, is directly
// readable in a debugger, and cannot be misread as a security token.
//
//   - name is included because it is the Prometheus "server" label
//     (client.go), so a rename must invalidate a cached Client rather than
//     keep emitting metrics under the old label.
//   - protocol collapses NULL and "" to "" because newClientForServer treats
//     them identically (both mean "unpinned/legacy").
//   - authHeaders/hasAuth: NULL (no ciphertext stored) and an empty-string
//     ciphertext are kept distinct via the bool, so "absent" and "empty"
//     cannot collide into the same serverConfig by accident. authHeaders
//     holds the ciphertext ONLY for equality comparison — it is held in
//     memory and MUST NEVER be logged. AES-GCM re-encryption of the same
//     plaintext headers yields different ciphertext on every write, so an
//     operator rotating a header (even to byte-identical plaintext) always
//     produces a serverConfig MISS — a rebuild, never a stale reuse. That is
//     the safe direction for ADR-039's write-only-over-API contract: this
//     cache can be wrong in the direction of "rebuilds one extra time", never
//     in the direction of "serves traffic under a header an operator already
//     rotated away from".
type serverConfig struct {
	name        string
	url         string
	protocol    string
	authHeaders string
	hasAuth     bool
}

// serverConfigOf extracts srv's serverConfig. See serverConfig's doc for the
// NULL/"" collapsing rules applied here.
func serverConfigOf(srv db.McpServer) serverConfig {
	cfg := serverConfig{name: srv.Name, url: srv.Url}
	if srv.ProtocolVersion != nil {
		cfg.protocol = *srv.ProtocolVersion
	}
	if srv.AuthHeadersEncrypted != nil {
		cfg.hasAuth = true
		cfg.authHeaders = *srv.AuthHeadersEncrypted
	}
	return cfg
}

// cachedClient pairs a built *Client with the serverConfig it was built
// from, so a later resolve can detect a configuration change with a plain
// `==` rather than re-deriving whether anything relevant changed.
type cachedClient struct {
	cfg    serverConfig
	client *Client
}

// cachedToolList pairs a discovered tool catalog with the serverConfig it
// was discovered under and the instant it stops being servable. tools is
// treated as immutable once stored — every reader gets a fresh
// slices.Clone, never the stored slice itself, so a caller mutating its copy
// can never corrupt the cache (or another caller's copy).
type cachedToolList struct {
	cfg       serverConfig
	tools     []Tool
	expiresAt time.Time
}

// registryCache holds the Registry's per-server client and tool-catalog
// caches behind one mutex. One registryCache is shared by every goroutine
// using a given *Registry (main.go wires one Registry process-wide), so
// mu guards both maps against concurrent resolves/refreshes for different
// servers as well as the same one.
type registryCache struct {
	mu        sync.Mutex
	clients   map[string]cachedClient
	toolLists map[string]cachedToolList
}

func newRegistryCache() *registryCache {
	return &registryCache{
		clients:   make(map[string]cachedClient),
		toolLists: make(map[string]cachedToolList),
	}
}

// clientForServer returns a *Client for srv, reusing a previously built
// client when srv's current serverConfig still matches the one it was built
// from, and building (and caching) a fresh one otherwise. This is what lets
// ResolveToolByName stop paying a full legacy initialize +
// notifications/initialized handshake on every poll-check tick: the same
// *Client, with its already-negotiated session, is handed back on every call
// for as long as the server's config is unchanged.
//
// Safe to share across goroutines: *Client already guards its mutable state
// (sessionID, negotiatedVersion) with its own mutex, and authHeaders,
// protocolVersion, and httpClient are set once at construction and never
// mutated afterward — that is what makes cross-goroutine reuse legal here,
// not something this cache adds.
//
// There is nothing to close on a discarded build (a cache miss whose result
// loses the race below, or a rebuild that replaces a stale entry): NewClient
// (client.go) leaves http.Client.Transport nil, so every Client shares
// http.DefaultTransport's connection pool — a dropped Client releases no
// sockets, just one small struct.
//
// newClientForServer runs OUTSIDE r.cache.mu, deliberately. An earlier
// version of this method held the lock across the whole miss path on the
// reasoning that the AES decrypt inside newClientForServer is microseconds —
// true, but that reasoning missed dropReservedAuthHeaders's slog.Warn call
// per grandfathered reserved header (registry.go), which newClientForServer
// can also hit on every build. Holding the cache mutex across a call that
// can block on a slow or unavailable log sink would stall every MCP resolve
// process-wide, not just the one goroutine doing the rebuild. The build is
// therefore done unlocked, then re-checked under the lock before storing: if
// another goroutine already stored a matching entry while this one was
// building (only possible when multiple goroutines miss on the same server
// concurrently), this call discards its own build and returns the
// already-stored *Client instead of overwriting it, so every caller still
// observes one identity for a given serverConfig regardless of how many
// racing builds happened — TestRegistryCache_ConcurrentResolveIsRaceFree
// asserts this directly, not just "clean under -race".
//
// Deliberately NOT used by ResolveForPolicy, ProbeTools, ProbeProtocol, or
// refreshProtocolVersion:
//   - ResolveForPolicy already dedups clients per call (by server URL), and
//     sharing a client ACROSS runs is a materially wider behavioral change
//     than this issue asks for — a 401-driven resetSession triggered by one
//     run would become silently visible to a different, unrelated run.
//   - ProbeTools and ProbeProtocol build a client for a synthetic "<probe>"
//     server ID that has no real mcp_servers row, so there is nothing a
//     cache entry could be keyed against or invalidated by.
func (r *Registry) clientForServer(srv db.McpServer) *Client {
	cfg := serverConfigOf(srv)

	r.cache.mu.Lock()
	entry, ok := r.cache.clients[srv.ID]
	r.cache.mu.Unlock()
	if ok && entry.cfg == cfg {
		return entry.client
	}

	client := r.newClientForServer(srv)

	r.cache.mu.Lock()
	defer r.cache.mu.Unlock()
	if entry, ok := r.cache.clients[srv.ID]; ok && entry.cfg == cfg {
		// Lost the race: another goroutine already stored a client built from
		// the same serverConfig while we were building ours. Discard ours
		// (see the "nothing to close" note above) and hand back the one that
		// is now the single source of truth.
		return entry.client
	}
	r.cache.clients[srv.ID] = cachedClient{cfg: cfg, client: client}
	return client
}

// discoverToolsCached returns srv's tool catalog, serving a cached result
// when one exists for srv's current serverConfig and has not yet reached its
// expiresAt, and performing a full tools/list discovery otherwise. The
// second return value reports whether the result came from the cache —
// RefreshTools uses it to keep has_drift a real-fetch-only signal (see its
// doc for why) and to surface the fact on ToolDiff.ServedFromCache, which
// MCPHandler.Discover exposes to the operator as served_from_cache. A cache
// hit is also logged at slog.Info, not slog.Debug: today RefreshTools's only
// production caller is the operator's explicit Discover action, so a hit
// means that "check now" press made no network request — worth a visible
// trace at the default log level, not something that only shows up with
// debug logging turned on.
//
// Uses a THROWAWAY client from newClientForServer, deliberately NOT
// clientForServer, so RefreshTools's documented "probe client separate from
// discovery client" property (registry.go) is unaffected by this cache —
// this method only ever affects whether the tools/list ROUND TRIP happens,
// never which client makes it.
//
// On a discovery error, the cache is left completely untouched (no entry
// written, no existing entry evicted) and the error is returned as-is: a
// transient failure must never populate the cache with a lie, and must never
// evict an existing, still-fresh, good entry either.
//
// On success, a cache entry is stored only when the hint is Present with a
// strictly positive TTL AND Scope is not cacheScopePrivate; otherwise any
// existing entry for this server is dropped. The Present/TTL>0 half covers
// three cases identically: a legacy server (hint never Present), a modern
// server that stopped sending a hint on this call, and a modern server
// sending ttlMs: 0 ("immediately stale") — all three fall back to exactly
// today's always-fetch behavior rather than serving one further cache hit
// from a previous, no-longer-authoritative hint. The Scope check is a
// separate, deliberately conservative decision: Gleipnir's cache is
// per-server-row today, with no per-user or per-policy dimension, so a
// "private" catalog and a "public" one are indistinguishable in storage —
// correct only while ADR-039's per-user/per-policy credential-scoping
// deferral holds. ADR-053 plans to remove that deferral; failing closed on
// "private" now, before any server actually depends on the distinction,
// costs nothing (no server observed in the wild sends "private" today) and
// avoids a future world where this cache silently serves one user's private
// catalog to another. parseCacheHint deliberately still parses and reports
// "private" verbatim rather than rejecting it outright — see cacheScope's
// doc for why that split is deliberate.
//
// The returned []Tool (and each entry's InputSchema json.RawMessage) is
// treated as immutable while cached; canonicalizeDiscovered (canonical.go),
// the only consumer, only reads it. Memory bound: at most one catalog per
// modern hint-sending server, held for at most maxCacheHintTTL.
func (r *Registry) discoverToolsCached(ctx context.Context, srv db.McpServer) ([]Tool, bool, error) {
	cfg := serverConfigOf(srv)

	r.cache.mu.Lock()
	entry, ok := r.cache.toolLists[srv.ID]
	r.cache.mu.Unlock()
	if ok && entry.cfg == cfg && timeNow().Before(entry.expiresAt) {
		slog.Info("mcp tools/list cache hit: server requested caching via ttlMs/cacheScope, skipping the tools/list network round trip",
			"server_id", srv.ID, "server_name", srv.Name, "cache_expires_at", entry.expiresAt.Format(time.RFC3339))
		return slices.Clone(entry.tools), true, nil
	}

	fresh, hint, err := r.newClientForServer(srv).discoverToolsWithHint(ctx)
	if err != nil {
		return nil, false, err
	}

	r.cache.mu.Lock()
	defer r.cache.mu.Unlock()
	if hint.Present && hint.TTL > 0 && hint.Scope != cacheScopePrivate {
		r.cache.toolLists[srv.ID] = cachedToolList{
			cfg:       cfg,
			tools:     slices.Clone(fresh),
			expiresAt: timeNow().Add(hint.TTL),
		}
	} else {
		delete(r.cache.toolLists, srv.ID)
	}
	return fresh, false, nil
}

// ForgetServer drops any cached *Client and tool catalog held for serverID.
// MCPHandler.Delete calls this immediately after deleting the mcp_servers
// row, so a resolve or refresh for serverID that happens AFTER this call
// starts from a clean slate instead of reusing whatever was built before the
// delete.
//
// This is NOT a hard guarantee that no entry for serverID exists afterward:
// DeleteMCPServer and this call are two separate operations, not one atomic
// transaction. A ResolveToolByName call already in flight can have read its
// db.McpServer snapshot before the delete committed and then call
// clientForServer with that now-stale snapshot after ForgetServer has
// already run, re-installing an entry for a server ID that no longer has a
// row. That residue is harmless, not merely tolerated: mcp_tools rows
// cascade-delete with their mcp_servers row (schemas/sql_schemas.sql), so
// every subsequent ResolveToolByName call for that dot-name fails its
// GetMCPToolByServerAndName lookup before it ever reaches clientForServer
// again — nothing can hand the re-installed entry's *Client out a second
// time. A *Client for a server deleted through some OTHER path entirely
// (e.g. a direct DB delete outside the API, which never calls ForgetServer
// at all) lingers the same way until restart — see clientForServer's doc for
// why that residue is bounded and harmless.
func (r *Registry) ForgetServer(serverID string) {
	r.cache.mu.Lock()
	defer r.cache.mu.Unlock()
	delete(r.cache.clients, serverID)
	delete(r.cache.toolLists, serverID)
}
