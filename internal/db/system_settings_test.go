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

func TestClearSystemSettingIfPrefix(t *testing.T) {
	ctx := context.Background()
	const now = "2026-01-01T00:00:00Z"

	tests := []struct {
		name      string
		preset    *string // nil = no row
		prefix    string
		wantRows  int64
		wantValue string
	}{
		{name: "matching prefix is blanked", preset: ptr("anthropic:claude-a"), prefix: "anthropic:", wantRows: 1, wantValue: ""},
		{name: "other provider is kept", preset: ptr("openai:gpt-a"), prefix: "anthropic:", wantRows: 0, wantValue: "openai:gpt-a"},
		{name: "longer provider name sharing the prefix text is kept", preset: ptr("openai-compat:m"), prefix: "openai:", wantRows: 0, wantValue: "openai-compat:m"},
		{name: "LIKE wildcards are literal", preset: ptr("abc:m"), prefix: "a_c:", wantRows: 0, wantValue: "abc:m"},
		{name: "percent is literal", preset: ptr("abc:m"), prefix: "%:", wantRows: 0, wantValue: "abc:m"},
		{name: "already empty is a no-op", preset: ptr(""), prefix: "anthropic:", wantRows: 0, wantValue: ""},
		{name: "absent row is a no-op", prefix: "anthropic:", wantRows: 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			q := newTestStore(t).Queries()
			if tc.preset != nil {
				if err := q.UpsertSystemSetting(ctx, UpsertSystemSettingParams{Key: "default_model", Value: *tc.preset, UpdatedAt: "old"}); err != nil {
					t.Fatalf("seed: %v", err)
				}
			}

			n, err := q.ClearSystemSettingIfPrefix(ctx, ClearSystemSettingIfPrefixParams{Key: "default_model", Prefix: tc.prefix, UpdatedAt: now})
			if err != nil {
				t.Fatalf("ClearSystemSettingIfPrefix: %v", err)
			}
			if n != tc.wantRows {
				t.Errorf("rows affected = %d, want %d", n, tc.wantRows)
			}
			if tc.preset == nil {
				return
			}
			row, err := q.GetSystemSetting(ctx, "default_model")
			if err != nil {
				t.Fatalf("GetSystemSetting: %v", err)
			}
			if row.Value != tc.wantValue {
				t.Errorf("value = %q, want %q", row.Value, tc.wantValue)
			}
		})
	}

	t.Run("cleared value is reseedable", func(t *testing.T) {
		q := newTestStore(t).Queries()
		if err := q.UpsertSystemSetting(ctx, UpsertSystemSettingParams{Key: "default_model", Value: "anthropic:claude-a", UpdatedAt: now}); err != nil {
			t.Fatalf("seed: %v", err)
		}
		if _, err := q.ClearSystemSettingIfPrefix(ctx, ClearSystemSettingIfPrefixParams{Key: "default_model", Prefix: "anthropic:", UpdatedAt: now}); err != nil {
			t.Fatalf("clear: %v", err)
		}
		n, err := q.SetSystemSettingIfEmpty(ctx, SetSystemSettingIfEmptyParams{Key: "default_model", Value: "openai:gpt-a", UpdatedAt: now})
		if err != nil || n != 1 {
			t.Fatalf("reseed rows=%d err=%v, want 1 nil", n, err)
		}
	})
}

func ptr(s string) *string { return &s }
