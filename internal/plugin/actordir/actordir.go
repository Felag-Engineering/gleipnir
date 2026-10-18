// Package actordir resolves a channel's externally-asserted actor id to the
// Gleipnir user an admin linked it to, on one plugin instance.
//
// It is a leaf (internal/db and internal/model only) so both consumers can
// import it without a cycle: internal/plugin/hostendpoint, whose
// host/authorize_actor pre-checks a click before a channel settles a request,
// and internal/execution/run, whose task channel adapter verifies the actor a
// settled task names before stamping it on a decision.
//
// ADR-058: only a verified or admin-set identity link may feed actor
// authorization. A plugin's own claim about who clicked is not evidence on its
// own. Today the only source is plugin_user_identities with link_method
// 'admin_set', a mapping an admin typed in. The verified link methods of spec
// §9.1 fold in behind the same Resolve, keyed off verified_at, without either
// consumer changing.
//
// Resolution is per instance, not global. Two instances of one channel plugin
// can serve different workspaces whose actor ids overlap, so an actor id only
// names a person together with the instance that issued it. A mapping made on
// instance A authorizes nothing on instance B.
package actordir

import (
	"context"
	"fmt"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/model"
)

// Resolution is what a Directory found for one actor id: the single Gleipnir
// user it is mapped to and every role that user holds.
type Resolution struct {
	UserID   string
	Username string
	Roles    []model.Role
}

// Directory resolves an actor id asserted by a channel instance.
type Directory interface {
	// Resolve looks up actorExternalID as instanceID names it. found=false
	// means no usable mapping: no mapping on that instance, a deactivated
	// user, or a user with no roles. Callers treat it exactly like "mapped
	// but not authorized".
	Resolve(ctx context.Context, instanceID, actorExternalID string) (resolution Resolution, found bool, err error)
}

// Querier is the sqlc surface DB needs. *db.Queries satisfies it.
type Querier interface {
	GetUserByExternalIdentity(ctx context.Context, arg db.GetUserByExternalIdentityParams) ([]db.GetUserByExternalIdentityRow, error)
}

// DB is the Directory over plugin_user_identities, joined to user_roles, with
// deactivated users excluded by the query itself.
type DB struct {
	Querier Querier
}

// Resolve implements Directory.
func (d DB) Resolve(ctx context.Context, instanceID, actorExternalID string) (Resolution, bool, error) {
	if instanceID == "" || actorExternalID == "" {
		// An empty instance would match nothing anyway; refusing here keeps an
		// unattributed call from ever reading as a lookup that happened.
		return Resolution{}, false, nil
	}
	rows, err := d.Querier.GetUserByExternalIdentity(ctx, db.GetUserByExternalIdentityParams{
		InstanceID:     instanceID,
		ExternalUserID: actorExternalID,
	})
	if err != nil {
		return Resolution{}, false, fmt.Errorf("look up actor %q on instance %s: %w", actorExternalID, instanceID, err)
	}
	if len(rows) == 0 {
		return Resolution{}, false, nil
	}
	res := Resolution{UserID: rows[0].ID, Username: rows[0].Username}
	for _, row := range rows {
		res.Roles = append(res.Roles, model.Role(row.Role))
	}
	return res, true, nil
}
