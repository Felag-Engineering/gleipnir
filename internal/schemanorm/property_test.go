package schemanorm_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"reflect"
	"strconv"
	"testing"

	"github.com/felag-engineering/gleipnir/internal/schemanorm"
	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

// propertyTestSeed is fixed, not derived from the current time or run
// index, so a failure reproduces: rerun this test and the exact same
// sequence of random schemas and probe instances is generated again. Print
// it on every failure (see the t.Fatalf calls below) so a CI failure log is
// self-contained -- a human does not need to go spelunking for the seed.
const propertyTestSeed = 735

const (
	propertyIterations     = 300
	probesPerSchema        = 6
	maxRandomSchemaDepth   = 3
	maxRandomInstanceDepth = 3
)

// TestNormalize_PropertyNoDisagreement is the harness the human merge
// authority mandated after three consecutive fixture-table failures: fixture
// tables missed every one of the six defects found across three review
// rounds, while a randomized raw-vs-normalized differential found two new
// classes within minutes. It generates a random JSON Schema document with
// object keys deliberately inserted in SHUFFLED (not alphabetical) order,
// and runs TWO independent oracles against it:
//
//  1. A DeepEqual oracle: decode(raw) and decode(Normalize(raw)) -- both
//     decoded with UseNumber, exactly as Normalize itself decodes -- must be
//     IDENTICAL Go values. This is a strictly stronger check than "no
//     validator disagrees": it catches a dropped or altered key/value
//     ANYWHERE in the document regardless of whether that key happens to be
//     one a validator enforces (an annotation-only keyword like "$schema" or
//     "$id" has zero effect on jsonschema/v6's Validate() result, so only
//     this oracle -- not the compile-and-validate oracle below -- would
//     notice it silently vanishing).
//  2. A validator-differential oracle: compile both the as-generated ("raw")
//     bytes and Normalize's output with santhosh-tekuri/jsonschema/v6, and
//     validate a batch of random probe instances against both. ANY
//     disagreement -- in EITHER direction, a narrowing is exactly as much a
//     bug as a widening now, because this package must be *exactly*
//     equivalent to its input, not merely no stricter -- fails the test.
//     This oracle is what would catch a genuine SEMANTIC change (e.g. a
//     keyword's effective scope moving) that nonetheless preserves the
//     decoded value shape; it is kept alongside the DeepEqual oracle rather
//     than replaced by it, because a byte-normalizer has no semantic
//     transformation logic left to have that kind of bug, but the harness
//     should not silently lose coverage of the failure mode three prior
//     review rounds actually found.
//
// The random schemas build their object members with an explicit,
// independently-shuffled key order (see orderedValue below) rather than as
// ordinary Go maps: encoding/json.Marshal on a map[string]any already sorts
// keys, which would make "raw" byte-identical to what Normalize produces
// before Normalize ever ran, and the property test would prove nothing. That
// premise is correct and load-bearing (confirmed independently: a map-based
// generator produces raw bytes byte-identical to Normalize's output; with
// this shuffled-order generator, the large majority of iterations produce
// raw bytes that genuinely differ from normalized). NOTE: the
// validator-differential oracle structurally CANNOT detect a member-order
// effect at all -- jsonschema/v6 ingests a schema via encoding/json's
// UnmarshalJSON into a map[string]any, which destroys member order before
// compilation even begins -- so it is the DeepEqual oracle above, not this
// one, that is actually exercising the "did normalization change anything"
// question for the common case (pure reordering) this package exists to
// perform safely.
//
// The generator (randomSchemaDocument and friends, below) intentionally
// covers the keywords three security reviews and a mutation-testing pass
// found under-exercised: $ref/$defs/definitions (with a guaranteed-resolvable
// pool built up front, so compilation never fails on a dangling reference),
// $schema/$id/$anchor, unevaluatedProperties/unevaluatedItems,
// not/if/then/else, const, patternProperties, allOf with a sibling keyword
// (the shape a reintroduced allOf-merge would target), non-ASCII and
// escaped-character property names, and exotic number literal spellings
// (stashed in "default", an annotation-only keyword, so they cannot affect
// the validator-differential oracle -- only the DeepEqual oracle needs them).
// deriveProbeFromSchema derives roughly half the probe instances from the
// generated schema's own shape so a meaningful fraction of probes actually
// validate, rather than the differential oracle mostly comparing two
// rejections of unrelated random junk.
func TestNormalize_PropertyNoDisagreement(t *testing.T) {
	r := rand.New(rand.NewSource(propertyTestSeed))
	t.Logf("property test seed: %d (rerun with this seed to reproduce any failure)", propertyTestSeed)

	for i := 0; i < propertyIterations; i++ {
		schema := randomSchemaDocument(r)
		raw, err := json.Marshal(schema)
		if err != nil {
			t.Fatalf("seed %d iter %d: marshal random schema: %v", propertyTestSeed, i, err)
		}

		normalized, err := schemanorm.Normalize(raw)
		if err != nil {
			// The generator only ever produces unique, valid-UTF-8 property
			// names and plain scalar values (see propertyNamePool / the
			// scalar generators below), so it can never trip a sharp edge; a
			// failure here means Normalize itself has a bug, not the
			// generator.
			t.Fatalf("seed %d iter %d: Normalize(%s): unexpected error: %v", propertyTestSeed, i, raw, err)
		}

		rawDecoded, err := decodeWithNumber(raw)
		if err != nil {
			t.Fatalf("seed %d iter %d: decode raw with UseNumber: %v", propertyTestSeed, i, err)
		}
		normDecoded, err := decodeWithNumber(normalized)
		if err != nil {
			t.Fatalf("seed %d iter %d: decode normalized with UseNumber: %v", propertyTestSeed, i, err)
		}
		if !reflect.DeepEqual(rawDecoded, normDecoded) {
			t.Fatalf(
				"seed %d iter %d: Normalize CHANGED THE DECODED VALUE\nraw:        %s\nnormalized: %s",
				propertyTestSeed, i, raw, normalized,
			)
		}

		rawSchema, err := compileSchema(fmt.Sprintf("mem://prop-raw-%d.json", i), raw)
		if err != nil {
			t.Fatalf("seed %d iter %d: raw schema failed to compile with jsonschema/v6: %v\nschema: %s", propertyTestSeed, i, err, raw)
		}
		normalizedSchema, err := compileSchema(fmt.Sprintf("mem://prop-norm-%d.json", i), normalized)
		if err != nil {
			t.Fatalf("seed %d iter %d: normalized schema failed to compile with jsonschema/v6: %v\nnormalized: %s", propertyTestSeed, i, err, normalized)
		}

		for p := 0; p < probesPerSchema; p++ {
			var instance any
			if p%2 == 0 {
				instance = deriveProbeFromSchema(r, schema, 0)
			} else {
				instance = randomInstance(r, 0)
			}

			rawErr := rawSchema.Validate(instance)
			normErr := normalizedSchema.Validate(instance)

			if (rawErr == nil) != (normErr == nil) {
				instanceJSON, _ := json.Marshal(instance)
				t.Fatalf(
					"seed %d iter %d probe %d: DISAGREEMENT between raw and normalized schema\nraw:        %s\nnormalized: %s\ninstance:   %s\nraw result:        %v\nnormalized result: %v",
					propertyTestSeed, i, p, raw, normalized, instanceJSON, rawErr, normErr,
				)
			}
		}
	}
}

