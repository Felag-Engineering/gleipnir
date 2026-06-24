package oauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"golang.org/x/oauth2"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/infra/headervalidate"
	"github.com/felag-engineering/gleipnir/internal/model"
	pluginstate "github.com/felag-engineering/gleipnir/internal/plugin/state"
	sdkmanifest "github.com/felag-engineering/gleipnir/plugin-sdk/manifest"
)

// Audit event types emitted by the store (spec §9.4).
const (
	auditOAuthIssued        = "plugin_oauth_issued"
	auditOAuthRefreshed     = "plugin_oauth_refreshed"
	auditOAuthRefreshFailed = "plugin_oauth_refresh_failed"
	auditOAuthRevoked       = "plugin_oauth_revoked"

	auditCredentialSet     = "plugin_credentials_set"
	auditCredentialDeleted = "plugin_credentials_deleted"
	auditCredentialCleared = "plugin_credentials_cleared"
)

// RefreshFailureDetailPrefix is the stable prefix used in the health-state
// detail field when MarkRefreshFailed drives an instance to unhealthy.
// The admin UI matches on this prefix to decide whether to show the
// "Re-authorize" button — pinning it as a constant makes the contract explicit.
const RefreshFailureDetailPrefix = "oauth refresh failed"

// ErrWrongStrategy is returned by Set* methods when the stored credential
// strategy does not match the operation — e.g. calling SetStaticAPIKey on an
// instance whose manifest declares header_set.
var ErrWrongStrategy = errors.New("oauth store: instance strategy does not match operation")

// EncryptFunc encrypts a plaintext string to a base64-encoded ciphertext blob.
// The concrete implementation is crypto.Encrypt bound to the host's
// GLEIPNIR_ENCRYPTION_KEY — passed as a function to avoid importing
// internal/admin (which imports internal/plugin/oauth, causing a cycle).
type EncryptFunc func(plaintext string) (string, error)

// DecryptFunc decrypts a base64-encoded ciphertext blob produced by EncryptFunc.
// The concrete implementation is crypto.Decrypt bound to the same key.
type DecryptFunc func(encoded string) (string, error)

// OAuthQuerier is the narrow DB interface required by DBStore and Manager.
// Using an interface keeps both testable with a fake querier.
type OAuthQuerier interface {
	GetPluginInstanceByID(ctx context.Context, id string) (db.PluginInstance, error)
	UpdatePluginInstanceCredentials(ctx context.Context, arg db.UpdatePluginInstanceCredentialsParams) (int64, error)
	UpdatePluginInstanceHealth(ctx context.Context, arg db.UpdatePluginInstanceHealthParams) (int64, error)
	InsertPluginAuditEvent(ctx context.Context, arg db.InsertPluginAuditEventParams) (db.PluginAuditEvent, error)
	ListPluginInstancesWithExpiringCredentials(ctx context.Context, cutoff *string) ([]db.PluginInstance, error)
	// UpdatePluginInstanceOAuthCallback records the last callback URL used in a
	// BeginAuthcode call so operators can detect when public_url changes mid-dance.
	UpdatePluginInstanceOAuthCallback(ctx context.Context, arg db.UpdatePluginInstanceOAuthCallbackParams) (int64, error)
}

// DBStore persists OAuth2 credentials into plugin_instances.credentials_encrypted
// using the host's AES-256-GCM encryption key. It emits audit events and drives
// the plugin health state machine on refresh failure.
type DBStore struct {
	q       OAuthQuerier
	encrypt EncryptFunc
	decrypt DecryptFunc
	health  pluginstate.Querier // narrow; only GetPluginInstanceByID + UpdatePluginInstanceHealth
	locks   instanceLocks
	clock   func() time.Time
}

// NewDBStore constructs a DBStore. encrypt and decrypt are closures over the
// host's encryption key (see EncryptFunc / DecryptFunc). setHealth is the
// narrow Querier used by pluginstate.SetHealthState; it is separate from q
// because pluginstate.Querier and OAuthQuerier share some methods but differ
// in scope.
//
// clock is injectable for tests (pass time.Now in production).
func NewDBStore(q OAuthQuerier, enc EncryptFunc, dec DecryptFunc, health pluginstate.Querier, clock func() time.Time) *DBStore {
	if clock == nil {
		clock = time.Now
	}
	return &DBStore{q: q, encrypt: enc, decrypt: dec, health: health, clock: clock}
}

