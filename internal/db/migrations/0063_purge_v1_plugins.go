package migrations

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/felag-engineering/gleipnir/plugin-sdk/manifestv2"
	"github.com/felag-engineering/gleipnir/plugin-sdk/signing"
)

// PurgeV1Plugins deletes every v1 (gRPC-era) plugin and the rows that hang off
// it (issue #1004). After the v2 cutover no binary can run a v1 plugin, and the
// owner decision is to delete rather than retain them.
//
// THIS MIGRATION IS DELIBERATELY NOT REGISTERED IN All(). Migrations run on
// every boot regardless of build tag, so registering it early would purge
// plugins out from under the v1 build. The flip (#1005) appends it to All().
// Until then tests drive it through ApplyPurgeV1.
//
// A "v1 plugin" is a plugins row whose manifest_snapshot is not a v2 manifest
// (manifestv2.IsV2), the same sniff the loader uses to route a bundle.
//
// Two references are ON DELETE RESTRICT and must be cleared first:
// audience_entries.plugin_instance_id and plugin_pending_requests.
// plugin_instance_id. plugin_oauth_nonces.instance_id has no foreign key.
// Everything else (instances, pending manifests, managed mcp_servers, dedup,
// cursors, containers, identities) cascades, and plugin_audit_events /
// plugin_container_images are SET NULL, so the gRPC-era audit history stays.
//
// Policies are YAML blobs (ADR-002) and are not edited. The migration scans
// for ones this orphans and records them in one audit row instead.
type PurgeV1Plugins struct{}

const (
	auditEventPluginPurgedV1          = "plugin_purged_v1"
	auditEventV1PurgeOrphanedPolicies = "v1_purge_orphaned_policies"
)

// purgeNow is the clock for audit timestamps; tests may swap it.
var purgeNow = func() time.Time { return time.Now() }

func (m *PurgeV1Plugins) Version() int { return 53 }
func (m *PurgeV1Plugins) Name() string { return "purge_v1_plugins" }

// ShouldSkip makes a second run a no-op: once the v1 rows are gone there is
// nothing to select.
func (m *PurgeV1Plugins) ShouldSkip(ctx context.Context, db *sql.DB) (bool, error) {
	v1, err := selectV1Plugins(ctx, db)
	if err != nil {
		return false, err
	}
	return len(v1) == 0, nil
}

func (m *PurgeV1Plugins) Up(ctx context.Context, tx *sql.Tx) error {
	return ApplyPurgeV1(ctx, tx)
}

type v1Plugin struct {
	id            string
	name          string
	version       string
	pubkey        string
	instanceIDs   []string
	instanceNames []string
}