// TestNormalize_Property_DuplicateKeyAlwaysRejected is a property-based
// regression for sharp edge 1 (see the package doc): across many randomly
// shaped schemas, injecting a duplicate object key at a randomly chosen
// depth must ALWAYS be rejected with ErrDuplicateKey, regardless of which
// keyword the duplicated key happens to be, how deep it sits, or what shape
// the rest of the schema takes. TestNormalize_DuplicateKeyRejected in
// schemanorm_test.go covers a handful of fixed positions; this is the same
// property swept across hundreds of randomly generated shapes.
func TestNormalize_Property_DuplicateKeyAlwaysRejected(t *testing.T) {
	seed := int64(propertyTestSeed + 1)
	r := rand.New(rand.NewSource(seed))
	t.Logf("property test seed: %d (rerun with this seed to reproduce any failure)", seed)

	injected := 0
	for i := 0; i < propertyIterations; i++ {
		schema := randomSchemaDocument(r)
		mutated, ok := injectDuplicateKey(r, schema)
		if !ok {
			continue // rare boolean-schema root ("true"/"false"): no object to duplicate a key in
		}
		injected++

		raw, err := json.Marshal(mutated)
		if err != nil {
			t.Fatalf("seed %d iter %d: marshal mutated schema: %v", seed, i, err)
		}

		_, err = schemanorm.Normalize(raw)
		if err == nil {
			t.Fatalf("seed %d iter %d: Normalize(%s): expected ErrDuplicateKey, got no error", seed, i, raw)
		}
		if !errors.Is(err, schemanorm.ErrDuplicateKey) {
			t.Errorf("seed %d iter %d: errors.Is(err, ErrDuplicateKey) = false; err = %v\nschema: %s", seed, i, err, raw)
		}
	}
	if injected == 0 {
		t.Fatal("no iteration produced an injectable object node -- generator regression")
	}
}

