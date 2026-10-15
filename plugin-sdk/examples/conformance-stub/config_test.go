package main

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func lookupFrom(env map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) {
		v, ok := env[k]
		return v, ok
	}
}

func TestParseConfig(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		check   func(t *testing.T, c Config)
		wantErr string
	}{
		{
			name: "defaults",
			check: func(t *testing.T, c Config) {
				want := Profiles{ToolProvider: true, EventSource: true, HumanChannel: true}
				if c.Profiles != want {
					t.Errorf("Profiles = %+v, want %+v", c.Profiles, want)
				}
				if c.EmitCount != 3 || c.EmitInterval != time.Second {
					t.Errorf("emit = %d every %s, want 3 every 1s", c.EmitCount, c.EmitInterval)
				}
				if c.Assurance != "authenticated" || c.AutoResolve != "off" || c.AutoResolveDelay != 0 {
					t.Errorf("unexpected channel defaults: %+v", c)
				}
				if c.DataDir != "/data" {
					t.Errorf("DataDir = %q, want /data", c.DataDir)
				}
			},
		},
		{
			name: "empty value falls back to the default",
			env:  map[string]string{"STUB_PROFILES": ""},
			check: func(t *testing.T, c Config) {
				if len(c.Profiles.Names()) != 3 {
					t.Errorf("Profiles = %v, want all three", c.Profiles.Names())
				}
			},
		},
		{
			name: "single profile with spaces",
			env:  map[string]string{"STUB_PROFILES": " event_source "},
			check: func(t *testing.T, c Config) {
				if got := c.Profiles.Names(); !reflect.DeepEqual(got, []string{"event_source"}) {
					t.Errorf("Names() = %v", got)
				}
			},
		},
		{
			name: "auto resolve configured",
			env: map[string]string{
				"STUB_AUTO_RESOLVE": "first_option", "STUB_AUTO_RESOLVE_DELAY_MS": "250",
				"STUB_ACTOR_ID": "u-1", "STUB_ASSURANCE": "weak",
			},
			check: func(t *testing.T, c Config) {
				if c.AutoResolve != "first_option" || c.AutoResolveDelay != 250*time.Millisecond ||
					c.ActorID != "u-1" || c.Assurance != "weak" {
					t.Errorf("unexpected config: %+v", c)
				}
			},
		},
		{name: "unknown profile", env: map[string]string{"STUB_PROFILES": "tool_provider,bogus"}, wantErr: `STUB_PROFILES: unknown profile "bogus"`},
		{name: "duplicate profile", env: map[string]string{"STUB_PROFILES": "event_source,event_source"}, wantErr: "listed twice"},
		{name: "trailing comma", env: map[string]string{"STUB_PROFILES": "event_source,"}, wantErr: "unknown profile"},
		{name: "bad assurance", env: map[string]string{"STUB_ASSURANCE": "strong"}, wantErr: "STUB_ASSURANCE"},
		{name: "bad auto resolve", env: map[string]string{"STUB_AUTO_RESOLVE": "yes"}, wantErr: "STUB_AUTO_RESOLVE"},
		{
			name:    "auto resolve without channel profile",
			env:     map[string]string{"STUB_PROFILES": "tool_provider", "STUB_AUTO_RESOLVE": "reject"},
			wantErr: "needs the human_channel profile",
		},
		{name: "negative count", env: map[string]string{"STUB_EMIT_COUNT": "-1"}, wantErr: "STUB_EMIT_COUNT: must not be negative"},
		{name: "non-numeric interval", env: map[string]string{"STUB_EMIT_INTERVAL_MS": "soon"}, wantErr: "STUB_EMIT_INTERVAL_MS"},
		{name: "non-numeric delay", env: map[string]string{"STUB_AUTO_RESOLVE_DELAY_MS": "1.5"}, wantErr: "STUB_AUTO_RESOLVE_DELAY_MS"},
		{
			name:    "every bad value is reported",
			env:     map[string]string{"STUB_EMIT_COUNT": "x", "STUB_ASSURANCE": "y"},
			wantErr: "STUB_ASSURANCE",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := ParseConfig(lookupFrom(tc.env))
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseConfig: %v", err)
			}
			tc.check(t, cfg)
		})
	}
}

func TestParseConfigReportsAllErrors(t *testing.T) {
	_, err := ParseConfig(lookupFrom(map[string]string{"STUB_EMIT_COUNT": "x", "STUB_ASSURANCE": "y"}))
	if err == nil || !strings.Contains(err.Error(), "STUB_EMIT_COUNT") || !strings.Contains(err.Error(), "STUB_ASSURANCE") {
		t.Fatalf("err = %v, want both variables named", err)
	}
}