// queryer is the read surface shared by *sql.DB and *sql.Tx.
type queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// ApplyPurgeV1 runs the purge inside tx. It is exported so tests can run the
// migration while it is unregistered. It is a no-op when there are no v1 plugins.
func ApplyPurgeV1(ctx context.Context, tx *sql.Tx) error {
	// Without enforcement the CASCADE / SET NULL clauses would silently not
	// fire and the purge would leave orphans behind.
	var fkEnabled int
	if err := tx.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&fkEnabled); err != nil {
		return fmt.Errorf("read foreign_keys pragma: %w", err)
	}
	if fkEnabled != 1 {
		return fmt.Errorf("foreign_keys is off; refusing to purge without cascades")
	}

	plugins, err := selectV1Plugins(ctx, tx)
	if err != nil {
		return err
	}
	if len(plugins) == 0 {
		return nil
	}

	var pluginIDs, instanceIDs, instanceNames []string
	for _, p := range plugins {
		pluginIDs = append(pluginIDs, p.id)
		instanceIDs = append(instanceIDs, p.instanceIDs...)
		instanceNames = append(instanceNames, p.instanceNames...)
	}

	// Scan before deleting: the audience entries are what tell us which
	// audiences are about to lose their last plugin entry.
	orphaned, err := findOrphanedPolicies(ctx, tx, instanceNames, instanceIDs)
	if err != nil {
		return err
	}

	now := purgeNow().UTC().Format(time.RFC3339Nano)
	for _, p := range plugins {
		payload := map[string]any{
			"plugin_name":        p.name,
			"version":            p.version,
			"instance_names":     nonNilSorted(p.instanceNames),
			"pubkey_fingerprint": pubkeyFingerprint([]byte(p.pubkey)),
		}
		if err := insertPurgeAudit(ctx, tx, auditEventPluginPurgedV1, payload, now); err != nil {
			return err
		}
	}

	if len(instanceIDs) > 0 {
		in, args := inClause(instanceIDs)
		if _, err := tx.ExecContext(ctx, `DELETE FROM plugin_pending_requests WHERE plugin_instance_id IN `+in, args...); err != nil {
			return fmt.Errorf("delete v1 plugin_pending_requests: %w", err)
		}
		// Positions are left with gaps: ordering is preserved and
		// UNIQUE(audience_id, position) still holds.
		if _, err := tx.ExecContext(ctx, `DELETE FROM audience_entries WHERE plugin_instance_id IN `+in, args...); err != nil {
			return fmt.Errorf("delete v1 audience_entries: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM plugin_oauth_nonces WHERE instance_id IN `+in, args...); err != nil {
			return fmt.Errorf("delete v1 plugin_oauth_nonces: %w", err)
		}
	}

	in, args := inClause(pluginIDs)
	if _, err := tx.ExecContext(ctx, `DELETE FROM plugins WHERE id IN `+in, args...); err != nil {
		return fmt.Errorf("delete v1 plugins: %w", err)
	}

	if len(orphaned.subscribedPolicyIDs) > 0 || len(orphaned.emptiedAudiencePolicyIDs) > 0 {
		slog.Warn("v1 plugin purge orphaned policies; they are left unchanged",
			"subscribed_policy_ids", orphaned.subscribedPolicyIDs,
			"emptied_audience_policy_ids", orphaned.emptiedAudiencePolicyIDs)
		payload := map[string]any{
			"subscribed_policy_ids":       nonNilSorted(orphaned.subscribedPolicyIDs),
			"emptied_audience_policy_ids": nonNilSorted(orphaned.emptiedAudiencePolicyIDs),
		}
		if err := insertPurgeAudit(ctx, tx, auditEventV1PurgeOrphanedPolicies, payload, now); err != nil {
			return err
		}
	}

	if err := checkForeignKeys(ctx, tx); err != nil {
		return err
	}
	slog.Info("migrated: purged v1 plugins", "plugins", len(pluginIDs), "instances", len(instanceIDs))
	return nil
}

// selectV1Plugins returns plugins whose manifest is not v2, with their
// instances attached, ordered by name for deterministic audit rows.
func selectV1Plugins(ctx context.Context, q queryer) ([]v1Plugin, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT id, name, plugin_version, manifest_snapshot, trusted_pubkey FROM plugins ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("query plugins: %w", err)
	}
	var out []v1Plugin
	for rows.Next() {
		var p v1Plugin
		var manifest string
		if err := rows.Scan(&p.id, &p.name, &p.version, &manifest, &p.pubkey); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan plugin: %w", err)
		}
		if !manifestv2.IsV2([]byte(manifest)) {
			out = append(out, p)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("iterate plugins: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close plugins rows: %w", err)
	}

	// The plugins cursor is closed above because the pool has a single
	// connection: the instance queries below would otherwise deadlock.
	for i := range out {
		instRows, err := q.QueryContext(ctx,
			`SELECT id, instance_name FROM plugin_instances WHERE plugin_id = ? ORDER BY instance_name`, out[i].id)
		if err != nil {
			return nil, fmt.Errorf("query instances of plugin %s: %w", out[i].name, err)
		}
		for instRows.Next() {
			var id, name string
			if err := instRows.Scan(&id, &name); err != nil {
				instRows.Close()
				return nil, fmt.Errorf("scan instance: %w", err)
			}
			out[i].instanceIDs = append(out[i].instanceIDs, id)
			out[i].instanceNames = append(out[i].instanceNames, name)
		}
		if err := instRows.Err(); err != nil {
			instRows.Close()
			return nil, fmt.Errorf("iterate instances: %w", err)
		}
		if err := instRows.Close(); err != nil {
			return nil, fmt.Errorf("close instance rows: %w", err)
		}
	}
	return out, nil
}

type orphanedPolicies struct {
	subscribedPolicyIDs      []string
	emptiedAudiencePolicyIDs []string
}

