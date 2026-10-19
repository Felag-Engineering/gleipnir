package binding_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/felag-engineering/gleipnir/internal/plugin/binding"
	"gopkg.in/yaml.v3"
)

// pluginManifests are the v2 plugin manifests that first-party plugin modules
// ship. The root module cannot import a plugin's package main, so this test
// reads each manifest from disk and holds its declared event kinds to the host
// evaluator: a binding field the evaluator cannot compile, or one that does not
// behave as the manifest attests, would otherwise surface only when an operator
// bound a policy to it.
const pluginManifests = "../../../plugins/*/manifest.yaml"

type manifestEventKind struct {
	Kind          string              `yaml:"kind"`
	BindingSchema yaml.Node           `yaml:"binding_schema"`
	Operators     map[string][]string `yaml:"operators"`
}

type v2Manifest struct {
	SchemaVersion string `yaml:"schema_version"`
	Gleipnir      struct {
		EventKinds []manifestEventKind `yaml:"event_kinds"`
	} `yaml:"gleipnir"`
}

func TestPluginEventKindBindingFieldsCompile(t *testing.T) {
	paths, err := filepath.Glob(pluginManifests)
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	checked := 0
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		var m v2Manifest
		if err := yaml.Unmarshal(raw, &m); err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		if m.SchemaVersion != "2" || len(m.Gleipnir.EventKinds) == 0 {
			continue
		}
		for _, kind := range m.Gleipnir.EventKinds {
			t.Run(filepath.Base(filepath.Dir(path))+"/"+kind.Kind, func(t *testing.T) {
				checked += checkKind(t, kind)
			})
		}
	}
	if checked == 0 {
		t.Skip("no v2 plugin manifest declares event kinds")
	}
}

// checkKind compiles and exercises every binding field of one kind, and
// returns how many it checked.
func checkKind(t *testing.T, kind manifestEventKind) int {
	t.Helper()
	var schema struct {
		Properties map[string]struct {
			Type   string `yaml:"type"`
			Format string `yaml:"format"`
		} `yaml:"properties"`
	}
	if err := kind.BindingSchema.Decode(&schema); err != nil {
		t.Fatalf("decode binding_schema: %v", err)
	}
	if len(schema.Properties) == 0 {
		t.Fatalf("kind %s declares no binding fields", kind.Kind)
	}

	for name, prop := range schema.Properties {
		// mention_only is reserved by the evaluator: its value is checked
		// against a different payload field, so a plugin field of that name
		// would silently not mean what its schema says (spec section 5).
		if name == "mention_only" {
			t.Errorf("field %q uses a name the evaluator reserves", name)
			continue
		}

		// A matching and a non-matching value, and the operator the schema
		// shape selects for them.
		var bindValue, hit, miss any
		var wantOp string
		switch {
		case prop.Type == "boolean":
			bindValue, hit, miss, wantOp = true, true, false, "equals"
		case prop.Type == "string" && prop.Format == "contains":
			bindValue, hit, miss, wantOp = "abc", "xxabcxx", "xyz", "contains"
		case prop.Type == "string" && prop.Format == "regex":
			bindValue, hit, miss, wantOp = "^a.c$", "abc", "abd", "regex"
		case prop.Type == "string" && prop.Format == "":
			bindValue, hit, miss, wantOp = "abc", "abc", "abcd", "equals"
		default:
			t.Errorf("field %q has type %q format %q, which no operator handles", name, prop.Type, prop.Format)
			continue
		}

		if ops := kind.Operators[name]; len(ops) != 1 || ops[0] != wantOp {
			t.Errorf("field %q: manifest attests operators %v, but its schema shape evaluates as %q", name, ops, wantOp)
		}

		cb, err := binding.Compile(map[string]any{name: bindValue}, &kind.BindingSchema)
		if err != nil {
			t.Errorf("field %q does not compile: %v", name, err)
			continue
		}
		if ok, _ := cb.Evaluate(map[string]any{name: hit}); !ok {
			t.Errorf("field %q: binding %v did not match payload %v", name, bindValue, hit)
		}
		if ok, _ := cb.Evaluate(map[string]any{name: miss}); ok {
			t.Errorf("field %q: binding %v wrongly matched payload %v", name, bindValue, miss)
		}
	}

	// Every field together must also compile: a policy binds several at once.
	all := map[string]any{}
	for name, prop := range schema.Properties {
		switch {
		case prop.Type == "boolean":
			all[name] = true
		case prop.Format == "regex":
			all[name] = "^a"
		default:
			all[name] = "a"
		}
	}
	if _, err := binding.Compile(all, &kind.BindingSchema); err != nil {
		t.Errorf("all fields of %s together do not compile: %v", kind.Kind, err)
	}
	for name := range kind.Operators {
		if _, declared := schema.Properties[name]; !declared {
			t.Errorf("operators name %q, which is not a binding field", name)
		}
	}
	return len(schema.Properties)
}
