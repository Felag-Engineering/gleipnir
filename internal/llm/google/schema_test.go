package google

import (
	"reflect"
	"strings"
	"testing"

	"google.golang.org/genai"
)

func TestTranslateJSONSchemaToGenaiSchema(t *testing.T) {
	tests := []struct {
		name    string
		input   map[string]any
		want    *genai.Schema
		wantErr string
	}{
		{
			name:  "string type",
			input: map[string]any{"type": "string"},
			want:  &genai.Schema{Type: genai.TypeString},
		},
		{
			name:  "integer type",
			input: map[string]any{"type": "integer"},
			want:  &genai.Schema{Type: genai.TypeInteger},
		},
		{
			name:  "number type",
			input: map[string]any{"type": "number"},
			want:  &genai.Schema{Type: genai.TypeNumber},
		},
		{
			name:  "boolean type",
			input: map[string]any{"type": "boolean"},
			want:  &genai.Schema{Type: genai.TypeBoolean},
		},
		{
			name:  "string with description",
			input: map[string]any{"type": "string", "description": "A namespace name"},
			want:  &genai.Schema{Type: genai.TypeString, Description: "A namespace name"},
		},
		{
			name:  "enum single value",
			input: map[string]any{"type": "string", "enum": []any{"worker-01"}},
			want: &genai.Schema{
				Type:   genai.TypeString,
				Enum:   []string{"worker-01"},
				Format: "enum",
			},
		},
		{
			name:  "enum multiple values",
			input: map[string]any{"type": "string", "enum": []any{"worker-01", "worker-02", "worker-03"}},
			want: &genai.Schema{
				Type:   genai.TypeString,
				Enum:   []string{"worker-01", "worker-02", "worker-03"},
				Format: "enum",
			},
		},
		{
			name: "object with properties and required",
			input: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"namespace": map[string]any{"type": "string"},
					"force":     map[string]any{"type": "boolean"},
				},
				"required": []any{"namespace"},
			},
			want: &genai.Schema{
				Type: genai.TypeObject,
				Properties: map[string]*genai.Schema{
					"namespace": {Type: genai.TypeString},
					"force":     {Type: genai.TypeBoolean},
				},
				Required: []string{"namespace"},
			},
		},
		{
			name:  "object with no properties",
			input: map[string]any{"type": "object"},
			want:  &genai.Schema{Type: genai.TypeObject},
		},
		{
			name: "nested object",
			input: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"metadata": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"name": map[string]any{"type": "string"},
						},
					},
				},
			},
			want: &genai.Schema{
				Type: genai.TypeObject,
				Properties: map[string]*genai.Schema{
					"metadata": {
						Type: genai.TypeObject,
						Properties: map[string]*genai.Schema{
							"name": {Type: genai.TypeString},
						},
					},
				},
			},
		},
		{
			name: "array with string items",
			input: map[string]any{
				"type":  "array",
				"items": map[string]any{"type": "string"},
			},
			want: &genai.Schema{
				Type:  genai.TypeArray,
				Items: &genai.Schema{Type: genai.TypeString},
			},
		},
		{
			name: "array with object items",
			input: map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"name": map[string]any{"type": "string"},
					},
					"required": []any{"name"},
				},
			},
			want: &genai.Schema{
				Type: genai.TypeArray,
				Items: &genai.Schema{
					Type: genai.TypeObject,
					Properties: map[string]*genai.Schema{
						"name": {Type: genai.TypeString},
					},
					Required: []string{"name"},
				},
			},
		},
		{
			name: "enum-constrained object (ADR-017 parameter scoping)",
			input: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"namespace": map[string]any{
						"type": "string",
						"enum": []any{"worker-01", "worker-02"},
					},
					"pod": map[string]any{"type": "string"},
				},
				"required": []any{"namespace", "pod"},
			},
			want: &genai.Schema{
				Type: genai.TypeObject,
				Properties: map[string]*genai.Schema{
					"namespace": {
						Type:   genai.TypeString,
						Enum:   []string{"worker-01", "worker-02"},
						Format: "enum",
					},
					"pod": {Type: genai.TypeString},
				},
				Required: []string{"namespace", "pod"},
			},
		},
		{
			name:    "error: missing type field",
			input:   map[string]any{"description": "no type"},
			wantErr: `missing required "type"`,
		},
		{
			name:    "error: non-string type field",
			input:   map[string]any{"type": []any{"string", "null"}},
			wantErr: "must be a string",
		},
		{
			name:    "error: non-string type field includes Go type",
			input:   map[string]any{"type": []any{"string", "null"}},
			wantErr: "[]interface",
		},
		{
			name:    "error: unsupported type",
			input:   map[string]any{"type": "null"},
			wantErr: "unsupported",
		},
		{
			name:    "error: array missing items",
			input:   map[string]any{"type": "array"},
			wantErr: "items",
		},
		{
			name:    "error: properties not a map",
			input:   map[string]any{"type": "object", "properties": "invalid"},
			wantErr: `"properties" must be an object`,
		},
		{
			name:    "error: array items not a map",
			input:   map[string]any{"type": "array", "items": "string"},
			wantErr: "array items: value is not an object",
		},
		{
			name: "unknown properties ignored gracefully",
			input: map[string]any{
				"type":                 "string",
				"additionalProperties": false,
				"$schema":              "http://json-schema.org/draft-07/schema#",
			},
			want: &genai.Schema{Type: genai.TypeString},
		},
		{
			// Real-world MCP schema: kubectl get_pods style tool with multiple field types.
			name: "real-world MCP schema: kubectl get_pods",
			input: map[string]any{
				"type":        "object",
				"description": "List pods in a Kubernetes cluster",
				"properties": map[string]any{
					"namespace": map[string]any{
						"type":        "string",
						"description": "Kubernetes namespace to query",
					},
					"all_namespaces": map[string]any{
						"type":        "boolean",
						"description": "List pods across all namespaces",
					},
					"label_selector": map[string]any{
						"type":        "string",
						"description": "Filter by label selector",
					},
					"output": map[string]any{
						"type": "string",
						"enum": []any{"json", "yaml", "wide"},
					},
				},
				"required":             []any{"namespace"},
				"additionalProperties": false,
			},
			want: &genai.Schema{
				Type:        genai.TypeObject,
				Description: "List pods in a Kubernetes cluster",
				Properties: map[string]*genai.Schema{
					"namespace": {
						Type:        genai.TypeString,
						Description: "Kubernetes namespace to query",
					},
					"all_namespaces": {
						Type:        genai.TypeBoolean,
						Description: "List pods across all namespaces",
					},
					"label_selector": {
						Type:        genai.TypeString,
						Description: "Filter by label selector",
					},
					"output": {
						Type:   genai.TypeString,
						Enum:   []string{"json", "yaml", "wide"},
						Format: "enum",
					},
				},
				Required: []string{"namespace"},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := translateJSONSchemaToGenaiSchema(tc.input)

			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("error %q does not contain %q", err.Error(), tc.wantErr)
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("mismatch:\n  got:  %+v\n  want: %+v", got, tc.want)
			}
		})
	}
}
