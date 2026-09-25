package configvalidate

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/felag-engineering/gleipnir/plugin-sdk/manifestv2"
	"gopkg.in/yaml.v3"
)

// RedactionSentinel is the placeholder value the host substitutes for every
// secret config property on GET responses (ADR-049). Callers that need to
// detect or reject the sentinel compare against this constant.
const RedactionSentinel = "***"

// SecretPropertyNames returns the set of top-level property names in the
// manifest's ConfigSchema that are marked with x-gleipnir-secret: true.
//
// Only the root properties map is inspected; nested object secrets are out of
// scope (ADR-049). Returns a nil map (not an error) when schemaNode is nil or
// declares no properties key. The boolean value of the annotation must be
// exactly true — the string "true" and non-boolean values are not included.
//
// This is the LIVE implementation behind the ADR-049 redaction path. It
// forwards to plugin-sdk/manifestv2's copy of the same logic rather than
// re-implementing it: the two lived as independent, test-pinned duplicates
// until #950 consolidated them (see manifestv2.SecretPropertyNames's doc for
// why a v2-only manifest package carries this logic at all).
func SecretPropertyNames(schemaNode *yaml.Node) (map[string]bool, error) {
	secrets, err := manifestv2.SecretPropertyNames(schemaNode)
	if err != nil {
		return nil, fmt.Errorf("configvalidate: %w", err)
	}
	return secrets, nil
}

// RedactSecrets replaces the value of every key in secretNames with
// RedactionSentinel in configJSON. Non-secret keys are preserved verbatim.
// Keys present in secretNames but absent from configJSON are not added.
//
// Returns "{}" when configJSON is empty or "{}".
func RedactSecrets(configJSON string, secretNames map[string]bool) (string, error) {
	if configJSON == "" || configJSON == "{}" || len(secretNames) == 0 {
		if configJSON == "" {
			return "{}", nil
		}
		return configJSON, nil
	}

	var cfg map[string]any
	if err := json.Unmarshal([]byte(configJSON), &cfg); err != nil {
		return "", fmt.Errorf("configvalidate: unmarshal config JSON for redaction: %w", err)
	}

	for key := range secretNames {
		if _, present := cfg[key]; present {
			cfg[key] = RedactionSentinel
		}
	}

	out, err := json.Marshal(cfg)
	if err != nil {
		return "", fmt.Errorf("configvalidate: marshal redacted config: %w", err)
	}
	return string(out), nil
}

// ContainsRedactionSentinel returns the sorted list of secret-marked keys in
// configMap whose value is exactly RedactionSentinel (the string "***").
// Keys that are not in secretNames are ignored, even if their value is "***".
// Returns an empty slice when no offending keys are found.
func ContainsRedactionSentinel(configMap map[string]any, secretNames map[string]bool) []string {
	if len(configMap) == 0 || len(secretNames) == 0 {
		return nil
	}
	var offenders []string
	for key := range secretNames {
		val, present := configMap[key]
		if !present {
			continue
		}
		if strVal, isStr := val.(string); isStr && strVal == RedactionSentinel {
			offenders = append(offenders, key)
		}
	}
	sort.Strings(offenders)
	return offenders
}