// utf8SentinelPlaceholder is a plain-ASCII string marshaled into a guaranteed
// string field, then corrupted at the byte level after marshaling. It cannot
// be corrupted by constructing an invalid Go string and letting
// json.Marshal encode it -- Go strings holding invalid UTF-8 get silently
// replaced with U+FFFD by encoding/json's own Marshal, which would defeat
// the injection before Normalize ever saw it.
const utf8SentinelPlaceholder = "ZZ_UTF8_SENTINEL_ZZ"

// TestNormalize_Property_InvalidUTF8AlwaysRejected is a property-based
// regression for sharp edge 2 (see the package doc): across many randomly
// shaped schemas, corrupting a string value's bytes into invalid UTF-8 (or a
// lone UTF-16 surrogate escape) must ALWAYS be rejected with ErrInvalidUTF8.
func TestNormalize_Property_InvalidUTF8AlwaysRejected(t *testing.T) {
	seed := int64(propertyTestSeed + 2)
	r := rand.New(rand.NewSource(seed))
	t.Logf("property test seed: %d (rerun with this seed to reproduce any failure)", seed)

	injected := 0
	for i := 0; i < propertyIterations; i++ {
		schema := randomSchemaDocument(r)
		if !schema.isObj {
			continue // rare boolean-schema root: nowhere to add the sentinel field
		}
		withSentinel := orderedValue{
			isObj: true,
			keys:  append(append([]string{}, schema.keys...), "zzSentinel"),
			vals:  append(append([]orderedValue{}, schema.vals...), orderedScalar(utf8SentinelPlaceholder)),
		}
		raw, err := json.Marshal(withSentinel)
		if err != nil {
			t.Fatalf("seed %d iter %d: marshal sentinel schema: %v", seed, i, err)
		}

		var corrupted []byte
		if r.Intn(2) == 0 {
			corrupted = bytes.Replace(raw, []byte(`"`+utf8SentinelPlaceholder+`"`), []byte("\"\xff\xfe\""), 1)
		} else {
			corrupted = bytes.Replace(raw, []byte(`"`+utf8SentinelPlaceholder+`"`), []byte(`"\ud800"`), 1)
		}
		if bytes.Equal(corrupted, raw) {
			t.Fatalf("seed %d iter %d: sentinel placeholder not found in marshaled schema: %s", seed, i, raw)
		}
		injected++

		_, err = schemanorm.Normalize(corrupted)
		if err == nil {
			t.Fatalf("seed %d iter %d: Normalize(%s): expected ErrInvalidUTF8, got no error", seed, i, corrupted)
		}
		if !errors.Is(err, schemanorm.ErrInvalidUTF8) {
			t.Errorf("seed %d iter %d: errors.Is(err, ErrInvalidUTF8) = false; err = %v\nschema: %s", seed, i, err, corrupted)
		}
	}
	if injected == 0 {
		t.Fatal("no iteration produced an injectable object root -- generator regression")
	}
}

// decodeWithNumber decodes raw exactly as schemanorm's own internal
// decodeJSON does (UseNumber, so number literal spelling round-trips for
// exact comparison rather than collapsing through float64). This
// deliberately reimplements those two lines instead of importing an
// unexported helper: the DeepEqual oracle above must check Normalize's
// OBSERVABLE input/output behavior from outside the package, not reuse its
// internals in a way that could hide a bug shared by both.
func decodeWithNumber(raw []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	return v, nil
}

