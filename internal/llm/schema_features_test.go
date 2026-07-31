package llm

import (
	"reflect"
	"testing"
)

// TestFullSchemaSupport_AllFieldsTrue guards against the "added a field,
// forgot to set it in FullSchemaSupport" mistake, which would silently push
// every wire onto the restricted path.
func TestFullSchemaSupport_AllFieldsTrue(t *testing.T) {
	v := reflect.ValueOf(FullSchemaSupport())
	for i := 0; i < v.NumField(); i++ {
		field := v.Field(i)
		name := v.Type().Field(i).Name
		if field.Kind() != reflect.Bool {
			t.Fatalf("field %s is %s, want bool — SchemaFeatureSet must stay comparable", name, field.Kind())
		}
		if !field.Bool() {
			t.Errorf("field %s = false, want true in FullSchemaSupport()", name)
		}
	}
}

func TestSchemaFeatureSet_IsFull(t *testing.T) {
	cases := []struct {
		name string
		set  SchemaFeatureSet
		want bool
	}{
		{"full", FullSchemaSupport(), true},
		{"zero value", SchemaFeatureSet{}, false},
		{"OneOf false", withoutOneOf(), false},
		{"AnyOf false", withoutAnyOf(), false},
		{"AllOf false", withoutAllOf(), false},
		{"Not false", withoutNot(), false},
		{"Defs false", withoutDefs(), false},
		{"Const false", withoutConst(), false},
		{"Formats false", withoutFormats(), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.set.IsFull(); got != tc.want {
				t.Errorf("IsFull() = %v, want %v", got, tc.want)
			}
		})
	}
}

func withoutOneOf() SchemaFeatureSet   { f := FullSchemaSupport(); f.OneOf = false; return f }
func withoutAnyOf() SchemaFeatureSet   { f := FullSchemaSupport(); f.AnyOf = false; return f }
func withoutAllOf() SchemaFeatureSet   { f := FullSchemaSupport(); f.AllOf = false; return f }
func withoutNot() SchemaFeatureSet     { f := FullSchemaSupport(); f.Not = false; return f }
func withoutDefs() SchemaFeatureSet    { f := FullSchemaSupport(); f.Defs = false; return f }
func withoutConst() SchemaFeatureSet   { f := FullSchemaSupport(); f.Const = false; return f }
func withoutFormats() SchemaFeatureSet { f := FullSchemaSupport(); f.Formats = false; return f }