// LoadCredentials reads and decrypts the stored credentials for instanceID.
// Returns the credentials, the current CAS version, and any error.
func (s *DBStore) LoadCredentials(ctx context.Context, instanceID string) (StoredCredentials, int64, error) {
	row, err := s.q.GetPluginInstanceByID(ctx, instanceID)
	if err != nil {
		return StoredCredentials{}, 0, fmt.Errorf("load credentials: get instance: %w", err)
	}
	if row.CredentialsEncrypted == nil || *row.CredentialsEncrypted == "" {
		return StoredCredentials{}, row.Version, nil
	}
	plaintext, err := s.decrypt(*row.CredentialsEncrypted)
	if err != nil {
		return StoredCredentials{}, 0, fmt.Errorf("load credentials: decrypt: %w", err)
	}
	creds, err := UnmarshalCredentials(plaintext)
	if err != nil {
		return StoredCredentials{}, 0, fmt.Errorf("load credentials: %w", err)
	}
	return creds, row.Version, nil
}

// SaveCredentials encrypts and persists creds for instanceID using a CAS guard
// on expectedVersion. Returns an error if the write fails or a concurrent writer
// raced ahead.
func (s *DBStore) SaveCredentials(ctx context.Context, instanceID string, creds StoredCredentials, expectedVersion int64) error {
	plaintext, err := creds.Marshal()
	if err != nil {
		return fmt.Errorf("save credentials: %w", err)
	}
	ciphertext, err := s.encrypt(plaintext)
	if err != nil {
		return fmt.Errorf("save credentials: encrypt: %w", err)
	}

	expiresAt := credentialsExpiresAt(creds)

	now := s.clock().UTC().Format(time.RFC3339Nano)
	rows, err := s.q.UpdatePluginInstanceCredentials(ctx, db.UpdatePluginInstanceCredentialsParams{
		CredentialsEncrypted: &ciphertext,
		CredentialsExpiresAt: expiresAt,
		UpdatedAt:            now,
		ID:                   instanceID,
		ExpectedVersion:      expectedVersion,
	})
	if err != nil {
		return fmt.Errorf("save credentials: update: %w", err)
	}
	if rows == 0 {
		return ErrCASConflict
	}
	return nil
}

// ErrCASConflict is returned by SaveToken and SaveCredentials when the CAS
// guard detects a concurrent writer advanced the row version.
var ErrCASConflict = errors.New("oauth store: CAS conflict")

// SaveToken updates only the token field inside the stored credentials.
// It retries the load→modify→save cycle up to 3 times on CAS conflict. If a
// concurrent writer already stored a fresher token (later Expiry), the write is
// skipped — the other refresher won, which is correct. Returns an error only
// after 3 failed attempts, so the scanner can log and retry next tick.
func (s *DBStore) SaveToken(ctx context.Context, instanceID string, tok *oauth2.Token) error {
	const maxAttempts = 3

	mu := s.locks.Get(instanceID)
	mu.Lock()
	defer mu.Unlock()

	for attempt := 0; attempt < maxAttempts; attempt++ {
		creds, ver, err := s.LoadCredentials(ctx, instanceID)
		if err != nil {
			return fmt.Errorf("save token (attempt %d): %w", attempt+1, err)
		}

		// Skip if the reloaded token is already strictly fresher than ours.
		// A concurrent refresher already wrote a better token; accept that.
		// Only apply the check when tok has a real expiry — a zero Expiry means
		// the token is non-expiring and must always be persisted regardless of
		// what's currently stored.
		if !tok.Expiry.IsZero() && creds.Token != nil && tok.Expiry.Before(creds.Token.Expiry) {
			return nil
		}

		creds.Token = tok
		err = s.SaveCredentials(ctx, instanceID, creds, ver)
		if err == nil {
			// Caller decides which audit event applies (issued vs. refreshed)
			// — see Manager.HandleCallback (EmitIssued) and persistingTokenSource
			// (EmitRefreshed). Emitting from here would double-log the initial
			// authcode exchange.
			return nil
		}
		if !errors.Is(err, ErrCASConflict) {
			return fmt.Errorf("save token: %w", err)
		}
		// CAS conflict — reload and retry.
	}
	return fmt.Errorf("save token: failed after %d CAS conflicts", maxAttempts)
}

