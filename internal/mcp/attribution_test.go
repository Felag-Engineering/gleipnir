package mcp

import (
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"

	"golang.org/x/net/http/httpguts"
)

func TestValidateRunAttribution(t *testing.T) {
	tests := []struct {
		name             string
		cfg              RunAttributionConfig
		authHeaderNames  []string
		wantErrSubstring string // "" means no error
		want             RunAttributionConfig
	}{
		{
			name: "relay ok",
			cfg:  RunAttributionConfig{Mode: RunAttributionRelay},
			want: RunAttributionConfig{Mode: RunAttributionRelay},
		},
		{
			name: "off ok, names ignored not rejected",
			cfg:  RunAttributionConfig{Mode: RunAttributionOff, OnBehalfOfHeader: "X-Junk"},
			want: RunAttributionConfig{Mode: RunAttributionOff},
		},
		{
			name: "relay ok, supplied names ignored not rejected",
			cfg:  RunAttributionConfig{Mode: RunAttributionRelay, OnBehalfOfHeader: "X-Junk", SessionRefHeader: "X-Junk-2"},
			want: RunAttributionConfig{Mode: RunAttributionRelay},
		},
		{
			name: "custom with one name ok",
			cfg:  RunAttributionConfig{Mode: RunAttributionCustom, OnBehalfOfHeader: "X-Actor"},
			want: RunAttributionConfig{Mode: RunAttributionCustom, OnBehalfOfHeader: "X-Actor"},
		},
		{
			name: "custom with three names ok, whitespace trimmed",
			cfg: RunAttributionConfig{
				Mode:              RunAttributionCustom,
				OnBehalfOfHeader:  "  X-Actor  ",
				SessionRefHeader:  "X-Session",
				TraceparentHeader: "traceparent",
			},
			want: RunAttributionConfig{
				Mode:              RunAttributionCustom,
				OnBehalfOfHeader:  "X-Actor",
				SessionRefHeader:  "X-Session",
				TraceparentHeader: "traceparent",
			},
		},
		{
			name:             `unknown mode "Off" (wrong case) is rejected, not case-insensitively matched`,
			cfg:              RunAttributionConfig{Mode: "Off"},
			wantErrSubstring: "unknown mode",
		},
		{
			name:             `unknown mode "RELAY" (wrong case) is rejected, not case-insensitively matched`,
			cfg:              RunAttributionConfig{Mode: "RELAY"},
			wantErrSubstring: "unknown mode",
		},
		{
			name:             "empty mode is rejected",
			cfg:              RunAttributionConfig{Mode: ""},
			wantErrSubstring: "unknown mode",
		},
		{
			name:             "custom with no names is rejected",
			cfg:              RunAttributionConfig{Mode: RunAttributionCustom},
			wantErrSubstring: "at least one header name",
		},
		{
			name:             "reserved name Mcp-Session-Id is rejected",
			cfg:              RunAttributionConfig{Mode: RunAttributionCustom, OnBehalfOfHeader: "Mcp-Session-Id"},
			wantErrSubstring: "reserved",
		},
		{
			name:             "reserved name mcp-method (case-insensitive) is rejected",
			cfg:              RunAttributionConfig{Mode: RunAttributionCustom, OnBehalfOfHeader: "mcp-method"},
			wantErrSubstring: "reserved",
		},
		{
			name:             "reserved name Content-Type is rejected",
			cfg:              RunAttributionConfig{Mode: RunAttributionCustom, OnBehalfOfHeader: "Content-Type"},
			wantErrSubstring: "reserved",
		},
		{
			name:             "reserved name Host is rejected",
			cfg:              RunAttributionConfig{Mode: RunAttributionCustom, OnBehalfOfHeader: "Host"},
			wantErrSubstring: "reserved",
		},
		{
			name:             "CRLF in a name is rejected",
			cfg:              RunAttributionConfig{Mode: RunAttributionCustom, OnBehalfOfHeader: "X-A\r\nX-Injected: 1"},
			wantErrSubstring: "invalid characters",
		},
		{
			name:             "space in a name is rejected",
			cfg:              RunAttributionConfig{Mode: RunAttributionCustom, OnBehalfOfHeader: "X A"},
			wantErrSubstring: "invalid characters",
		},
		{
			name:             "colon in a name is rejected",
			cfg:              RunAttributionConfig{Mode: RunAttributionCustom, OnBehalfOfHeader: "X:A"},
			wantErrSubstring: "invalid characters",
		},
		{
			name:             "underscore twin is rejected outright",
			cfg:              RunAttributionConfig{Mode: RunAttributionCustom, OnBehalfOfHeader: "X_Actor"},
			wantErrSubstring: "letters, digits",
		},
		{
			name:             "dot twin is rejected outright",
			cfg:              RunAttributionConfig{Mode: RunAttributionCustom, OnBehalfOfHeader: "X.Actor"},
			wantErrSubstring: "letters, digits",
		},
		{
			name:             "denied name Authorization is rejected",
			cfg:              RunAttributionConfig{Mode: RunAttributionCustom, OnBehalfOfHeader: "Authorization"},
			wantErrSubstring: "not permitted for run attribution",
		},
		{
			name:             "denied name Cookie is rejected",
			cfg:              RunAttributionConfig{Mode: RunAttributionCustom, OnBehalfOfHeader: "Cookie"},
			wantErrSubstring: "not permitted for run attribution",
		},
		{
			name:             "denied name X-Forwarded-For is rejected",
			cfg:              RunAttributionConfig{Mode: RunAttributionCustom, OnBehalfOfHeader: "X-Forwarded-For"},
			wantErrSubstring: "not permitted for run attribution",
		},
		{
			name:             "denied name User-Agent is rejected",
			cfg:              RunAttributionConfig{Mode: RunAttributionCustom, OnBehalfOfHeader: "User-Agent"},
			wantErrSubstring: "not permitted for run attribution",
		},
		{
			name:             "denied name Connection is rejected",
			cfg:              RunAttributionConfig{Mode: RunAttributionCustom, OnBehalfOfHeader: "Connection"},
			wantErrSubstring: "not permitted for run attribution",
		},
		{
			name: "duplicate names across fields, differing case, is rejected",
			cfg: RunAttributionConfig{
				Mode:             RunAttributionCustom,
				OnBehalfOfHeader: "X-Actor",
				SessionRefHeader: "x-actor",
			},
			wantErrSubstring: "must be distinct",
		},
		{
			name:             "collision with an auth header, differing case, is rejected",
			cfg:              RunAttributionConfig{Mode: RunAttributionCustom, OnBehalfOfHeader: "x-api-key"},
			authHeaderNames:  []string{"X-Api-Key"},
			wantErrSubstring: "collides with this server's auth headers",
		},
		{
			name:             "relay preset colliding with an auth header named traceparent is rejected (security review: relay is checked too, not only client build)",
			cfg:              RunAttributionConfig{Mode: RunAttributionRelay},
			authHeaderNames:  []string{"traceparent"},
			wantErrSubstring: "collides with this server's auth headers",
		},
		{
			name:             "relay preset colliding with an auth header, differing case, is rejected",
			cfg:              RunAttributionConfig{Mode: RunAttributionRelay},
			authHeaderNames:  []string{"TRACEPARENT"},
			wantErrSubstring: "collides with this server's auth headers",
		},
		{
			name:             "relay preset colliding with an auth header named X-Relay-Session-Ref is rejected",
			cfg:              RunAttributionConfig{Mode: RunAttributionRelay},
			authHeaderNames:  []string{"X-Relay-Session-Ref"},
			wantErrSubstring: "collides with this server's auth headers",
		},
		{
			name:            "relay preset with an unrelated auth header is unaffected",
			cfg:             RunAttributionConfig{Mode: RunAttributionRelay},
			authHeaderNames: []string{"X-Api-Key"},
			want:            RunAttributionConfig{Mode: RunAttributionRelay},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ValidateRunAttribution(tc.cfg, tc.authHeaderNames)
			if tc.wantErrSubstring != "" {
				if err == nil {
					t.Fatalf("ValidateRunAttribution: want error containing %q, got nil (result %+v)", tc.wantErrSubstring, got)
				}
				if !strings.Contains(err.Error(), tc.wantErrSubstring) {
					t.Fatalf("ValidateRunAttribution error = %q, want substring %q", err.Error(), tc.wantErrSubstring)
				}
				return
			}
			if err != nil {
				t.Fatalf("ValidateRunAttribution: unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("ValidateRunAttribution = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestParseRunAttributionColumn(t *testing.T) {
	tests := []struct {
		name    string
		stored  *string
		want    RunAttributionConfig
		wantErr bool
	}{
		{name: "nil is off", stored: nil, want: RunAttributionConfig{Mode: RunAttributionOff}},
		{name: "empty string is off", stored: strPtr(""), want: RunAttributionConfig{Mode: RunAttributionOff}},
		{name: "whitespace is off", stored: strPtr("   \t\n"), want: RunAttributionConfig{Mode: RunAttributionOff}},
		{name: "relay", stored: strPtr(`{"mode":"relay"}`), want: RunAttributionConfig{Mode: RunAttributionRelay}},
		{
			name:   "custom with all three names",
			stored: strPtr(`{"mode":"custom","on_behalf_of_header":"X-A","session_ref_header":"X-S","traceparent_header":"traceparent"}`),
			want: RunAttributionConfig{
				Mode:              RunAttributionCustom,
				OnBehalfOfHeader:  "X-A",
				SessionRefHeader:  "X-S",
				TraceparentHeader: "traceparent",
			},
		},
		{name: "malformed JSON errors", stored: strPtr(`{"mode":`), wantErr: true},
		{name: "unknown mode errors", stored: strPtr(`{"mode":"bogus"}`), wantErr: true},
		{name: "unknown field errors", stored: strPtr(`{"mode":"relay","surprise":"field"}`), wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseRunAttributionColumn(tc.stored)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ParseRunAttributionColumn: want error, got %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseRunAttributionColumn: unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("ParseRunAttributionColumn = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// TestRunAttributionColumnRoundTrip pins that Column() always produces the
// canonical stored form: off marshals to nil, relay drops any stray names
// down to the bare {"mode":"relay"}, and re-parsing Column()'s output always
// reproduces the same effective header names.
func TestRunAttributionColumnRoundTrip(t *testing.T) {
	tests := []struct {
		name       string
		cfg        RunAttributionConfig
		wantColumn *string
	}{
		{name: "off marshals to nil", cfg: RunAttributionConfig{Mode: RunAttributionOff}, wantColumn: nil},
		{
			name:       "relay canonicalises to the bare object regardless of any stray fields",
			cfg:        RunAttributionConfig{Mode: RunAttributionRelay, OnBehalfOfHeader: "should be dropped"},
			wantColumn: strPtr(`{"mode":"relay"}`),
		},
		{
			name:       "custom marshals every field",
			cfg:        RunAttributionConfig{Mode: RunAttributionCustom, OnBehalfOfHeader: "X-A"},
			wantColumn: strPtr(`{"mode":"custom","on_behalf_of_header":"X-A"}`),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.cfg.Column()
			if err != nil {
				t.Fatalf("Column: unexpected error: %v", err)
			}
			if (got == nil) != (tc.wantColumn == nil) {
				t.Fatalf("Column() = %v, want %v", got, tc.wantColumn)
			}
			if got != nil && *got != *tc.wantColumn {
				t.Fatalf("Column() = %q, want %q", *got, *tc.wantColumn)
			}

			reparsed, err := ParseRunAttributionColumn(got)
			if err != nil {
				t.Fatalf("ParseRunAttributionColumn(Column()): unexpected error: %v", err)
			}
			if reparsed.EffectiveHeaderNames() != tc.cfg.EffectiveHeaderNames() {
				t.Fatalf("round trip changed effective header names: got %+v, want %+v",
					reparsed.EffectiveHeaderNames(), tc.cfg.EffectiveHeaderNames())
			}
		})
	}
}

func TestAttributionNamesCollide(t *testing.T) {
	relay := `{"mode":"relay"}`
	custom := `{"mode":"custom","on_behalf_of_header":"X-Actor"}`

	tests := []struct {
		name       string
		stored     *string
		headerName string
		want       bool
	}{
		{name: "nil stored never collides", stored: nil, headerName: "X-Relay-On-Behalf-Of", want: false},
		{name: "off never collides", stored: strPtr(`{"mode":"off"}`), headerName: "X-Relay-On-Behalf-Of", want: false},
		{name: "relay preset name collides", stored: &relay, headerName: "traceparent", want: true},
		{name: "relay preset name, different case, still collides", stored: &relay, headerName: "TRACEPARENT", want: true},
		{name: "relay: unrelated name does not collide", stored: &relay, headerName: "X-Api-Key", want: false},
		{name: "custom name collides", stored: &custom, headerName: "x-actor", want: true},
		{name: "custom: unrelated name does not collide", stored: &custom, headerName: "X-Session", want: false},
		{name: "unparseable stored value never collides", stored: strPtr("{not json"), headerName: "traceparent", want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := AttributionNamesCollide(tc.stored, tc.headerName)
			if err != nil {
				t.Fatalf("AttributionNamesCollide: unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("AttributionNamesCollide(%v, %q) = %v, want %v", tc.stored, tc.headerName, got, tc.want)
			}
		})
	}
}

// relayAssertedRule reproduces Relay's audit.ValidateAsserted rule locally
// (relay/internal/audit/attribution.go): every field must be at most 256
// bytes and contain no C0/C1 control character or DEL. Used as a property
// check against every sanitizeAttributionValue output.
func relayAssertedRule(s string) error {
	if len(s) > maxAttributionValueBytes {
		return errTooLong
	}
	if strings.IndexFunc(s, isAttributionControlRune) >= 0 {
		return errHasControl
	}
	return nil
}

var (
	errTooLong    = &sanitizeCheckError{"too long"}
	errHasControl = &sanitizeCheckError{"has control character"}
)

type sanitizeCheckError struct{ msg string }

func (e *sanitizeCheckError) Error() string { return e.msg }

func TestSanitizeAttributionValue(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "tab becomes space", in: "a\tb", want: "a b"},
		{name: "CR becomes space", in: "a\rb", want: "a b"},
		{name: "LF becomes space", in: "a\nb", want: "a b"},
		{name: "NUL becomes space", in: "a\x00b", want: "a b"},
		{name: "DEL becomes space", in: "a\x7fb", want: "a b"},
		{name: "U+0085 (NEL, C1) becomes space", in: "a\u0085b", want: "a b"},
		{name: "U+009F (C1) becomes space", in: "a\u009fb", want: "a b"},
		{name: "invalid UTF-8 becomes U+FFFD", in: "a\xffb", want: "a�b"},
		{name: "printable non-ASCII is kept", in: "José", want: "José"},
		{name: "leading/trailing whitespace trimmed", in: "  hello  ", want: "hello"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := sanitizeAttributionValue(tc.in)
			if got != tc.want {
				t.Fatalf("sanitizeAttributionValue(%q) = %q, want %q", tc.in, got, tc.want)
			}
			assertPassesRelayRule(t, got)
		})
	}

	t.Run("a 300-byte ASCII string becomes exactly 256 bytes", func(t *testing.T) {
		in := strings.Repeat("a", 300)
		got := sanitizeAttributionValue(in)
		if len(got) != maxAttributionValueBytes {
			t.Fatalf("len(sanitizeAttributionValue(300 'a's)) = %d, want %d", len(got), maxAttributionValueBytes)
		}
		assertPassesRelayRule(t, got)
	})

	t.Run("a multibyte string straddling byte 256 is cut on a rune boundary", func(t *testing.T) {
		// Each "é" is 2 bytes (U+00E9), so 200 of them is 400 bytes and byte
		// 256 falls in the middle of a rune.
		in := strings.Repeat("é", 200)
		got := sanitizeAttributionValue(in)
		if !utf8.ValidString(got) {
			t.Fatalf("sanitizeAttributionValue produced invalid UTF-8: %q", got)
		}
		if len(got) > maxAttributionValueBytes {
			t.Fatalf("len(sanitizeAttributionValue(...)) = %d, want <= %d", len(got), maxAttributionValueBytes)
		}
		assertPassesRelayRule(t, got)
	})
}

// assertPassesRelayRule is the property check every sanitizeAttributionValue
// output must satisfy: Relay's own asserted-field rule, reproduced locally,
// and net/http's outbound header-value validity check.
func assertPassesRelayRule(t *testing.T, s string) {
	t.Helper()
	if err := relayAssertedRule(s); err != nil {
		t.Errorf("sanitizeAttributionValue output %q fails Relay's asserted-field rule: %v", s, err)
	}
	if !httpguts.ValidHeaderFieldValue(s) {
		t.Errorf("sanitizeAttributionValue output %q is not a valid HTTP header field value", s)
	}
}

func TestOnBehalfOfValue(t *testing.T) {
	tests := []struct {
		name string
		a    RunAttribution
		want string
	}{
		{
			name: "agent name alone",
			a:    RunAttribution{AgentName: "fleet-responder", RunID: "r1"},
			want: "fleet-responder",
		},
		{
			name: "agent name plus triggering user",
			a:    RunAttribution{AgentName: "fleet-responder", RunID: "r1", TriggeredBy: "alice"},
			want: "fleet-responder (triggered by alice)",
		},
		{
			name: "empty triggered-by omits the parenthetical",
			a:    RunAttribution{AgentName: "fleet-reader", RunID: "r1", TriggeredBy: ""},
			want: "fleet-reader",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := onBehalfOfValue(tc.a)
			if got != tc.want {
				t.Fatalf("onBehalfOfValue(%+v) = %q, want %q", tc.a, got, tc.want)
			}
		})
	}
}

func TestSessionRefValue(t *testing.T) {
	tests := []struct {
		name string
		a    RunAttribution
		want string
	}{
		{
			name: "run ID alone, no public URL",
			a:    RunAttribution{RunID: "01J000"},
			want: "gleipnir run 01J000",
		},
		{
			name: "run ID plus public URL",
			a:    RunAttribution{RunID: "01J000", PublicURL: "https://g.example"},
			want: "gleipnir run 01J000 (https://g.example/runs/01J000)",
		},
		{
			name: "trailing slash on public URL is normalised",
			a:    RunAttribution{RunID: "01J000", PublicURL: "https://g.example/"},
			want: "gleipnir run 01J000 (https://g.example/runs/01J000)",
		},
		{
			name: "an overlong URL is dropped entirely, run ID stays the join key",
			a:    RunAttribution{RunID: "01J000", PublicURL: "https://g.example/" + strings.Repeat("x", 300)},
			want: "gleipnir run 01J000",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := sessionRefValue(tc.a)
			if got != tc.want {
				t.Fatalf("sessionRefValue(%+v) = %q, want %q", tc.a, got, tc.want)
			}
			assertPassesRelayRule(t, got)
		})
	}
}

var traceparentPattern = regexp.MustCompile(`^00-[0-9a-f]{32}-[0-9a-f]{16}-01$`)

func TestTraceparentValue(t *testing.T) {
	span := [8]byte{1, 2, 3, 4, 5, 6, 7, 8}

	got := traceparentValue("r1", span)
	if !traceparentPattern.MatchString(got) {
		t.Fatalf("traceparentValue(%q) = %q, does not match %s", "r1", got, traceparentPattern)
	}
	if !strings.Contains(got, "0102030405060708") {
		t.Fatalf("traceparentValue(%q) = %q, want the pinned span-id verbatim", "r1", got)
	}

	t.Run("same run gives the same trace-id", func(t *testing.T) {
		a := traceparentValue("r1", span)
		b := traceparentValue("r1", [8]byte{9, 9, 9, 9, 9, 9, 9, 9})
		traceIDOf := func(s string) string { return strings.Split(s, "-")[1] }
		if traceIDOf(a) != traceIDOf(b) {
			t.Fatalf("same run produced different trace-ids: %q vs %q", traceIDOf(a), traceIDOf(b))
		}
	})

	t.Run("different runs give different trace-ids", func(t *testing.T) {
		a := traceIDFor("r1")
		b := traceIDFor("r2")
		if a == b {
			t.Fatalf("different runs produced the same trace-id %q", a)
		}
	})

	t.Run("trace-id is never all zero", func(t *testing.T) {
		id := traceIDFor("r1")
		if id == strings.Repeat("0", 32) {
			t.Fatal("traceIDFor produced an all-zero trace-id, which W3C forbids")
		}
	})
}

func TestBuildAttributionHeaders(t *testing.T) {
	names := AttributionHeaderNames{OnBehalfOf: RelayOnBehalfOfHeader, SessionRef: RelaySessionRefHeader, Traceparent: RelayTraceparentHeader}
	a := RunAttribution{AgentName: "fleet-responder", RunID: "r1"}
	span := [8]byte{1, 2, 3, 4, 5, 6, 7, 8}

	t.Run("zero names sends nothing", func(t *testing.T) {
		if got := buildAttributionHeaders(AttributionHeaderNames{}, a, span); got != nil {
			t.Fatalf("buildAttributionHeaders with zero names = %+v, want nil", got)
		}
	})

	t.Run("zero attribution sends nothing even with configured names", func(t *testing.T) {
		if got := buildAttributionHeaders(names, RunAttribution{}, span); got != nil {
			t.Fatalf("buildAttributionHeaders with zero attribution = %+v, want nil", got)
		}
	})

	t.Run("configured names and a real run produce all three headers", func(t *testing.T) {
		got := buildAttributionHeaders(names, a, span)
		if len(got) != 3 {
			t.Fatalf("buildAttributionHeaders = %+v, want 3 headers", got)
		}
	})

	t.Run("a partial custom config sends only the configured names", func(t *testing.T) {
		got := buildAttributionHeaders(AttributionHeaderNames{OnBehalfOf: "X-Actor"}, a, span)
		if len(got) != 1 || got[0].Name != "X-Actor" {
			t.Fatalf("buildAttributionHeaders = %+v, want exactly one X-Actor header", got)
		}
	})
}
