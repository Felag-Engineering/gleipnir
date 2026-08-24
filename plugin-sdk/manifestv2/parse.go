package manifestv2

import (
	"bytes"
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Issue is one validation failure, tagged with the field that caused it so a
// consent screen can point at the offending line rather than printing a wall of
// text.
type Issue struct {
	Field   string
	Message string
}

func (i Issue) String() string { return i.Field + ": " + i.Message }

// ValidationError carries every issue found in one manifest. Validation
// reports all of them rather than stopping at the first: an author fixing a
// manifest should see the whole list, not discover it one round trip at a time.
type ValidationError struct {
	Issues []Issue
}

func (e *ValidationError) Error() string {
	parts := make([]string, len(e.Issues))
	for i, issue := range e.Issues {
		parts[i] = issue.String()
	}
	return "invalid plugin manifest: " + strings.Join(parts, "; ")
}

// Parse decodes and validates a v2 manifest.
//
// Decoding is STRICT: an unknown field is an error, not something to ignore.
// The manifest is a consent surface, and a field the host silently drops is a
// claim the admin read and the runtime never enforced — the exact shape of
// mistake that makes a review meaningless. That covers unknown profiles,
// misspelled keys, and fields from a future schema version this host predates.
//
// Validation is fail-closed for the same reason (ADR-049 posture): a manifest
// that does not fully parse and validate does not install.
func Parse(data []byte) (*Manifest, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)

	var m Manifest
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("parsing plugin manifest: %w", err)
	}

	if err := Validate(&m); err != nil {
		return nil, err
	}
	return &m, nil
}

// Validate checks a manifest's semantic rules, returning a *ValidationError
// listing every problem found.
func Validate(m *Manifest) error {
	var issues []Issue
	add := func(field, format string, args ...any) {
		issues = append(issues, Issue{Field: field, Message: fmt.Sprintf(format, args...)})
	}

	if m.SchemaVersion != SchemaVersion {
		add("schema_version", "must be %q, got %q", SchemaVersion, m.SchemaVersion)
	}
	if strings.TrimSpace(m.Name) == "" {
		add("name", "is required")
	} else if strings.ContainsAny(m.Name, " \t\n") {
		add("name", "must not contain whitespace")
	}
	if strings.TrimSpace(m.Version) == "" {
		add("version", "is required")
	}

	validatePackage(m.Package, add)
	validateProfiles(m.Gleipnir.Profiles, add)
	validateEgress(m.Gleipnir.Egress, add)
	validateResources(m.Gleipnir.Resources, add)
	validateTools(m.Gleipnir.Tools, add)
	validateEventKinds(m.Gleipnir.EventKinds, m.Gleipnir.Profiles.EventSource, add)

	if len(issues) > 0 {
		return &ValidationError{Issues: issues}
	}
	return nil
}

func validatePackage(p Package, add func(string, string, ...any)) {
	if p.RegistryType != RegistryTypeOCI {
		add("package.registry_type", "must be %q (a managed plugin runs as a container), got %q",
			RegistryTypeOCI, p.RegistryType)
	}

	switch {
	case strings.TrimSpace(p.Identifier) == "":
		add("package.identifier", "is required")
	case !strings.Contains(p.Identifier, "@sha256:"):
		// A tag is a mutable pointer. Consenting to a tag is consenting to
		// whatever it points at later, which is not consent at all.
		add("package.identifier", "must be digest-pinned (repository@sha256:...), got %q", p.Identifier)
	default:
		digest := p.Identifier[strings.Index(p.Identifier, "@sha256:")+len("@sha256:"):]
		if len(digest) != 64 || !isHex(digest) {
			add("package.identifier", "digest must be 64 hex characters, got %q", digest)
		}
	}

	if p.Transport.Type != TransportStreamableHTTP {
		add("package.transport.type", "must be %q, got %q", TransportStreamableHTTP, p.Transport.Type)
	}
	if p.Transport.Port < 0 || p.Transport.Port > 65535 {
		add("package.transport.port", "must be between 0 and 65535, got %d", p.Transport.Port)
	}
}

func validateProfiles(p Profiles, add func(string, string, ...any)) {
	if len(p.Declared()) == 0 {
		add("gleipnir.profiles", "at least one profile must be declared")
	}

	if p.HumanChannel != nil {
		switch p.HumanChannel.Assurance {
		case AssuranceAuthenticated, AssuranceWeak:
		case "":
			add("gleipnir.profiles.human_channel.assurance",
				"is required; the host decides what a channel may resolve from how strongly it authenticates the acting human")
		default:
			add("gleipnir.profiles.human_channel.assurance",
				"must be %q or %q, got %q", AssuranceAuthenticated, AssuranceWeak, p.HumanChannel.Assurance)
		}
	}

	if p.IdentityProvider != nil && len(p.IdentityProvider.LinkMethods) == 0 {
		add("gleipnir.profiles.identity_provider.link_methods",
			"at least one link method is required; a provider with no way to link identities provides no identity")
	}
}