// MarkRefreshFailed records a plugin_oauth_refresh_failed audit event and
// drives the instance to PluginHealthStateUnhealthy so the admin "Re-authorize"
// UI surface (#228) can pick it up.
func (s *DBStore) MarkRefreshFailed(ctx context.Context, instanceID string, cause error) error {
	detail := RefreshFailureDetailPrefix
	if cause != nil {
		detail = fmt.Sprintf("%s: %s", RefreshFailureDetailPrefix, cause)
	}

	s.emitAudit(ctx, auditOAuthRefreshFailed, "warning", instanceID, map[string]any{
		"error": detail,
	})

	// Drive instance to unhealthy. SetHealthState enforces the state-machine
	// graph; ErrIllegalTransition is silently ignored (e.g. already crashed).
	err := pluginstate.SetHealthState(
		ctx, s.health, nil, instanceID,
		pluginstate.OriginHost,
		model.PluginHealthStateUnhealthy,
		detail,
	)
	if err != nil && !errors.Is(err, pluginstate.ErrIllegalTransition) && !errors.Is(err, pluginstate.ErrTransitionConflict) {
		slog.ErrorContext(ctx, "oauth: set unhealthy failed", "instance_id", instanceID, "err", err)
	}
	return nil
}

// EmitIssued writes a plugin_oauth_issued audit event. Called by Manager after
// a successful token exchange (authcode) or initial clientcred grant.
func (s *DBStore) EmitIssued(ctx context.Context, instanceID string) {
	s.emitAudit(ctx, auditOAuthIssued, "info", instanceID, nil)
}

// EmitRefreshed writes a plugin_oauth_refreshed audit event. Called by
// persistingTokenSource after a successful refresh writes a new token via
// SaveToken. The initial token exchange (authcode/clientcred) uses EmitIssued
// instead so the audit log distinguishes "first grant" from "later refresh".
func (s *DBStore) EmitRefreshed(ctx context.Context, instanceID string) {
	s.emitAudit(ctx, auditOAuthRefreshed, "info", instanceID, nil)
}

// EmitRevoked writes a plugin_oauth_revoked audit event. Intended for future
// revocation flows; not yet called from any production path in #224.
func (s *DBStore) EmitRevoked(ctx context.Context, instanceID string) {
	s.emitAudit(ctx, auditOAuthRevoked, "info", instanceID, nil)
}

// credentialsExpiresAt extracts the token expiry from StoredCredentials and
// returns a pointer to an RFC3339Nano string for the DB column, or nil when the
// token has no expiry (e.g. non-expiring static tokens).
func credentialsExpiresAt(creds StoredCredentials) *string {
	if creds.Token == nil || creds.Token.Expiry.IsZero() {
		return nil
	}
	s := creds.Token.Expiry.UTC().Format(time.RFC3339Nano)
	return &s
}

// SeedOAuthToken seeds an OAuth access/refresh token directly into the stored
// credentials, initialising the Strategy from the manifest when the row is
// brand-new (credentials_encrypted is NULL). It is the escape hatch for admin
// seeding and E2E tests; the canonical happy path remains the authcode UI flow.
//
// Behaviour:
//   - If the stored credentials have no Strategy yet, Strategy is set to the
//     supplied value before writing the token.
//   - If a Strategy is already stored and it does not match, ErrWrongStrategy
//     is returned immediately.
//   - If a Strategy is already stored and matches, the Token is overwritten
//     (operator intent overrides any stale token — unlike SaveToken's
//     "skip if fresher" optimisation).
//
// The per-instance mutex is held for the duration of the CAS loop so this
// write is serialised against the OAuth refresh scanner.
func (s *DBStore) SeedOAuthToken(ctx context.Context, instanceID, strategy string, tok *oauth2.Token) error {
	mu := s.locks.Get(instanceID)
	mu.Lock()
	defer mu.Unlock()

	const maxAttempts = 3
	for attempt := 0; attempt < maxAttempts; attempt++ {
		creds, ver, err := s.LoadCredentials(ctx, instanceID)
		if err != nil {
			return fmt.Errorf("seed oauth token (attempt %d): %w", attempt+1, err)
		}
		if creds.Strategy == "" {
			// Fresh row — initialise strategy from the manifest.
			creds.Strategy = strategy
		} else if creds.Strategy != strategy {
			return ErrWrongStrategy
		}
		creds.Token = tok
		err = s.SaveCredentials(ctx, instanceID, creds, ver)
		if err == nil {
			s.emitAudit(ctx, auditCredentialSet, "info", instanceID, map[string]any{
				"strategy": strategy,
				"action":   "set_oauth_token",
			})
			return nil
		}
		if !errors.Is(err, ErrCASConflict) {
			return fmt.Errorf("seed oauth token: %w", err)
		}
		// CAS conflict — reload and retry.
	}
	return fmt.Errorf("seed oauth token: failed after %d CAS conflicts", maxAttempts)
}

