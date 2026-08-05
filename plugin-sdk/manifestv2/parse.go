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
	validateEventKinds(m.Gleipnir.EventKinds, add)

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

func validateEventKinds(kinds []EventKindDecl, add func(string, string, ...any)) {
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
	}
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
