package oauth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
	sdkmanifest "github.com/felag-engineering/gleipnir/plugin-sdk/manifestv2"
)

func TestDBStore_CredentialWriteActorAttribution(t *testing.T) {
	adminID := "admin-1"
	tests := []struct {
		name      string
		ctx       context.Context
		wantActor *string
	}{
		{name: "admin write carries actor", ctx: WithActor(context.Background(), adminID), wantActor: &adminID},
		{name: "system write keeps NULL actor", ctx: context.Background(), wantActor: nil},
		{name: "empty actor id is treated as system", ctx: WithActor(context.Background(), ""), wantActor: nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			q := &fakeOAuthQuerier{instance: db.PluginInstance{ID: "inst-1", HealthState: "healthy"}}
			store := NewDBStore(q, noopEncrypt, noopDecrypt, q, func() time.Time { return time.Unix(1000000, 0) })
			if err := store.SaveCredentials(context.Background(), "inst-1", StoredCredentials{Strategy: sdkmanifest.AuthStrategyStaticAPIKey}, 0); err != nil {
				t.Fatalf("seed: %v", err)
			}
			if err := store.SetStaticAPIKey(tc.ctx, "inst-1", "X-Key", "", "val"); err != nil {
				t.Fatalf("SetStaticAPIKey: %v", err)
			}
			var got *db.InsertPluginAuditEventParams
			for i := range q.auditEvents {
				if q.auditEvents[i].EventType == auditCredentialSet {
					got = &q.auditEvents[i]
				}
			}
			if got == nil {
				t.Fatal("no credentials_set event")
			}
			switch {
			case tc.wantActor == nil && got.ActorUserID != nil:
				t.Errorf("actor = %q, want NULL", *got.ActorUserID)
			case tc.wantActor != nil && (got.ActorUserID == nil || *got.ActorUserID != *tc.wantActor):
				t.Errorf("actor = %v, want %q", got.ActorUserID, *tc.wantActor)
			}
		})
	}
}

// The refresh scanner's failure path has no admin on its context and must
// keep system attribution.
func TestDBStore_MarkRefreshFailed_ActorIsNull(t *testing.T) {
	q := &fakeOAuthQuerier{instance: db.PluginInstance{ID: "inst-1", HealthState: "healthy"}}
	store := NewDBStore(q, noopEncrypt, noopDecrypt, q, func() time.Time { return time.Unix(1000000, 0) })
	if err := store.MarkRefreshFailed(context.Background(), "inst-1", errors.New("boom")); err != nil {
		t.Fatalf("MarkRefreshFailed: %v", err)
	}
	for _, ev := range q.auditEvents {
		if ev.ActorUserID != nil {
			t.Errorf("event %s has actor %q, want NULL", ev.EventType, *ev.ActorUserID)
		}
	}
}