// compileSchema compiles raw with santhosh-tekuri/jsonschema/v6, mirroring
// internal/plugin/configvalidate's compile pattern (the package this repo
// already uses jsonschema/v6 through). uri must be unique per compile call
// within a test binary run.
func compileSchema(uri string, raw []byte) (*jsonschema.Schema, error) {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("unmarshal: %w", err)
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource(uri, doc); err != nil {
		return nil, fmt.Errorf("add resource: %w", err)
	}
	return c.Compile(uri)
}

// --- random schema generation -----------------------------------------

// orderedValue is a JSON value that marshals its object members in exactly
// the order they were inserted, bypassing encoding/json's automatic
// map[string]any key sorting. The generator below inserts members in a
// randomly SHUFFLED order (not sorted, and not necessarily insertion
// order), so json.Marshal(orderedValue) genuinely differs byte-for-byte
// from what Normalize would produce -- which is the entire point of this
// harness (see the TestNormalize_PropertyNoDisagreement doc comment).
type orderedValue struct {
	scalar  any // used when keys == nil && array == nil
	array   []orderedValue
	isArray bool
	keys    []string
	vals    []orderedValue
	isObj   bool
}

func (v orderedValue) MarshalJSON() ([]byte, error) {
	switch {
	case v.isObj:
		var buf bytes.Buffer
		buf.WriteByte('{')
		for i, k := range v.keys {
			if i > 0 {
				buf.WriteByte(',')
			}
			kb, err := json.Marshal(k)
			if err != nil {
				return nil, err
			}
			buf.Write(kb)
			buf.WriteByte(':')
			vb, err := json.Marshal(v.vals[i])
			if err != nil {
				return nil, err
			}
			buf.Write(vb)
		}
		buf.WriteByte('}')
		return buf.Bytes(), nil
	case v.isArray:
		var buf bytes.Buffer
		buf.WriteByte('[')
		for i, e := range v.array {
			if i > 0 {
				buf.WriteByte(',')
			}
			eb, err := json.Marshal(e)
			if err != nil {
				return nil, err
			}
			buf.Write(eb)
		}
		buf.WriteByte(']')
		return buf.Bytes(), nil
	default:
		return json.Marshal(v.scalar)
	}
}

// lookup returns the value associated with key in an object-shaped
// orderedValue, or (zero value, false) if v is not an object or has no such
// key. Used by deriveProbeFromSchema to peek at a generated schema's shape.
func (v orderedValue) lookup(key string) (orderedValue, bool) {
	if !v.isObj {
		return orderedValue{}, false
	}
	for i, k := range v.keys {
		if k == key {
			return v.vals[i], true
		}
	}
	return orderedValue{}, false
}

func orderedScalar(v any) orderedValue {
	return orderedValue{scalar: v}
}

func orderedArrayOf(elems []orderedValue) orderedValue {
	return orderedValue{isArray: true, array: elems}
}

func orderedArrayOfStrings(ss []string) orderedValue {
	elems := make([]orderedValue, len(ss))
	for i, s := range ss {
		elems[i] = orderedScalar(s)
	}
	return orderedArrayOf(elems)
}

// orderedObject builds an object from key/value pairs and shuffles the
// member order with r, so the emitted JSON text is not already sorted.
func orderedObject(r *rand.Rand, keys []string, vals []orderedValue) orderedValue {
	idx := r.Perm(len(keys))
	shuffledKeys := make([]string, len(keys))
	shuffledVals := make([]orderedValue, len(vals))
	for newPos, oldPos := range idx {
		shuffledKeys[newPos] = keys[oldPos]
		shuffledVals[newPos] = vals[oldPos]
	}
	return orderedValue{isObj: true, keys: shuffledKeys, vals: shuffledVals}
}

// propertyNamePool includes plain ASCII names alongside non-ASCII and
// escaped-character names (a café/emoji/quote/backslash/slash/newline), so
// the generator exercises the same key-text diversity the duplicate-key
// scanner and marshalDeterministic's re-escaping both have to handle
// correctly, not only the common case.
var propertyNamePool = []string{
	"a", "b", "c", "d", "e", "name", "value", "id", "cmd", "kind",
	"café", "emoji😀", "with\"quote", "with\\backslash", "with/slash", "with\nnewline",
}

var scalarSchemaTypes = []string{"string", "number", "integer", "boolean"}

