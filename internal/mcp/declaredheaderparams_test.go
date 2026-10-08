package mcp

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestDeclaredHeaderParams(t *testing.T) {
	schema := func(props string) json.RawMessage {
		return json.RawMessage(`{"type":"object","properties":{` + props + `}}`)
	}
	tests := []struct {
		name        string
		schema      json.RawMessage
		authHeaders []AuthHeader
		attribution []string
		want        []HeaderParamDecl
		wantErr     bool
	}{
		{name: "no annotations", schema: schema(`"a":{"type":"string"}`)},
		{name: "empty schema", schema: nil},
		{
			name:   "sorted by property",
			schema: schema(`"tenant":{"type":"string","x-mcp-header":"X-Tenant-Id"},"region":{"type":"string","x-mcp-header":"X-Region"}`),
			want:   []HeaderParamDecl{{"region", "X-Region"}, {"tenant", "X-Tenant-Id"}},
		},
		{name: "denied name", schema: schema(`"a":{"x-mcp-header":"Authorization"}`), wantErr: true},
		{name: "non-allowlisted byte", schema: schema(`"a":{"x-mcp-header":"X_Api"}`), wantErr: true},
		{name: "non-string annotation", schema: schema(`"a":{"x-mcp-header":5}`), wantErr: true},
		{name: "duplicate", schema: schema(`"a":{"x-mcp-header":"X-A"},"b":{"x-mcp-header":"x-a"}`), wantErr: true},
		{
			name:        "collides with auth header",
			schema:      schema(`"a":{"x-mcp-header":"X-Api-Key"}`),
			authHeaders: []AuthHeader{{Name: "x-api-key", Value: "v"}},
			wantErr:     true,
		},
		{
			name:        "collides with attribution header",
			schema:      schema(`"a":{"x-mcp-header":"X-Run"}`),
			attribution: []string{"x-run"},
			wantErr:     true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := DeclaredHeaderParams(tt.schema, tt.authHeaders, tt.attribution)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %v, want %v", got, tt.want)
			}

			// The display must agree with the call path: a rejected
			// declaration set rejects every call, and an accepted one is
			// exactly what a call supplying every value would send.
			input := map[string]any{}
			for _, d := range tt.want {
				input[d.Property] = "v"
			}
			sent, extractErr := extractHeaderParams(tt.schema, input, tt.authHeaders, tt.attribution)
			if (extractErr != nil) != tt.wantErr {
				t.Fatalf("extractHeaderParams err = %v, wantErr %v", extractErr, tt.wantErr)
			}
			if len(sent) != len(tt.want) {
				t.Errorf("extractHeaderParams sent %d headers, display listed %d", len(sent), len(tt.want))
			}
		})
	}
}
