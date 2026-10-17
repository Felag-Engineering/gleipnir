package conform

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/santhosh-tekuri/jsonschema/v6" // vocab-allow: a module path, not a vendor reference
)

// profileToolProvider is the manifest profile name that selects the TP-* checks.
const profileToolProvider = "tool_provider"

// toolNameRE is the tool-name charset (TP-NAME-01): the MCP specification's
// recommended set, mandatory for a managed plugin because the name travels
// through policy YAML, a routing header and every LLM provider's name rules.
var toolNameRE = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,128}$`)

// hostToolPrefix is the host endpoint's tool-name prefix (TP-NAME-02).
const hostToolPrefix = "host/"

// extensionMethods are the io.gleipnir/* method names no tool may take
// (TP-NAME-03): host-initiated interactions are extension methods, never
// tools, and a tool by one of these names breaks that from the other side.
var extensionMethods = []string{
	"channel/notify",
	"channel/request",
	"events/discover",
	"events/event",
	"events/listen",
}

// unknownToolName is the name TP-CALL-01 calls. It is outside the TP-NAME-01
// charset on purpose, so no conforming server can actually serve it.
const unknownToolName = "conform/unknown-tool"

var toolProviderChecks = []Check{
	{ID: "TP-PROTO-01", Profile: profileToolProvider, Run: checkProtocol},
	{ID: "TP-DISC-01", Profile: profileToolProvider, Run: checkToolsList},
	{ID: "TP-DISC-02", Profile: profileToolProvider, Run: checkSchemas},
	{ID: "TP-DISC-03", Profile: profileToolProvider, Run: checkUniqueNames},
	{ID: "TP-NAME-01", Profile: profileToolProvider, Run: checkNameCharset},
	{ID: "TP-NAME-02", Profile: profileToolProvider, Run: checkNoHostPrefix},
	{ID: "TP-NAME-03", Profile: profileToolProvider, Run: checkNoExtensionNames},
	{ID: "TP-MAN-01", Profile: profileToolProvider, Run: checkManifestTools},
	{ID: "TP-CALL-01", Profile: profileToolProvider, Run: checkUnknownCall},
}

// TP-PROTO-01: server/discover succeeds and supports 2026-07-28.
func checkProtocol(ctx context.Context, s *Session) error {
	res, err := s.serverDiscover(ctx)
	if err != nil {
		return err
	}
	if !slices.Contains(res.SupportedVersions, protocolVersion) {
		return fmt.Errorf("server/discover supportedVersions %v does not include %s", res.SupportedVersions, protocolVersion)
	}
	return nil
}

// TP-DISC-01: tools/list succeeds over the stateless transport.
func checkToolsList(ctx context.Context, s *Session) error {
	_, err := s.toolsList(ctx)
	return err
}

// TP-DISC-02: every inputSchema is valid JSON without duplicate keys and
// compiles as JSON Schema.
func checkSchemas(ctx context.Context, s *Session) error {
	tools, err := s.toolsOrSkip(ctx)
	if err != nil {
		return err
	}
	var problems []string
	for _, t := range tools {
		if len(bytes.TrimSpace(t.InputSchema)) == 0 || bytes.Equal(bytes.TrimSpace(t.InputSchema), []byte("null")) {
			continue
		}
		if err := validateSchema(t.InputSchema); err != nil {
			problems = append(problems, fmt.Sprintf("%q: %v", t.Name, err))
		}
	}
	return joinProblems("inputSchema", problems)
}

// validateSchema reports why raw is not a usable schema.
func validateSchema(raw []byte) error {
	if !utf8.Valid(raw) {
		return errors.New("not valid UTF-8")
	}
	if err := findDuplicateKey(raw); err != nil {
		return err
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("not valid JSON: %w", err)
	}
	c := jsonschema.NewCompiler()
	// The compiler's default loader reads local files for a "file://" $ref,
	// and the schema is whatever the server under test sent. Standard
	// metaschemas still resolve from the library's embedded copies.
	c.UseLoader(denyAllLoader{})
	c.DefaultDraft(jsonschema.Draft2020)
	if err := c.AddResource("mem://schema.json", doc); err != nil {
		return fmt.Errorf("does not compile: %w", err)
	}
	if _, err := c.Compile("mem://schema.json"); err != nil {
		return fmt.Errorf("does not compile: %w", err)
	}
	return nil
}

// denyAllLoader refuses every external schema reference.
type denyAllLoader struct{}

func (denyAllLoader) Load(url string) (any, error) {
	return nil, fmt.Errorf("external schema reference not permitted: %s", url)
}

// findDuplicateKey reports the first object in raw that repeats a key.
// encoding/json keeps the last value silently, and a host that canonicalizes
// the schema refuses it, so it is caught here instead.
func findDuplicateKey(raw []byte) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := scanValue(dec); err != nil {
		return err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return errors.New("not valid JSON: content after the schema")
	}
	return nil
}

// scanValue consumes one JSON value from dec, recursing into containers.
func scanValue(dec *json.Decoder) error {
	tok, err := dec.Token()
	if err != nil {
		return fmt.Errorf("not valid JSON: %w", err)
	}
	switch tok {
	case json.Delim('{'):
		keys := map[string]bool{}
		for dec.More() {
			keyTok, err := dec.Token()
			if err != nil {
				return fmt.Errorf("not valid JSON: %w", err)
			}
			key := keyTok.(string) // the decoder only yields a string in key position
			if keys[key] {
				return fmt.Errorf("duplicate object key %q", key)
			}
			keys[key] = true
			if err := scanValue(dec); err != nil {
				return err
			}
		}
	case json.Delim('['):
		for dec.More() {
			if err := scanValue(dec); err != nil {
				return err
			}
		}
	default:
		return nil // a scalar
	}
	// The closing delimiter.
	if _, err := dec.Token(); err != nil {
		return fmt.Errorf("not valid JSON: %w", err)
	}
	return nil
}

// TP-DISC-03: tool names are unique within tools/list.
func checkUniqueNames(ctx context.Context, s *Session) error {
	tools, err := s.toolsOrSkip(ctx)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	var dups []string
	for _, t := range tools {
		if seen[t.Name] && !slices.Contains(dups, t.Name) {
			dups = append(dups, t.Name)
		}
		seen[t.Name] = true
	}
	return joinProblems("tool names listed more than once", quoteAll(dups))
}

// TP-NAME-01: names are 1–128 characters from [A-Za-z0-9_.-].
func checkNameCharset(ctx context.Context, s *Session) error {
	return eachName(ctx, s, "tool names outside [A-Za-z0-9_.-]{1,128}", func(name string) bool {
		return !toolNameRE.MatchString(name)
	})
}

// TP-NAME-02: no name starts with host/.
func checkNoHostPrefix(ctx context.Context, s *Session) error {
	return eachName(ctx, s, "tool names with the host-plane prefix "+hostToolPrefix, func(name string) bool {
		return strings.HasPrefix(name, hostToolPrefix)
	})
}

// TP-NAME-03: no name equals an io.gleipnir/* method.
func checkNoExtensionNames(ctx context.Context, s *Session) error {
	return eachName(ctx, s, "tool names that are io.gleipnir/* methods", func(name string) bool {
		return slices.Contains(extensionMethods, name)
	})
}

// eachName fails with every tool name bad reports true for.
func eachName(ctx context.Context, s *Session, what string, bad func(string) bool) error {
	tools, err := s.toolsOrSkip(ctx)
	if err != nil {
		return err
	}
	var offenders []string
	for _, t := range tools {
		if bad(t.Name) {
			offenders = append(offenders, t.Name)
		}
	}
	return joinProblems(what, quoteAll(offenders))
}

// TP-MAN-01: every manifest gleipnir.tools name is served.
func checkManifestTools(ctx context.Context, s *Session) error {
	tools, err := s.toolsOrSkip(ctx)
	if err != nil {
		return err
	}
	served := map[string]bool{}
	for _, t := range tools {
		served[t.Name] = true
	}
	var missing []string
	for _, decl := range s.target.Manifest.Gleipnir.Tools {
		if !served[decl.Name] {
			missing = append(missing, decl.Name)
		}
	}
	return joinProblems("manifest gleipnir.tools not served by tools/list", quoteAll(missing))
}

// TP-CALL-01: calling a tool the server does not serve fails.
func checkUnknownCall(ctx context.Context, s *Session) error {
	params := map[string]any{"name": unknownToolName, "arguments": map[string]any{}}
	resp, err := s.client.call(ctx, "tools/call", unknownToolName, params)
	if err != nil {
		// A non-2xx answer without a JSON-RPC body is still a refusal; only
		// a request that got no HTTP answer at all says nothing.
		if resp.HTTPStatus >= 400 {
			return nil
		}
		return fmt.Errorf("tools/call %q got no answer: %w", unknownToolName, err)
	}
	if resp.Err != nil {
		return nil
	}
	var result struct {
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return fmt.Errorf("tools/call %q: undecodable result: %w", unknownToolName, err)
	}
	if !result.IsError {
		return fmt.Errorf("tools/call %q, a tool the server does not serve, returned a success result", unknownToolName)
	}
	return nil
}

// joinProblems returns nil for no problems, or one error listing them.
func joinProblems(what string, problems []string) error {
	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf("%s: %s", what, strings.Join(problems, "; "))
}

func quoteAll(names []string) []string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = fmt.Sprintf("%q", n)
	}
	return out
}