var patternPropertyPatterns = []string{"^x-", "^a", ".*", "^[0-9]+$"}

// exoticNumberLiterals mirrors TestNormalize_NumberLiteralsPreserved's fixed
// table, randomized into the generator: these are stashed under "default"
// (annotation-only, never evaluated by the validator) so they exercise the
// DeepEqual oracle -- which is spelling-sensitive on json.Number -- without
// affecting the validator-differential oracle at all.
var exoticNumberLiterals = []string{
	"1.500", "1e400", "-0", "1.0",
	"123456789012345678901234567890",
	"0.100000000000000000000000000001",
}

// pickDistinct returns up to n distinct elements of pool, in pool's order
// (the caller is responsible for shuffling if order matters).
func pickDistinct(r *rand.Rand, pool []string, n int) []string {
	if n > len(pool) {
		n = len(pool)
	}
	idx := r.Perm(len(pool))[:n]
	out := make([]string, n)
	for i, p := range idx {
		out[i] = pool[p]
	}
	return out
}

// genCtx carries a per-document pool of $defs/definitions entries: built
// once, up front, before any schema node is generated, so that a $ref node
// generated anywhere in the tree (see randomSchemaValue's ref-shaped case)
// is GUARANTEED to resolve -- randomSchemaDocument attaches ctx's pool to
// the document root, unconditionally, after the body is built. Building the
// pool first and attaching it last (rather than trying to interleave $ref
// emission with $defs construction) is what keeps referential integrity
// trivial instead of needing a two-pass fixup.
type genCtx struct {
	refNames     []string
	refContainer string
	refsObject   orderedValue
}

// newGenCtx decides, once per generated document, whether this document
// gets a populated $defs/definitions pool (about 1 in 3 do) -- and if so,
// which container keyword name (both "$defs" and legacy "definitions" are
// exercised, since finding 3's survivor list named both).
func newGenCtx(r *rand.Rand) *genCtx {
	if r.Intn(3) != 0 {
		return &genCtx{}
	}
	container := "$defs"
	if r.Intn(2) == 0 {
		container = "definitions"
	}
	n := 1 + r.Intn(2)
	names := make([]string, n)
	keys := make([]string, n)
	vals := make([]orderedValue, n)
	for i := 0; i < n; i++ {
		name := "Def" + strconv.Itoa(i)
		names[i] = name
		keys[i] = name
		vals[i] = genLeafDefSchema(r)
	}
	return &genCtx{
		refNames:     names,
		refContainer: container,
		refsObject:   orderedObject(r, keys, vals),
	}
}

// genLeafDefSchema builds a small, non-recursive schema for a $defs entry.
// Kept deliberately simple (no nested $ref) so referential integrity never
// needs more than one level of bookkeeping.
func genLeafDefSchema(r *rand.Rand) orderedValue {
	switch r.Intn(3) {
	case 0:
		return orderedObject(r, []string{"type", "enum"}, []orderedValue{
			orderedScalar("string"),
			orderedArrayOf([]orderedValue{orderedScalar("alpha"), orderedScalar("beta")}),
		})
	case 1:
		return orderedObject(r, []string{"type", "minimum"}, []orderedValue{
			orderedScalar("integer"),
			orderedScalar(r.Intn(5)),
		})
	default:
		return orderedScalar(true) // permissive boolean schema
	}
}

// randomSchemaDocument is the property tests' single entry point into
// schema generation: it builds a ctx (possibly with a $defs/definitions
// pool), generates the body via ctx.randomSchemaValue, then -- only at the
// document root -- attaches the $defs/definitions pool (if any) plus
// optional $schema/$id/$anchor. These four are root-scoped on purpose: $id
// establishes a new base URI for JSON Pointer resolution, so adding it
// anywhere BETWEEN the root and a $ref node could change what that $ref
// resolves against; keeping $id/$anchor/$schema exclusively at the document
// root (never on an intermediate node) sidesteps that whole class of
// resolution subtlety, which is irrelevant to this package (it does not
// resolve $ref) but very relevant to keeping every generated document
// compilable by jsonschema/v6.
func randomSchemaDocument(r *rand.Rand) orderedValue {
	ctx := newGenCtx(r)
	root := ctx.randomSchemaValue(r, 0)
	if !root.isObj {
		return root // rare boolean-schema root: nothing to attach root-only extras to
	}

	keys := append([]string{}, root.keys...)
	vals := append([]orderedValue{}, root.vals...)
	add := func(k string, v orderedValue) {
		keys = append(keys, k)
		vals = append(vals, v)
	}

	if len(ctx.refNames) > 0 {
		add(ctx.refContainer, ctx.refsObject)
	}
	if r.Intn(4) == 0 {
		add("$schema", orderedScalar("https://json-schema.org/draft/2020-12/schema"))
	}
	if r.Intn(4) == 0 {
		add("$id", orderedScalar("https://example.test/schemas/"+strconv.Itoa(r.Intn(1000))))
	}
	if r.Intn(6) == 0 {
		add("$anchor", orderedScalar("anchor"+strconv.Itoa(r.Intn(1000))))
	}

	return orderedObject(r, keys, vals)
}

