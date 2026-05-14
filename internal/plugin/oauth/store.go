package oauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"golang.org/x/oauth2"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/model"
	pluginstate "github.com/felag-engineering/gleipnir/internal/plugin/state"
)

// Audit event types emitted by the store (spec §9.4).
const (
	auditOAuthIssued        = "plugin_oauth_issued"
	auditOAuthRefreshed     = "plugin_oauth_refreshed"
	auditOAuthRefreshFailed = "plugin_oauth_refresh_failed"
	auditOAuthRevoked       = "plugin_oauth_revoked"
)

// EncryptFunc encrypts a plaintext string to a base64-encoded ciphertext blob.
// The concrete implementation is admin.Encrypt bound to the host's
// GLEIPNIR_ENCRYPTION_KEY — passed as a function to avoid importing
// internal/admin (which imports internal/plugin/oauth, causing a cycle).
type EncryptFunc func(plaintext string) (string, error)

// DecryptFunc decrypts a base64-encoded ciphertext blob produced by EncryptFunc.
// The concrete implementation is admin.Decrypt bound to the same key.
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
		if creds.Token != nil && tok.Expiry.Before(creds.Token.Expiry) {
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
	detail := "oauth refresh failed"
	if cause != nil {
		detail = fmt.Sprintf("oauth refresh failed: %s", cause)
	}

	s.emitAudit(ctx, auditOAuthRefreshFailed, instanceID, map[string]any{
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
	s.emitAudit(ctx, auditOAuthIssued, instanceID, nil)
}

// EmitRefreshed writes a plugin_oauth_refreshed audit event. Called by
// persistingTokenSource after a successful refresh writes a new token via
// SaveToken. The initial token exchange (authcode/clientcred) uses EmitIssued
// instead so the audit log distinguishes "first grant" from "later refresh".
func (s *DBStore) EmitRefreshed(ctx context.Context, instanceID string) {
	s.emitAudit(ctx, auditOAuthRefreshed, instanceID, nil)
}

// EmitRevoked writes a plugin_oauth_revoked audit event. Intended for future
// revocation flows; not yet called from any production path in #224.
func (s *DBStore) EmitRevoked(ctx context.Context, instanceID string) {
	s.emitAudit(ctx, auditOAuthRevoked, instanceID, nil)
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

// emitAudit inserts a plugin audit event row. Failures are logged but not
// surfaced to the caller — the upstream operation (token save, health set) has
// already committed and the audit event is informational.
func (s *DBStore) emitAudit(ctx context.Context, eventType, instanceID string, extra map[string]any) {
	payload := map[string]any{"instance_id": instanceID}
	for k, v := range extra {
		payload[k] = v
	}
	body, _ := json.Marshal(payload)
	now := s.clock().UTC().Format(time.RFC3339Nano)
	_, err := s.q.InsertPluginAuditEvent(ctx, db.InsertPluginAuditEventParams{
		PluginInstanceID: &instanceID,
		EventType:        eventType,
		Severity:         "info",
		ActorUserID:      nil,
		PayloadJson:      string(body),
		CreatedAt:        now,
	})
	if err != nil {
		slog.ErrorContext(ctx, "oauth: emit audit event failed", "event_type", eventType, "instance_id", instanceID, "err", err)
	}
}