// SetOAuthClient stores the OAuth2 client_id and client_secret for instanceID,
// initialising the Strategy from the manifest when the row is brand-new
// (credentials_encrypted is NULL). It is the entry point for the admin UI's
// OAuth setup form — the operator pastes the values copied from the provider
// (Slack app, etc.) before clicking "Authorize".
//
// Behaviour:
//   - If the stored credentials have no Strategy yet, Strategy is set to the
//     supplied value before writing the client credentials.
//   - If a Strategy is already stored and it does not match, ErrWrongStrategy
//     is returned immediately.
//   - Existing Token, AuthorizationURL, TokenURL, and Scopes are preserved.
//
// The per-instance mutex is held for the duration of the CAS loop so this
// write is serialised against the OAuth refresh scanner.
func (s *DBStore) SetOAuthClient(ctx context.Context, instanceID, strategy, clientID, clientSecret string) error {
	if clientID == "" {
		return fmt.Errorf("set oauth client: client_id must not be empty")
	}
	if clientSecret == "" {
		return fmt.Errorf("set oauth client: client_secret must not be empty")
	}

	mu := s.locks.Get(instanceID)
	mu.Lock()
	defer mu.Unlock()

	const maxAttempts = 3
	for attempt := 0; attempt < maxAttempts; attempt++ {
		creds, ver, err := s.LoadCredentials(ctx, instanceID)
		if err != nil {
			return fmt.Errorf("set oauth client (attempt %d): %w", attempt+1, err)
		}
		if creds.Strategy == "" {
			creds.Strategy = strategy
		} else if creds.Strategy != strategy {
			return ErrWrongStrategy
		}
		creds.ClientID = clientID
		creds.ClientSecret = clientSecret
		err = s.SaveCredentials(ctx, instanceID, creds, ver)
		if err == nil {
			s.emitAudit(ctx, auditCredentialSet, "info", instanceID, map[string]any{
				"strategy": strategy,
				"action":   "set_oauth_client",
			})
			return nil
		}
		if !errors.Is(err, ErrCASConflict) {
			return fmt.Errorf("set oauth client: %w", err)
		}
	}
	return fmt.Errorf("set oauth client: failed after %d CAS conflicts", maxAttempts)
}

// SetStaticAPIKey overwrites the static_api_key credentials for instanceID.
// It rejects the call when the stored strategy is not AuthStrategyStaticAPIKey.
// The per-instance mutex is held for the duration of the CAS loop so this
// write is serialised against SaveToken (OAuth refresh scanner).
func (s *DBStore) SetStaticAPIKey(ctx context.Context, instanceID, headerName, scheme, apiKey string) error {
	mu := s.locks.Get(instanceID)
	mu.Lock()
	defer mu.Unlock()

	const maxAttempts = 3
	for attempt := 0; attempt < maxAttempts; attempt++ {
		creds, ver, err := s.LoadCredentials(ctx, instanceID)
		if err != nil {
			return fmt.Errorf("set static api key (attempt %d): %w", attempt+1, err)
		}
		if creds.Strategy == "" {
			// First credential write for a freshly-created instance, whose stored
			// blob has no strategy yet (instance creation seeds credentials_encrypted
			// as NULL). Seed it here. The HTTP handler has already validated this
			// operation against the manifest's declared strategy
			// (requireOneOfStrategies), so seeding is safe. Mirrors SetOAuthClient /
			// SeedOAuthToken, which non-OAuth strategies previously lacked (#572).
			creds.Strategy = sdkmanifest.AuthStrategyStaticAPIKey
		} else if creds.Strategy != sdkmanifest.AuthStrategyStaticAPIKey {
			return ErrWrongStrategy
		}
		creds.StaticAPIKey = &StaticAPIKeyCreds{
			HeaderName: headerName,
			Scheme:     scheme,
			APIKey:     apiKey,
		}
		err = s.SaveCredentials(ctx, instanceID, creds, ver)
		if err == nil {
			s.emitAudit(ctx, auditCredentialSet, "info", instanceID, map[string]any{
				"strategy": creds.Strategy,
				"action":   "set_static_api_key",
				"key":      headerName,
			})
			return nil
		}
		if !errors.Is(err, ErrCASConflict) {
			return fmt.Errorf("set static api key: %w", err)
		}
	}
	return fmt.Errorf("set static api key: failed after %d CAS conflicts", maxAttempts)
}