// randomSchemaValue builds one schema node as an orderedValue. See the
// orderedValue doc comment for why this does not use a plain Go map. Kept as
// a *genCtx method (rather than a free function) so a ref-shaped node
// anywhere in the tree can reach the $defs/definitions pool the document
// root will attach.
func (ctx *genCtx) randomSchemaValue(r *rand.Rand, depth int) orderedValue {
	if depth >= maxRandomSchemaDepth || r.Intn(6) == 0 {
		return orderedScalar(r.Intn(2) == 0) // boolean schema: true or false
	}

	var keys []string
	var vals []orderedValue
	add := func(k string, v orderedValue) {
		keys = append(keys, k)
		vals = append(vals, v)
	}

	// The ref-shaped case is only reachable once ctx has a populated pool
	// to reference -- see newGenCtx.
	caseCount := 6
	if len(ctx.refNames) > 0 {
		caseCount = 7
	}

	switch r.Intn(caseCount) {
	case 0: // object-shaped
		add("type", orderedScalar("object"))
		propNames := pickDistinct(r, propertyNamePool, r.Intn(4))
		if len(propNames) > 0 {
			propVals := make([]orderedValue, len(propNames))
			for i := range propNames {
				propVals[i] = ctx.randomSchemaValue(r, depth+1)
			}
			add("properties", orderedObject(r, propNames, propVals))
			if r.Intn(2) == 0 {
				required := pickDistinct(r, propNames, r.Intn(len(propNames)+1))
				add("required", orderedArrayOfStrings(required))
			}
		}
		if r.Intn(2) == 0 {
			add("additionalProperties", orderedScalar(r.Intn(2) == 0))
		}
		if r.Intn(4) == 0 {
			add("unevaluatedProperties", orderedScalar(r.Intn(2) == 0))
		}
		if r.Intn(4) == 0 {
			pattern := patternPropertyPatterns[r.Intn(len(patternPropertyPatterns))]
			add("patternProperties", orderedObject(r, []string{pattern}, []orderedValue{ctx.randomSchemaValue(r, depth+1)}))
		}
	case 1: // array-shaped
		add("type", orderedScalar("array"))
		add("items", ctx.randomSchemaValue(r, depth+1))
		if r.Intn(2) == 0 {
			add("minItems", orderedScalar(r.Intn(3)))
		}
		if r.Intn(4) == 0 {
			add("unevaluatedItems", orderedScalar(r.Intn(2) == 0))
		}
	case 2: // scalar-shaped with constraints
		st := scalarSchemaTypes[r.Intn(len(scalarSchemaTypes))]
		add("type", orderedScalar(st))
		switch st {
		case "string":
			if r.Intn(2) == 0 {
				add("minLength", orderedScalar(r.Intn(4)))
			}
		case "number", "integer":
			if r.Intn(2) == 0 {
				add("minimum", orderedScalar(r.Intn(10)))
			}
			if r.Intn(2) == 0 {
				add("maximum", orderedScalar(10+r.Intn(10)))
			}
		}
		if r.Intn(3) == 0 {
			add("enum", orderedArrayOf(randomEnumValues(r, st)))
		}
		if r.Intn(6) == 0 {
			add("const", randomConstValue(r, st))
		}
	case 3: // combinator-shaped
		kind := []string{"allOf", "anyOf", "oneOf"}[r.Intn(3)]
		n := 1 + r.Intn(2)
		branches := make([]orderedValue, n)
		for i := range branches {
			branches[i] = ctx.randomSchemaValue(r, depth+1)
		}
		add(kind, orderedArrayOf(branches))
		if kind == "allOf" && r.Intn(2) == 0 {
			// A sibling keyword alongside allOf is exactly the shape a
			// reintroduced allOf-merge bug (this package's predecessor's
			// central defect, see the package doc) would target.
			add("type", orderedScalar("object"))
		}
	case 4: // not-shaped
		add("not", ctx.randomSchemaValue(r, depth+1))
	case 5: // if/then[/else]-shaped
		add("if", ctx.randomSchemaValue(r, depth+1))
		add("then", ctx.randomSchemaValue(r, depth+1))
		if r.Intn(2) == 0 {
			add("else", ctx.randomSchemaValue(r, depth+1))
		}
	case 6: // ref-shaped: only reachable when ctx.refNames is non-empty
		name := ctx.refNames[r.Intn(len(ctx.refNames))]
		add("$ref", orderedScalar("#/"+ctx.refContainer+"/"+name))
	}

	if r.Intn(5) == 0 {
		add("description", orderedScalar("random schema "+strconv.Itoa(r.Int())))
	}
	if r.Intn(6) == 0 {
		// "default" is annotation-only (never evaluated by Validate), so an
		// exotic literal here exercises the DeepEqual oracle's
		// number-spelling sensitivity without perturbing the
		// validator-differential oracle.
		add("default", orderedScalar(json.Number(exoticNumberLiterals[r.Intn(len(exoticNumberLiterals))])))
	}

	return orderedObject(r, keys, vals)
}

