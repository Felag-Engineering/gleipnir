package migrations_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/felag-engineering/gleipnir/internal/db/migrations"
)

const (
	v1Manifest = "schema_version: \"1\"\nname: slack\n"
	v2Manifest = "schema_version: \"2\"\nname: github\n"
	ts         = "2024-01-01T00:00:00Z"
)

func mustExec(t *testing.T, db *sql.DB, stmts ...string) {
	t.Helper()
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("exec %q: %v", s, err)
		}
	}
}

func count(t *testing.T, db *sql.DB, query string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(query).Scan(&n); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}

// seedPurgeFixture builds a full schema holding a v1 plugin with two
// instances, a v2 plugin, and a dependent row in every table the purge touches.
func seedPurgeFixture(t *testing.T, db *sql.DB) {
	t.Helper()
	applyInitialSchema(t, db)
	if err := migrations.Apply(context.Background(), db, migrations.All(), nil); err != nil {
		t.Fatalf("apply All(): %v", err)
	}
	const subscribedYAML = "name: sub-v1\ntrigger:\n  type: subscribed\n  source: slack-prod\n  event_kind: message\n"
	const audienceYAML = "name: aud-v1\ntrigger:\n  type: manual\naudience: v1-only\n"
	const survivorYAML = "name: ok\ntrigger:\n  type: manual\naudience: mixed\n"
	mustExec(t, db,
		`INSERT INTO users(id, username, password_hash, created_at) VALUES ('u1','ann','x','`+ts+`')`,
		`INSERT INTO plugins(id,name,plugin_version,manifest_snapshot,trusted_pubkey,status,created_at,updated_at)
		 VALUES ('p1','slack','1.0.0','`+v1Manifest+`','not-a-key','active','`+ts+`','`+ts+`')`,
		`INSERT INTO plugins(id,name,plugin_version,manifest_snapshot,trusted_pubkey,status,created_at,updated_at)
		 VALUES ('p2','github','2.0.0','`+v2Manifest+`','not-a-key','active','`+ts+`','`+ts+`')`,
		`INSERT INTO plugin_instances(id,plugin_id,instance_name,health_state,created_at,updated_at)
		 VALUES ('i1','p1','slack-prod','healthy','`+ts+`','`+ts+`')`,
		`INSERT INTO plugin_instances(id,plugin_id,instance_name,health_state,created_at,updated_at)
		 VALUES ('i2','p1','slack-dev','healthy','`+ts+`','`+ts+`')`,
		`INSERT INTO plugin_instances(id,plugin_id,instance_name,health_state,created_at,updated_at)
		 VALUES ('i3','p2','gh-main','healthy','`+ts+`','`+ts+`')`,
		`INSERT INTO policies(id,name,trigger_type,yaml,created_at,updated_at)
		 VALUES ('pol-sub','sub-v1','subscribed','`+subscribedYAML+`','`+ts+`','`+ts+`')`,
		`INSERT INTO policies(id,name,trigger_type,yaml,created_at,updated_at)
		 VALUES ('pol-aud','aud-v1','manual','`+audienceYAML+`','`+ts+`','`+ts+`')`,
		`INSERT INTO policies(id,name,trigger_type,yaml,created_at,updated_at)
		 VALUES ('pol-ok','ok','manual','`+survivorYAML+`','`+ts+`','`+ts+`')`,
		`INSERT INTO runs(id,policy_id,status,trigger_type,trigger_payload,started_at,created_at)
		 VALUES ('r1','pol-aud','interrupted','manual','{}','`+ts+`','`+ts+`')`,
		`INSERT INTO plugin_audiences(id,name,created_at,updated_at) VALUES ('a-mixed','mixed','`+ts+`','`+ts+`')`,
		`INSERT INTO plugin_audiences(id,name,created_at,updated_at) VALUES ('a-v1','v1-only','`+ts+`','`+ts+`')`,
		`INSERT INTO audience_entries(id,audience_id,plugin_instance_id,position) VALUES ('e1','a-mixed','i1',1)`,
		`INSERT INTO audience_entries(id,audience_id,plugin_instance_id,position) VALUES ('e2','a-mixed','i3',2)`,
		`INSERT INTO audience_entries(id,audience_id,plugin_instance_id,position) VALUES ('e3','a-v1','i2',1)`,
		`INSERT INTO plugin_pending_requests(id,plugin_instance_id,run_id,audience_entry_id,status,created_at)
		 VALUES ('pr1','i1','r1','e1','pending','`+ts+`')`,
		`INSERT INTO plugin_oauth_nonces(nonce,instance_id,expires_at,created_at) VALUES ('n1','i1','`+ts+`','`+ts+`')`,
		`INSERT INTO plugin_oauth_nonces(nonce,instance_id,expires_at,created_at) VALUES ('n3','i3','`+ts+`','`+ts+`')`,
		`INSERT INTO plugin_event_dedup(plugin_instance_id,event_kind,event_id,created_at_ms) VALUES ('i1','message','ev1',1)`,
		`INSERT INTO plugin_event_dedup(plugin_instance_id,event_kind,event_id,created_at_ms) VALUES ('i3','push','ev3',1)`,
		`INSERT INTO plugin_event_cursors(plugin_instance_id,cursor,sequence,scope_hash,updated_at) VALUES ('i1','c',1,'h','`+ts+`')`,
		`INSERT INTO plugin_user_identities(instance_id,user_id,external_user_id,link_method,created_at) VALUES ('i1','u1','U1','admin_set','`+ts+`')`,
		`INSERT INTO plugin_user_identities(instance_id,user_id,external_user_id,link_method,created_at) VALUES ('i3','u1','U3','admin_set','`+ts+`')`,
		`INSERT INTO mcp_servers(id,name,url,created_at,plugin_instance_id) VALUES ('m1','slack-prod','http://x','`+ts+`','i1')`,
		`INSERT INTO mcp_servers(id,name,url,created_at,plugin_instance_id) VALUES ('m3','gh-main','http://y','`+ts+`','i3')`,
		`INSERT INTO plugin_audit_events(plugin_instance_id,event_type,severity,payload_json,created_at) VALUES ('i1','plugin_installed','info','{}','`+ts+`')`,
		`INSERT INTO plugin_audit_events(plugin_instance_id,event_type,severity,payload_json,created_at) VALUES ('i3','plugin_installed','info','{}','`+ts+`')`,
	)
}