// SetHeaderSetEntry adds or replaces a named header within the header_set
// credentials for instanceID. The replacement is case-insensitive on name,
// mirroring the MCPHandler.SetAuthHeader pattern. The header name is validated
// against RFC 7230 token syntax and the reserved-header list before any write.
// The per-instance mutex is held for the duration of the CAS loop.
func (s *DBStore) SetHeaderSetEntry(ctx context.Context, instanceID string, header NamedHeader) error {
	if err := headervalidate.ValidateName(header.Name); err != nil {
		return fmt.Errorf("set header set entry: %w", err)
	}

	mu := s.locks.Get(instanceID)
	mu.Lock()
	defer mu.Unlock()

	const maxAttempts = 3
	for attempt := 0; attempt < maxAttempts; attempt++ {
		creds, ver, err := s.LoadCredentials(ctx, instanceID)
		if err != nil {
			return fmt.Errorf("set header set entry (attempt %d): %w", attempt+1, err)
		}
		if creds.Strategy == "" {
			// Seed the strategy on first write for a fresh instance (#572). The
			// handler already validated it against the manifest. See SetStaticAPIKey.
			creds.Strategy = sdkmanifest.AuthStrategyHeaderSet
		} else if creds.Strategy != sdkmanifest.AuthStrategyHeaderSet {
			return ErrWrongStrategy
		}
		if creds.HeaderSet == nil {
			creds.HeaderSet = &HeaderSetCreds{}
		}
		// Replace existing entry by case-insensitive name, or append.
		replaced := false
		for i, h := range creds.HeaderSet.Headers {
			if strings.EqualFold(h.Name, header.Name) {
				creds.HeaderSet.Headers[i] = header
				replaced = true
				break
			}
		}
		if !replaced {
			creds.HeaderSet.Headers = append(creds.HeaderSet.Headers, header)
		}
		err = s.SaveCredentials(ctx, instanceID, creds, ver)
		if err == nil {
			s.emitAudit(ctx, auditCredentialSet, "info", instanceID, map[string]any{
				"strategy": creds.Strategy,
				"action":   "set_header",
				"key":      header.Name,
			})
			return nil
		}
		if !errors.Is(err, ErrCASConflict) {
			return fmt.Errorf("set header set entry: %w", err)
		}
	}
	return fmt.Errorf("set header set entry: failed after %d CAS conflicts", maxAttempts)
}

// DeleteHeaderSetEntry removes the named header from the header_set credentials.
// The match is case-insensitive. Idempotent: if the header is absent the call
// succeeds silently. If the last header is removed the Headers slice is left
// as an empty (non-nil) slice so the JSON shape is stable.
// The per-instance mutex is held for the duration of the CAS loop.
func (s *DBStore) DeleteHeaderSetEntry(ctx context.Context, instanceID, headerName string) error {
	mu := s.locks.Get(instanceID)
	mu.Lock()
	defer mu.Unlock()

	const maxAttempts = 3
	for attempt := 0; attempt < maxAttempts; attempt++ {
		creds, ver, err := s.LoadCredentials(ctx, instanceID)
		if err != nil {
			return fmt.Errorf("delete header set entry (attempt %d): %w", attempt+1, err)
		}
		if creds.Strategy != sdkmanifest.AuthStrategyHeaderSet {
			return ErrWrongStrategy
		}
		if creds.HeaderSet == nil {
			creds.HeaderSet = &HeaderSetCreds{Headers: []NamedHeader{}}
		}
		// Filter out matching header; preserve order. Headers[:0] is always
		// non-nil so JSON marshals as [] not null even when all entries removed.
		kept := creds.HeaderSet.Headers[:0]
		for _, h := range creds.HeaderSet.Headers {
			if !strings.EqualFold(h.Name, headerName) {
				kept = append(kept, h)
			}
		}
		creds.HeaderSet.Headers = kept
		err = s.SaveCredentials(ctx, instanceID, creds, ver)
		if err == nil {
			s.emitAudit(ctx, auditCredentialDeleted, "info", instanceID, map[string]any{
				"strategy": creds.Strategy,
				"key":      headerName,
			})
			return nil
		}
		if !errors.Is(err, ErrCASConflict) {
			return fmt.Errorf("delete header set entry: %w", err)
		}
	}
	return fmt.Errorf("delete header set entry: failed after %d CAS conflicts", maxAttempts)
}