func randomEnumValues(r *rand.Rand, schemaType string) []orderedValue {
	n := 1 + r.Intn(3)
	out := make([]orderedValue, n)
	for i := range out {
		switch schemaType {
		case "string":
			out[i] = orderedScalar([]string{"alpha", "beta", "gamma", "delta"}[r.Intn(4)])
		case "number", "integer":
			out[i] = orderedScalar(r.Intn(20))
		case "boolean":
			out[i] = orderedScalar(r.Intn(2) == 0)
		default:
			out[i] = orderedScalar(r.Intn(20))
		}
	}
	return out
}

func randomConstValue(r *rand.Rand, schemaType string) orderedValue {
	switch schemaType {
	case "string":
		return orderedScalar([]string{"alpha", "beta", "gamma"}[r.Intn(3)])
	case "number", "integer":
		return orderedScalar(r.Intn(20))
	case "boolean":
		return orderedScalar(r.Intn(2) == 0)
	default:
		return orderedScalar(r.Intn(20))
	}
}

// injectDuplicateKey walks v looking for an object node to duplicate a key
// in, descending randomly (rather than always at the root) so the injected
// duplicate lands at varying depths across calls. Returns (v, false)
// unchanged when v contains no object node at all -- only reachable for a
// boolean-schema root ("true"/"false"), which callers must skip.
func injectDuplicateKey(r *rand.Rand, v orderedValue) (orderedValue, bool) {
	switch {
	case v.isObj:
		if len(v.vals) > 0 && r.Intn(2) == 0 {
			i := r.Intn(len(v.vals))
			if mutated, ok := injectDuplicateKey(r, v.vals[i]); ok {
				newVals := append([]orderedValue{}, v.vals...)
				newVals[i] = mutated
				return orderedValue{isObj: true, keys: append([]string{}, v.keys...), vals: newVals}, true
			}
		}
		if len(v.keys) == 0 {
			return v, false
		}
		return orderedValue{
			isObj: true,
			keys:  append(append([]string{}, v.keys...), v.keys[0]),
			vals:  append(append([]orderedValue{}, v.vals...), v.vals[0]),
		}, true
	case v.isArray:
		if len(v.array) == 0 {
			return v, false
		}
		i := r.Intn(len(v.array))
		if mutated, ok := injectDuplicateKey(r, v.array[i]); ok {
			newArr := append([]orderedValue{}, v.array...)
			newArr[i] = mutated
			return orderedValue{isArray: true, array: newArr}, true
		}
		return v, false
	default:
		return v, false
	}
}

