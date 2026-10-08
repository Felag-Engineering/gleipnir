package db

import (
	"context"
	"testing"
)

func TestSetSystemSettingIfEmpty(t *testing.T) {
	ctx := context.Background()
	const now = "2026-01-01T00:00:00Z"

	tests := []struct {
		name      string
		preset    *string // nil = no row
		wantWrote int64
		wantValue string
	}{
		{name: "absent key is written", wantWrote: 1, wantValue: "new"},
		{name: "empty value is replaced", preset: ptr(""), wantWrote: 1, wantValue: "new"},
		{name: "existing value is kept", preset: ptr("old"), wantWrote: 0, wantValue: "old"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			q := newTestStore(t).Queries()
			if tc.preset != nil {
				if err := q.UpsertSystemSetting(ctx, UpsertSystemSettingParams{Key: "k", Value: *tc.preset, UpdatedAt: now}); err != nil {
					t.Fatalf("seed: %v", err)
				}
			}

			n, err := q.SetSystemSettingIfEmpty(ctx, SetSystemSettingIfEmptyParams{Key: "k", Value: "new", UpdatedAt: now})
			if err != nil {
				t.Fatalf("SetSystemSettingIfEmpty: %v", err)
			}
			if n != tc.wantWrote {
				t.Errorf("rows affected = %d, want %d", n, tc.wantWrote)
			}
			row, err := q.GetSystemSetting(ctx, "k")
			if err != nil {
				t.Fatalf("GetSystemSetting: %v", err)
			}
			if row.Value != tc.wantValue {
				t.Errorf("value = %q, want %q", row.Value, tc.wantValue)
			}
		})
	}
}

func ptr(s string) *string { return &s }
