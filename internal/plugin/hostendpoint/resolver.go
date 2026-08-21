package hostendpoint

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/plugin/identity"
	"github.com/felag-engineering/gleipnir/internal/plugin/reconciler"
)

// Two TokenResolver implementations, one per substrate era. The middleware
// does not care which is behind it — that is what lets the host endpoint
// come up against the v1.1 registry today and switch to generation rows when
// the reconciler goes live, without the auth semantics moving.

// RegistryResolver authenticates against the v1.1 in-memory token registry —
// the same identity.Registry hostsvc's gRPC interceptor consults, so a
// subprocess-era plugin's GLEIPNIR_INSTANCE_TOKEN authenticates identically
// on either transport. The registry is per-instance (Issue auto-revokes the
// prior token), so Generation is 0: the concept does not exist there.
type RegistryResolver struct {
	Registry *identity.Registry
}

func (r RegistryResolver) ResolveToken(_ context.Context, token string) (Identity, bool, error) {
	instanceID, ok := r.Registry.Lookup(token)
	if !ok {
		return Identity{}, false, nil
	}
	return Identity{InstanceID: instanceID}, true, nil
}

// GenerationTokenQuerier is the slice of the sqlc surface the DB-backed
// resolver needs. *db.Queries satisfies it.
type GenerationTokenQuerier interface {
	GetContainerGenerationByTokenHash(ctx context.Context, tokenHash string) (db.PluginContainerGeneration, error)
}

// GenerationTokenResolver authenticates against the substrate's
// per-generation token rows: hash the presented token exactly the way
// rotation stored it (reconciler.HashInstanceToken — two implementations of
// "the stored form" is one more than the number that can be right) and look
// the hash up. The query itself excludes revoked tokens, so a retired
// generation fails authentication with no revocation check to forget here —
// and a DRAINING generation still authenticates, deliberately: tokens are
// revoked at retire, not at switch, because revoking during drain would fail
// the in-flight work draining exists to protect (#813).
type GenerationTokenResolver struct {
	Querier GenerationTokenQuerier
}

func (r GenerationTokenResolver) ResolveToken(ctx context.Context, token string) (Identity, bool, error) {
	row, err := r.Querier.GetContainerGenerationByTokenHash(ctx, reconciler.HashInstanceToken(token))
	if errors.Is(err, sql.ErrNoRows) {
		return Identity{}, false, nil
	}
	if err != nil {
		return Identity{}, false, fmt.Errorf("hostendpoint: token lookup: %w", err)
	}
	return Identity{InstanceID: row.PluginInstanceID, Generation: row.Generation}, true, nil
}