func validateEgress(grants []EgressGrant, add func(string, string, ...any)) {
	seen := make(map[string]bool, len(grants))
	for i, g := range grants {
		field := fmt.Sprintf("gleipnir.egress[%d].domain", i)
		domain := strings.TrimSpace(g.Domain)

		switch {
		case domain == "":
			add(field, "is required")
			continue
		case strings.Contains(domain, "://"):
			add(field, "must be a bare hostname without a scheme, got %q", domain)
			continue
		case strings.ContainsAny(domain, "/: "):
			add(field, "must be a bare hostname without a path, port, or spaces, got %q", domain)
			continue
		}

		host := domain
		if strings.HasPrefix(host, "*.") {
			host = host[2:]
		}
		if strings.Contains(host, "*") {
			add(field, "supports only a single leading \"*.\" wildcard, got %q", domain)
			continue
		}
		if !isHostname(host) {
			add(field, "is not a valid hostname: %q", domain)
			continue
		}

		if seen[domain] {
			add(field, "is a duplicate of an earlier grant: %q", domain)
		}
		seen[domain] = true
	}
}

func validateResources(r *Resources, add func(string, string, ...any)) {
	if r == nil {
		return
	}
	if r.MemoryMB < 0 {
		add("gleipnir.resources.memory_mb", "must not be negative, got %d", r.MemoryMB)
	}
	if r.CPUMillicores < 0 {
		add("gleipnir.resources.cpu_millicores", "must not be negative, got %d", r.CPUMillicores)
	}
}

func validateTools(tools []ToolDecl, add func(string, string, ...any)) {
	seen := make(map[string]bool, len(tools))
	for i, t := range tools {
		if strings.TrimSpace(t.Name) == "" {
			add(fmt.Sprintf("gleipnir.tools[%d].name", i), "is required")
			continue
		}
		if seen[t.Name] {
			add(fmt.Sprintf("gleipnir.tools[%d].name", i), "is declared more than once: %q", t.Name)
		}
		seen[t.Name] = true

		switch t.ElicitationKind {
		case "", ElicitationKindPermission, ElicitationKindInformation:
		default:
			add(fmt.Sprintf("gleipnir.tools[%d].elicitation_kind", i),
				"must be %q or %q, got %q", ElicitationKindPermission, ElicitationKindInformation, t.ElicitationKind)
		}
	}
}

// allowedBindingOperators is the closed ADR-052 operator-name set an
// event_kinds[].operators entry may name. Mirrors internal/plugin/binding's
// Operator enum (equals, contains, regex, mention_only) — "glob" stays
// reserved there (ErrUnsupportedOperator) and is rejected here too. Kept as a
// local constant rather than imported: plugin-sdk cannot depend on
// internal/* (it is a separate Go module meant to also serve third-party
// plugin authors).
var allowedBindingOperators = map[string]bool{
	"equals":       true,
	"contains":     true,
	"regex":        true,
	"mention_only": true,
}