// SetBasicAuth overwrites the basic_auth credentials for instanceID.
// It rejects the call when the stored strategy is not AuthStrategyBasicAuth.
// The per-instance mutex is held for the duration of the CAS loop.
func (s *DBStore) SetBasicAuth(ctx context.Context, instanceID, username, password string) error {
	mu := s.locks.Get(instanceID)
	mu.Lock()
	defer mu.Unlock()

	const maxAttempts = 3
	for attempt := 0; attempt < maxAttempts; attempt++ {
		creds, ver, err := s.LoadCredentials(ctx, instanceID)
		if err != nil {
			return fmt.Errorf("set basic auth (attempt %d): %w", attempt+1, err)
		}
		if creds.Strategy == "" {
			// Seed the strategy on first write for a fresh instance (#572). The
			// handler already validated it against the manifest. See SetStaticAPIKey.
			creds.Strategy = sdkmanifest.AuthStrategyBasicAuth
		} else if creds.Strategy != sdkmanifest.AuthStrategyBasicAuth {
			return ErrWrongStrategy
		}
		creds.BasicAuth = &BasicAuthCreds{Username: username, Password: password}
		err = s.SaveCredentials(ctx, instanceID, creds, ver)
		if err == nil {
			s.emitAudit(ctx, auditCredentialSet, "info", instanceID, map[string]any{
				"strategy": creds.Strategy,
				"action":   "set_basic_auth",
			})
			return nil
		}
		if !errors.Is(err, ErrCASConflict) {
			return fmt.Errorf("set basic auth: %w", err)
		}
	}
	return fmt.Errorf("set basic auth: failed after %d CAS conflicts", maxAttempts)
}

// ClearCredentials wipes the secret sub-blob for instanceID while preserving
// the Strategy field. Emits a plugin_credentials_cleared audit event.
// The per-instance mutex is held for the duration of the CAS loop.
func (s *DBStore) ClearCredentials(ctx context.Context, instanceID string) error {
	mu := s.locks.Get(instanceID)
	mu.Lock()
	defer mu.Unlock()

	const maxAttempts = 3
	for attempt := 0; attempt < maxAttempts; attempt++ {
		creds, ver, err := s.LoadCredentials(ctx, instanceID)
		if err != nil {
			return fmt.Errorf("clear credentials (attempt %d): %w", attempt+1, err)
		}
		strategy := creds.Strategy
		cleared := StoredCredentials{Strategy: strategy}
		err = s.SaveCredentials(ctx, instanceID, cleared, ver)
		if err == nil {
			s.emitAudit(ctx, auditCredentialCleared, "info", instanceID, map[string]any{
				"strategy": strategy,
			})
			return nil
		}
		if !errors.Is(err, ErrCASConflict) {
			return fmt.Errorf("clear credentials: %w", err)
		}
	}
	return fmt.Errorf("clear credentials: failed after %d CAS conflicts", maxAttempts)
}

// emitAudit inserts a plugin audit event row. Failures are logged but not
// surfaced to the caller — the upstream operation (token save, health set) has
// already committed and the audit event is informational.
// severity must be one of "info", "warning", or "error".
func (s *DBStore) emitAudit(ctx context.Context, eventType, severity, instanceID string, extra map[string]any) {
	payload := map[string]any{"instance_id": instanceID}
	for k, v := range extra {
		payload[k] = v
	}
	body, _ := json.Marshal(payload)
	now := s.clock().UTC().Format(time.RFC3339Nano)
	_, err := s.q.InsertPluginAuditEvent(ctx, db.InsertPluginAuditEventParams{
		PluginInstanceID: &instanceID,
		EventType:        eventType,
		Severity:         severity,
		ActorUserID:      nil,
		PayloadJson:      string(body),
		CreatedAt:        now,
	})
	if err != nil {
		slog.ErrorContext(ctx, "oauth: emit audit event failed", "event_type", eventType, "instance_id", instanceID, "err", err)
	}
}
