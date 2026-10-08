package contract_test

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// numericLiteralSchema carries numeric literals that a float64 round-trip
// would rewrite (1.500 -> 1.5, 1e-21 form, 1e+31) or reject (1e400). The
// literals below must reach the wire byte-for-byte (#1064).
const numericLiteralSchema = `{"type":"object","properties":{"n":{"type":"number",` +
	`"minimum":1.500,"multipleOf":0.000000000000000000001,` +
	`"maximum":10000000000000000000000000000001,"exclusiveMaximum":1e400}}}`

var numericLiteralsByKeyword = map[string]string{
	"minimum":          "1.500",
	"multipleOf":       "0.000000000000000000001",
	"maximum":          "10000000000000000000000000000001",
	"exclusiveMaximum": "1e400",
}

var numericLiterals = []string{
	"1.500",
	"0.000000000000000000001",
	"10000000000000000000000000000001",
	"1e400",
}

// TestContract_SchemaNumericLiteralsVerbatim asserts that the three
// full-support wires serialize tool-schema numbers exactly as the canonical
// schema spelled them, so the model sees the same bounds the runtime enforces.
func TestContract_SchemaNumericLiteralsVerbatim(t *testing.T) {
	for _, p := range fullSupportProbes {
		t.Run(p.name, func(t *testing.T) {
			var lastReq string
			srv := captureServer(t, p.respBody, &lastReq)
			client := p.makeClient(t, srv)

			if _, err := client.CreateMessage(context.Background(), requestWithTool(json.RawMessage(numericLiteralSchema))); err != nil {
				t.Fatalf("CreateMessage: %v", err)
			}
			// The ":" prefix and trailing delimiter pin the literal as a bare
			// JSON number: some SDK encoders emit a json.Number as a quoted string.
			for key, lit := range numericLiteralsByKeyword {
				bare := `"` + key + `":` + lit
				if !strings.Contains(lastReq, bare+",") && !strings.Contains(lastReq, bare+"}") {
					t.Errorf("request body lost bare numeric literal %s; body=%s", bare, lastReq)
				}
			}
		})
	}
}

// TestContract_SchemaNumericLiteralsGoogle pins what the Google wire can
// carry. genai.Schema is built from type/description/enum/required/
// properties/items only, so minimum/maximum/multipleOf never reach Gemini
// (pre-existing, wire-local). Enum members are the one place numbers survive,
// as strings, and must keep their literal spelling; a 1e400 must not fail the
// request build.
func TestContract_SchemaNumericLiteralsGoogle(t *testing.T) {
	schema := `{"type":"object","properties":{"n":{"type":"number","enum":[1.500,0.000000000000000000001,10000000000000000000000000000001,1e400],"maximum":1e400}}}`
	gen := &fakeGenerator{response: googleTextResponse()}
	client := newGoogleClient(gen)

	if _, err := client.CreateMessage(context.Background(), requestWithTool(json.RawMessage(schema))); err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}
	decls := gen.lastConfig.Tools[0].FunctionDeclarations
	got := decls[0].Parameters.Properties["n"].Enum
	if !reflect.DeepEqual(got, numericLiterals) {
		t.Errorf("enum = %v, want %v", got, numericLiterals)
	}
}