// allowedBindingOperatorNames renders allowedBindingOperators as a stable,
// sorted, comma-separated list for error messages.
func allowedBindingOperatorNames() string {
	names := make([]string, 0, len(allowedBindingOperators))
	for name := range allowedBindingOperators {
		names = append(names, name)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

func validateEventKinds(kinds []EventKindDecl, eventSource *EventSourceProfile, add func(string, string, ...any)) {
	if len(kinds) > 0 && eventSource == nil {
		// A manifest attesting event kinds without declaring the profile that
		// emits them is disagreeing with itself.
		add("gleipnir.event_kinds", "requires profiles.event_source to be declared")
	}

	seen := make(map[string]bool, len(kinds))
	for i, k := range kinds {
		if strings.TrimSpace(k.Kind) == "" {
			add(fmt.Sprintf("gleipnir.event_kinds[%d].kind", i), "is required")
			continue
		}
		if seen[k.Kind] {
			add(fmt.Sprintf("gleipnir.event_kinds[%d].kind", i), "is declared more than once: %q", k.Kind)
		}
		seen[k.Kind] = true

		validateEventKindOperators(i, k, add)
	}
}

// validateEventKindOperators checks that every field named under operators is
// actually declared in the kind's binding_schema, and that every operator
// named for a field is from the closed ADR-052 set. An operator set for a
// field nobody can bind is an attestation about nothing; an operator outside
// the set is one the runtime evaluator (internal/plugin/binding) cannot
// enforce.
func validateEventKindOperators(i int, k EventKindDecl, add func(string, string, ...any)) {
	if len(k.Operators) == 0 {
		return
	}
	fields := bindingSchemaFieldNames(k.BindingSchema)

	names := make([]string, 0, len(k.Operators))
	for field := range k.Operators {
		names = append(names, field)
	}
	sort.Strings(names)

	field := fmt.Sprintf("gleipnir.event_kinds[%d].operators", i)
	for _, name := range names {
		if !fields[name] {
			add(field, "names field %q, which is not declared in binding_schema", name)
		}
		for _, op := range k.Operators[name] {
			if !allowedBindingOperators[op] {
				add(field, "field %q names unknown operator %q; must be one of: %s", name, op, allowedBindingOperatorNames())
			}
		}
	}
}

// bindingSchemaFieldNames returns the set of property names declared in a
// binding_schema's top-level "properties" map. A nil schema or one with no
// (or a non-mapping) "properties" key yields an empty set.
func bindingSchemaFieldNames(schema *yaml.Node) map[string]bool {
	fields := map[string]bool{}
	props := mappingValue(schema, "properties")
	if props == nil || props.Kind != yaml.MappingNode {
		return fields
	}
	for i := 0; i+1 < len(props.Content); i += 2 {
		fields[props.Content[i].Value] = true
	}
	return fields
}

// mappingValue returns the value node for key in a YAML mapping node, or nil
// when node is not a mapping or key is absent.
func mappingValue(node *yaml.Node, key string) *yaml.Node {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	return nil
}

// Marshal serializes m into canonical YAML: mapping keys sorted at every level,
// 2-space indent, exactly one trailing newline.
//
// Canonical form matters because signing hashes the manifest bytes: the same Go
// declarations must produce byte-identical output, or a re-serialized manifest
// would fail its own signature.
func Marshal(m *Manifest) ([]byte, error) {
	var root yaml.Node
	if err := root.Encode(m); err != nil {
		return nil, fmt.Errorf("manifest marshal: encode to node: %w", err)
	}
	sortMappingNode(&root)

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&root); err != nil {
		return nil, fmt.Errorf("manifest marshal: encode to bytes: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("manifest marshal: close encoder: %w", err)
	}

	out := bytes.TrimRight(buf.Bytes(), "\n")
	return append(out, '\n'), nil
}

// sortMappingNode recursively sorts mapping keys. Sequences keep declaration
// order — the order of tools or egress grants is authored meaning, not
// incidental.
func sortMappingNode(n *yaml.Node) {
	switch n.Kind {
	case yaml.DocumentNode, yaml.SequenceNode:
		for _, child := range n.Content {
			sortMappingNode(child)
		}
	case yaml.MappingNode:
		type pair struct{ key, value *yaml.Node }
		pairs := make([]pair, 0, len(n.Content)/2)
		for i := 0; i+1 < len(n.Content); i += 2 {
			pairs = append(pairs, pair{n.Content[i], n.Content[i+1]})
		}
		sort.SliceStable(pairs, func(i, j int) bool { return pairs[i].key.Value < pairs[j].key.Value })
		n.Content = n.Content[:0]
		for _, p := range pairs {
			sortMappingNode(p.value)
			n.Content = append(n.Content, p.key, p.value)
		}
	}
}

func isHex(s string) bool {
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'f', r >= 'A' && r <= 'F':
		default:
			return false
		}
	}
	return true
}

// isHostname reports whether s is a plausible DNS hostname: dot-separated
// labels of alphanumerics and hyphens, no empty labels, no leading or trailing
// hyphen. Deliberately not a full RFC 1035 implementation — this is a consent
// surface check, and a name that passes here still has to resolve.
func isHostname(s string) bool {
	if s == "" || len(s) > 253 {
		return false
	}
	for _, label := range strings.Split(s, ".") {
		if label == "" || len(label) > 63 {
			return false
		}
		if strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return false
		}
		for _, r := range label {
			switch {
			case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-':
			default:
				return false
			}
		}
	}
	return true
}

// IsV2 reports whether data looks like a v2 manifest, by reading only its
// schema_version field.
//
// It exists because the host has to ROUTE a bundle before it can trust it: a
// v1 tarball and a v2 tarball arrive through the same directory, and picking
// the wrong pipeline produces a confusing parse error rather than "this is an
// old-format bundle". Parse is strict by design and would reject a v1 manifest
// with a list of unknown-field complaints that say nothing useful about what
// actually happened.
//
// This is a sniff, not a validation. A true answer means "route this to the v2
// pipeline", which then parses strictly and rejects anything wrong. Malformed
// YAML answers false — an unreadable document makes no version claim.
func IsV2(data []byte) bool {
	var probe struct {
		SchemaVersion string `yaml:"schema_version"`
	}
	// Non-strict on purpose: every other field is somebody else's business at
	// this point, including fields from a schema version that does not exist yet.
	if err := yaml.Unmarshal(data, &probe); err != nil {
		return false
	}
	return probe.SchemaVersion == SchemaVersion
}
