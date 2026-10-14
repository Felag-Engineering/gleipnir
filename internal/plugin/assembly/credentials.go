package assembly

import (
	"context"
	"log/slog"
	"time"

	"github.com/felag-engineering/gleipnir/internal/admin"
	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/infra/crypto"
	pluginoauth "github.com/felag-engineering/gleipnir/internal/plugin/oauth"
)

// credentialParts is the plugin credential and OAuth surface. It is the same
// code the v1 substrate wires in pluginruntime.go: those handlers read the
// manifest through internal/plugin/manifest.Read, so they serve a v2 manifest
// as well as a v1 one, and nothing in them touches a subprocess or a gRPC conn.
type credentialParts struct {
	store   *pluginoauth.DBStore
	nonces  *pluginoauth.DBNonceStore
	scanner *pluginoauth.RefreshScanner

	oauth              *admin.PluginOAuthHandler
	credentials        *admin.PluginCredentialsHandler
	onPublicURLChanged func(ctx context.Context, oldURL, newURL string)
}

// newCredentialParts builds the credential surface. It returns nil when there
// is no encryption key: credentials are stored encrypted, so without a key
// there is nothing safe to build (v1 behaves the same way).
func newCredentialParts(deps Deps, q *db.Queries) *credentialParts {
	key := deps.EncryptionKey
	if len(key) == 0 {
		return nil
	}

	encrypt := func(p string) (string, error) { return crypto.Encrypt(key, p) }
	decrypt := func(c string) (string, error) { return crypto.Decrypt(key, c) }
	clock := func() time.Time { return timeNow() }
	getPublicURL := func() string {
		if deps.SystemSettings == nil {
			return ""
		}
		u, _ := deps.SystemSettings.GetPublicURL(context.Background())
		return u
	}

	store := pluginoauth.NewDBStore(q, encrypt, decrypt, q, clock)
	nonces := pluginoauth.NewDBNonceStore(q, clock)
	manager := pluginoauth.NewManager(store, nonces, clock, pluginoauth.DeriveHMACKey(key), getPublicURL)
	rescanner := pluginoauth.NewCallbackRescanner(q, q, getPublicURL, clock)

	return &credentialParts{
		store:   store,
		nonces:  nonces,
		scanner: pluginoauth.NewRefreshScanner(store, q, getPublicURL, deps.Config.OAuthRefreshInterval, deps.Config.OAuthRefreshLead),

		oauth:       admin.NewPluginOAuthHandler(q, manager, getPublicURL),
		credentials: admin.NewPluginCredentialsHandler(q, store, deps.Publisher),
		onPublicURLChanged: func(ctx context.Context, _, _ string) {
			if _, err := rescanner.Scan(ctx); err != nil {
				slog.ErrorContext(ctx, "callback rescan failed after public_url change", "err", err)
			}
		},
	}
}
