// Package audience resolves the effective entry list for a plugin audience,
// including the auto-appended gleipnir.in-app synthetic entry (§6.2).
package audience

import (
	"context"
	"errors"

	"github.com/felag-engineering/gleipnir/internal/db"
)

const (
	// InAppEntryID is the stable identifier used for the synthetic in-app entry
	// that is always appended last unless disable_in_app_fallback is set.
	InAppEntryID = "gleipnir.in-app"

	// InAppPluginID is the PluginInstanceID value set on the synthetic entry.
	// The empty string signals "no real plugin_instances row" — dispatch code
	// uses (Auto && PluginInstanceID == "") to detect the synthetic entry and
	// skip gRPC calls / DB inserts.
	InAppPluginID = ""
)

// ErrAudienceNotFound is returned by Resolve when the audience does not exist
// (the query returned zero rows).
var ErrAudienceNotFound = errors.New("audience not found")

// EffectiveEntry is a resolved audience entry, including the synthetic
// gleipnir.in-app entry that the resolver appends automatically.
type EffectiveEntry struct {
	EntryID          string
	PluginInstanceID string
	Position         int64
	Notify           bool
	Request          bool
	ConfigJSON       string
	// Auto is true for the synthetic gleipnir.in-app entry. It is never
	// persisted; it is injected at resolve time.
	Auto bool
}

// Resolve converts raw sqlc LEFT-JOIN rows into the ordered list of effective
// entries for this audience, appending the synthetic gleipnir.in-app entry
// unless disable_in_app_fallback is set.
//
// LEFT-JOIN rows where EntryID or PluginInstanceID is nil represent an
// audience with zero persisted entries — they are skipped but the
// disable_in_app_fallback flag is still read from rows[0].
func Resolve(rows []db.GetPluginAudienceWithEntriesRow) ([]EffectiveEntry, error) {
	if len(rows) == 0 {
		return nil, ErrAudienceNotFound
	}

	// Read the disable flag from the audience-level columns (same on every row).
	disableInAppFallback := rows[0].DisableInAppFallback != 0

	var entries []EffectiveEntry
	var maxPosition int64 = -1

	for _, row := range rows {
		// LEFT JOIN produces nil entry columns when the audience has no entries.
		if row.EntryID == nil || row.PluginInstanceID == nil {
			continue
		}
		entry := EffectiveEntry{
			EntryID:          *row.EntryID,
			PluginInstanceID: *row.PluginInstanceID,
		}
		if row.Position != nil {
			entry.Position = *row.Position
			if entry.Position > maxPosition {
				maxPosition = entry.Position
			}
		}
		if row.Notify != nil {
			entry.Notify = *row.Notify != 0
		}
		if row.Request != nil {
			entry.Request = *row.Request != 0
		}
		if row.ConfigJson != nil {
			entry.ConfigJSON = *row.ConfigJson
		}
		entries = append(entries, entry)
	}

	if !disableInAppFallback {
		entries = append(entries, EffectiveEntry{
			EntryID:          InAppEntryID,
			PluginInstanceID: InAppPluginID,
			Position:         maxPosition + 1,
			Notify:           true,
			Request:          true,
			ConfigJSON:       "{}",
			Auto:             true,
		})
	}

	return entries, nil
}

// ResolveByID is a convenience wrapper that loads the audience rows from the
// database and calls Resolve.
func ResolveByID(ctx context.Context, q *db.Queries, audienceID string) ([]EffectiveEntry, error) {
	rows, err := q.GetPluginAudienceWithEntries(ctx, audienceID)
	if err != nil {
		return nil, err
	}
	return Resolve(rows)
}