func runPurge(t *testing.T, db *sql.DB) {
	t.Helper()
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := migrations.ApplyPurgeV1(context.Background(), tx); err != nil {
		tx.Rollback()
		t.Fatalf("ApplyPurgeV1: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

func TestPurgeV1Plugins(t *testing.T) {
	db := openTestDB(t)
	seedPurgeFixture(t, db)
	auditBefore := count(t, db, `SELECT COUNT(*) FROM plugin_audit_events`)

	runPurge(t, db)

	gone := []struct{ name, query string }{
		{"v1 plugin", `SELECT COUNT(*) FROM plugins WHERE id='p1'`},
		{"v1 instances", `SELECT COUNT(*) FROM plugin_instances WHERE plugin_id='p1'`},
		{"v1 audience entries", `SELECT COUNT(*) FROM audience_entries WHERE plugin_instance_id IN ('i1','i2')`},
		{"v1 pending requests", `SELECT COUNT(*) FROM plugin_pending_requests`},
		{"v1 oauth nonces", `SELECT COUNT(*) FROM plugin_oauth_nonces WHERE instance_id='i1'`},
		{"v1 dedup", `SELECT COUNT(*) FROM plugin_event_dedup WHERE plugin_instance_id='i1'`},
		{"v1 cursors", `SELECT COUNT(*) FROM plugin_event_cursors`},
		{"v1 identities", `SELECT COUNT(*) FROM plugin_user_identities WHERE instance_id='i1'`},
		{"v1 managed mcp server", `SELECT COUNT(*) FROM mcp_servers WHERE id='m1'`},
	}
	for _, c := range gone {
		t.Run("removed/"+c.name, func(t *testing.T) {
			if n := count(t, db, c.query); n != 0 {
				t.Errorf("%d rows remain", n)
			}
		})
	}

	kept := []struct {
		name, query string
		want        int
	}{
		{"v2 plugin", `SELECT COUNT(*) FROM plugins WHERE id='p2'`, 1},
		{"v2 instance", `SELECT COUNT(*) FROM plugin_instances WHERE id='i3'`, 1},
		{"audiences", `SELECT COUNT(*) FROM plugin_audiences`, 2},
		{"surviving audience entry", `SELECT COUNT(*) FROM audience_entries WHERE id='e2' AND position=2`, 1},
		{"v2 nonce", `SELECT COUNT(*) FROM plugin_oauth_nonces WHERE instance_id='i3'`, 1},
		{"v2 dedup", `SELECT COUNT(*) FROM plugin_event_dedup WHERE plugin_instance_id='i3'`, 1},
		{"v2 identity", `SELECT COUNT(*) FROM plugin_user_identities WHERE instance_id='i3'`, 1},
		{"v2 managed mcp server", `SELECT COUNT(*) FROM mcp_servers WHERE id='m3'`, 1},
		{"policies untouched", `SELECT COUNT(*) FROM policies`, 3},
		{"runs untouched", `SELECT COUNT(*) FROM runs`, 1},
		{"pre-existing audit row, v1 link nulled", `SELECT COUNT(*) FROM plugin_audit_events WHERE event_type='plugin_installed' AND plugin_instance_id IS NULL`, 1},
		{"pre-existing audit row, v2 link kept", `SELECT COUNT(*) FROM plugin_audit_events WHERE event_type='plugin_installed' AND plugin_instance_id='i3'`, 1},
	}
	for _, c := range kept {
		t.Run("kept/"+c.name, func(t *testing.T) {
			if n := count(t, db, c.query); n != c.want {
				t.Errorf("count = %d, want %d", n, c.want)
			}
		})
	}

	// The two pre-existing rows survive; one plugin_purged_v1 and one orphan row are new.
	if got, want := count(t, db, `SELECT COUNT(*) FROM plugin_audit_events`), auditBefore+2; got != want {
		t.Errorf("audit rows = %d, want %d", got, want)
	}

	var severity, payload string
	var instanceID sql.NullString
	err := db.QueryRow(`SELECT severity, plugin_instance_id, payload_json FROM plugin_audit_events WHERE event_type='plugin_purged_v1'`).
		Scan(&severity, &instanceID, &payload)
	if err != nil {
		t.Fatalf("read plugin_purged_v1: %v", err)
	}
	if severity != "high" || instanceID.Valid {
		t.Errorf("severity=%q instance_valid=%v, want high / NULL", severity, instanceID.Valid)
	}
	var purged struct {
		PluginName  string   `json:"plugin_name"`
		Version     string   `json:"version"`
		Instances   []string `json:"instance_names"`
		Fingerprint string   `json:"pubkey_fingerprint"`
	}
	if err := json.Unmarshal([]byte(payload), &purged); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if purged.PluginName != "slack" || purged.Version != "1.0.0" || purged.Fingerprint == "" ||
		!reflect.DeepEqual(purged.Instances, []string{"slack-dev", "slack-prod"}) {
		t.Errorf("purged payload = %+v", purged)
	}

	if err := db.QueryRow(`SELECT payload_json FROM plugin_audit_events WHERE event_type='v1_purge_orphaned_policies'`).Scan(&payload); err != nil {
		t.Fatalf("read orphan audit: %v", err)
	}
	var orphans struct {
		Subscribed []string `json:"subscribed_policy_ids"`
		Audience   []string `json:"emptied_audience_policy_ids"`
	}
	if err := json.Unmarshal([]byte(payload), &orphans); err != nil {
		t.Fatalf("unmarshal orphans: %v", err)
	}
	if !reflect.DeepEqual(orphans.Subscribed, []string{"pol-sub"}) || !reflect.DeepEqual(orphans.Audience, []string{"pol-aud"}) {
		t.Errorf("orphans = %+v; the mixed-audience policy must not be listed", orphans)
	}

	rows, err := db.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatalf("foreign_key_check: %v", err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Error("foreign_key_check reported a violation")
	}
}

func TestPurgeV1PluginsIdempotent(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	seedPurgeFixture(t, db)
	m := &migrations.PurgeV1Plugins{}

	if skip, err := m.ShouldSkip(ctx, db); err != nil || skip {
		t.Fatalf("ShouldSkip before purge = %v, %v; want false", skip, err)
	}
	if err := migrations.Apply(ctx, db, []migrations.Migration{m}, nil); err != nil {
		t.Fatalf("first Apply: %v", err)
	}
	auditAfterFirst := count(t, db, `SELECT COUNT(*) FROM plugin_audit_events`)

	if skip, err := m.ShouldSkip(ctx, db); err != nil || !skip {
		t.Fatalf("ShouldSkip after purge = %v, %v; want true", skip, err)
	}
	if err := migrations.Apply(ctx, db, []migrations.Migration{m}, nil); err != nil {
		t.Fatalf("second Apply: %v", err)
	}
	// The direct helper must be a no-op too, not only the skip path.
	runPurge(t, db)
	if got := count(t, db, `SELECT COUNT(*) FROM plugin_audit_events`); got != auditAfterFirst {
		t.Errorf("audit rows after re-run = %d, want %d", got, auditAfterFirst)
	}
	if got := count(t, db, `SELECT COUNT(*) FROM plugins`); got != 1 {
		t.Errorf("plugins = %d, want 1 (the v2 plugin)", got)
	}
}

func TestPurgeV1PluginsNoV1RowsWritesNothing(t *testing.T) {
	db := openTestDB(t)
	applyInitialSchema(t, db)
	if err := migrations.Apply(context.Background(), db, migrations.All(), nil); err != nil {
		t.Fatalf("apply All(): %v", err)
	}
	runPurge(t, db)
	if n := count(t, db, `SELECT COUNT(*) FROM plugin_audit_events`); n != 0 {
		t.Errorf("audit rows = %d, want 0", n)
	}
}

// The flip (#1005) registers the migration and must invert this assertion.
func TestPurgeV1PluginsNotRegistered(t *testing.T) {
	for _, m := range migrations.All() {
		if _, ok := m.(*migrations.PurgeV1Plugins); ok {
			t.Fatal("PurgeV1Plugins is registered in All(); only the flip (#1005) may register it")
		}
	}
}