// findOrphanedPolicies is read-only. A policy is orphaned if it subscribes to
// a purged instance by name, or names an audience whose every entry points at
// a purged instance.
func findOrphanedPolicies(ctx context.Context, q queryer, instanceNames, instanceIDs []string) (orphanedPolicies, error) {
	var res orphanedPolicies
	purgedNames := toSet(instanceNames)

	emptied, err := audiencesEmptiedBy(ctx, q, instanceIDs)
	if err != nil {
		return res, err
	}

	rows, err := q.QueryContext(ctx, `SELECT id, yaml FROM policies ORDER BY id`)
	if err != nil {
		return res, fmt.Errorf("query policies: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, body string
		if err := rows.Scan(&id, &body); err != nil {
			return res, fmt.Errorf("scan policy: %w", err)
		}
		var doc struct {
			Audience string `yaml:"audience"`
			Trigger  struct {
				Type   string `yaml:"type"`
				Source string `yaml:"source"`
			} `yaml:"trigger"`
		}
		if err := yaml.Unmarshal([]byte(body), &doc); err != nil {
			slog.Warn("v1 purge: skipping unparseable policy YAML in orphan scan", "policy_id", id, "err", err)
			continue
		}
		if doc.Trigger.Type == "subscribed" && purgedNames[doc.Trigger.Source] {
			res.subscribedPolicyIDs = append(res.subscribedPolicyIDs, id)
		}
		if doc.Audience != "" && emptied[doc.Audience] {
			res.emptiedAudiencePolicyIDs = append(res.emptiedAudiencePolicyIDs, id)
		}
	}
	if err := rows.Err(); err != nil {
		return res, fmt.Errorf("iterate policies: %w", err)
	}
	return res, nil
}

// audiencesEmptiedBy returns the names of audiences that have at least one
// entry on a purged instance and none on any other instance.
func audiencesEmptiedBy(ctx context.Context, q queryer, instanceIDs []string) (map[string]bool, error) {
	emptied := map[string]bool{}
	if len(instanceIDs) == 0 {
		return emptied, nil
	}
	in, args := inClause(instanceIDs)
	rows, err := q.QueryContext(ctx, `
SELECT a.name FROM plugin_audiences a
WHERE EXISTS (SELECT 1 FROM audience_entries e WHERE e.audience_id = a.id AND e.plugin_instance_id IN `+in+`)
  AND NOT EXISTS (SELECT 1 FROM audience_entries e WHERE e.audience_id = a.id AND e.plugin_instance_id NOT IN `+in+`)`,
		append(args, args...)...)
	if err != nil {
		return nil, fmt.Errorf("query audiences emptied by purge: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("scan audience name: %w", err)
		}
		emptied[name] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate audiences: %w", err)
	}
	return emptied, nil
}

func insertPurgeAudit(ctx context.Context, tx *sql.Tx, eventType string, payload map[string]any, now string) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal %s payload: %w", eventType, err)
	}
	// plugin_instance_id stays NULL: the instances these rows describe are
	// deleted by the same transaction.
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO plugin_audit_events (plugin_instance_id, event_type, severity, payload_json, created_at)
		 VALUES (NULL, ?, 'high', ?, ?)`,
		eventType, string(body), now,
	); err != nil {
		return fmt.Errorf("insert %s audit event: %w", eventType, err)
	}
	return nil
}

func checkForeignKeys(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return fmt.Errorf("foreign_key_check: %w", err)
	}
	defer rows.Close()
	if rows.Next() {
		var table, parent string
		var rowid sql.NullInt64
		var fkid int
		if err := rows.Scan(&table, &rowid, &parent, &fkid); err != nil {
			return fmt.Errorf("scan foreign_key_check row: %w", err)
		}
		return fmt.Errorf("foreign_key_check found a violation after purge: table %s rowid %d references missing %s", table, rowid.Int64, parent)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate foreign_key_check: %w", err)
	}
	return nil
}

// pubkeyFingerprint mirrors the loader's audit fingerprint (hex Minisign key
// ID) so purge rows can be matched against the original install rows.
func pubkeyFingerprint(pubkey []byte) string {
	if len(pubkey) == 0 {
		return "(unknown)"
	}
	pk, _, err := signing.ParsePublicKey(pubkey)
	if err != nil {
		return "(unparseable)"
	}
	return fmt.Sprintf("%x", pk.KeyID)
}

func inClause(ids []string) (string, []any) {
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	return "(" + strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",") + ")", args
}

func toSet(items []string) map[string]bool {
	s := make(map[string]bool, len(items))
	for _, it := range items {
		s[it] = true
	}
	return s
}

// nonNilSorted keeps the JSON payload an array (never null) in stable order.
func nonNilSorted(s []string) []string {
	out := append([]string{}, s...)
	sort.Strings(out)
	return out
}