// orderedValueToPlainAny converts an orderedValue into the shape
// encoding/json would decode its marshaled bytes into (map[string]any /
// []any / string / bool / nil / float64), for use as a probe instance value
// lifted directly from a schema literal (a const or enum entry).
// json.Number scalars are converted to float64 on a best-effort basis: a
// probe instance is a plain decoded value, not a schema, and does not need
// the exact-literal-spelling guarantee schema values do.
func orderedValueToPlainAny(v orderedValue) any {
	switch {
	case v.isObj:
		m := make(map[string]any, len(v.keys))
		for i, k := range v.keys {
			m[k] = orderedValueToPlainAny(v.vals[i])
		}
		return m
	case v.isArray:
		arr := make([]any, len(v.array))
		for i, e := range v.array {
			arr[i] = orderedValueToPlainAny(e)
		}
		return arr
	default:
		if n, ok := v.scalar.(json.Number); ok {
			f, err := n.Float64()
			if err != nil {
				return 0.0
			}
			return f
		}
		return v.scalar
	}
}

// deriveProbeFromSchema builds an instance likely to satisfy schema's shape
// constraints (const/enum/type/properties/items), so a meaningful fraction
// of TestNormalize_PropertyNoDisagreement's probes actually validate rather
// than exercising two rejections of unrelated random junk. It is a
// best-effort heuristic, not a solver: schemas built from $ref, allOf/anyOf/
// oneOf, not, or if/then/else fall through to a plain random instance (see
// randomInstance) instead of trying to satisfy those structurally.
func deriveProbeFromSchema(r *rand.Rand, schema orderedValue, depth int) any {
	if depth >= maxRandomInstanceDepth {
		return randomScalarInstance(r)
	}
	if c, ok := schema.lookup("const"); ok {
		return orderedValueToPlainAny(c)
	}
	if e, ok := schema.lookup("enum"); ok && e.isArray && len(e.array) > 0 {
		return orderedValueToPlainAny(e.array[r.Intn(len(e.array))])
	}
	t, ok := schema.lookup("type")
	if !ok {
		return randomInstance(r, depth)
	}
	st, ok := t.scalar.(string)
	if !ok {
		return randomInstance(r, depth)
	}
	switch st {
	case "object":
		obj := make(map[string]any)
		if p, ok := schema.lookup("properties"); ok && p.isObj {
			for i, k := range p.keys {
				if r.Intn(2) == 0 {
					obj[k] = deriveProbeFromSchema(r, p.vals[i], depth+1)
				}
			}
		}
		return obj
	case "array":
		n := r.Intn(3)
		arr := make([]any, n)
		items, hasItems := schema.lookup("items")
		for i := range arr {
			if hasItems {
				arr[i] = deriveProbeFromSchema(r, items, depth+1)
			} else {
				arr[i] = randomScalarInstance(r)
			}
		}
		return arr
	case "string":
		return []string{"alpha", "beta", "gamma", "delta", "x"}[r.Intn(5)]
	case "number", "integer":
		return float64(r.Intn(20))
	case "boolean":
		return r.Intn(2) == 0
	default:
		return randomInstance(r, depth)
	}
}

// randomInstance builds a plain JSON value (never a schema) to validate
// against a random schema. Instance order never matters -- only schema
// member order does -- so this returns ordinary Go values.
func randomInstance(r *rand.Rand, depth int) any {
	if depth >= maxRandomInstanceDepth {
		return randomScalarInstance(r)
	}
	switch r.Intn(4) {
	case 0:
		n := r.Intn(3)
		arr := make([]any, n)
		for i := range arr {
			arr[i] = randomInstance(r, depth+1)
		}
		return arr
	case 1:
		names := pickDistinct(r, propertyNamePool, r.Intn(4))
		obj := make(map[string]any, len(names))
		for _, name := range names {
			obj[name] = randomInstance(r, depth+1)
		}
		return obj
	default:
		return randomScalarInstance(r)
	}
}

func randomScalarInstance(r *rand.Rand) any {
	switch r.Intn(5) {
	case 0:
		return nil
	case 1:
		return r.Intn(2) == 0
	case 2:
		return r.Intn(100)
	case 3:
		return []string{"alpha", "beta", "gamma", "delta", "x"}[r.Intn(5)]
	default:
		return float64(r.Intn(1000)) / 3
	}
}
