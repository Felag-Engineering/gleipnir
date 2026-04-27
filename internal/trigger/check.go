package trigger

import (
	"encoding/json"
	"strings"

	"github.com/ohler55/ojg/jp"
	"github.com/ohler55/ojg/oj"
	"github.com/felag-engineering/gleipnir/internal/model"
)

// checkResult holds the raw output and any error from a single MCP tool call.
// When Err is non-nil, the corresponding check is treated as not-passed.
type checkResult struct {
	Output json.RawMessage
	Err    error
}

// extractTextContent unwraps the MCP content array format.
// CallTool returns output in the form [{"type":"text","text":"..."},...].
// This helper finds the first item with type="text" and a non-empty text field,
// returning the text value and true. If no such item exists, it returns ("", false).
//
// Users write JSONPath expressions like $.status expecting to query the tool's
// logical JSON response. Unwrapping here lets evaluateCheck present that clean
// interface without exposing the MCP wire format to the comparator logic.
func extractTextContent(output json.RawMessage) (string, bool) {
	var items []any
	if err := json.Unmarshal(output, &items); err != nil {
		return "", false
	}

	for _, item := range items {
		obj, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if obj["type"] != "text" {
			continue
		}
		text, ok := obj["text"].(string)
		if ok && text != "" {
			return text, true
		}
	}

	return "", false
}

// evaluateCheck applies a single PollCheck against MCP tool output.
// It unwraps the MCP content array, parses the text as JSON, evaluates the
// JSONPath expression, and applies the comparator. Returns false on any error
// (bad JSON, path miss, type mismatch) — errors are treated as not-passed.
func evaluateCheck(output json.RawMessage, check model.PollCheck) bool {
	// Step 0: unwrap the MCP content array to get the tool's logical text response.
	text, ok := extractTextContent(output)
	if !ok {
		return false
	}

	// Step 1: parse the text as JSON so we can evaluate a JSONPath against it.
	parsed, err := oj.ParseString(text)
	if err != nil {
		return false
	}

	// Step 2: evaluate the JSONPath expression against the parsed value.
	expr, err := jp.ParseString(check.Path)
	if err != nil {
		return false
	}
	results := expr.Get(parsed)
	if len(results) == 0 {
		return false
	}

	// Step 3: take the first matched value and apply the comparator.
	matched := results[0]

	switch check.Comparator {
	case model.ComparatorEquals:
		return compareEqual(matched, check.Value)
	case model.ComparatorNotEquals:
		return !compareEqual(matched, check.Value)
	case model.ComparatorGreaterThan:
		return compareNumeric(matched, check.Value, func(a, b float64) bool { return a > b })
	case model.ComparatorLessThan:
		return compareNumeric(matched, check.Value, func(a, b float64) bool { return a < b })
	case model.ComparatorContains:
		return compareContains(matched, check.Value)
	}

	return false
}

// compareEqual returns true when matched and expected are equal after
// type-coercing both to the same kind (string, float64, bool).
func compareEqual(matched, expected any) bool {
	switch e := expected.(type) {
	case string:
		m, ok := matched.(string)
		return ok && m == e
	case float64:
		return toFloat64Equal(matched, e)
	case int:
		return toFloat64Equal(matched, float64(e))
	case bool:
		m, ok := matched.(bool)
		return ok && m == e
	default:
		// Fallback: compare string representations.
		return toString(matched) == toString(expected)
	}
}

// toFloat64Equal converts matched to float64 and compares it to expected.
// Returns false if matched cannot be converted to a number.
func toFloat64Equal(matched any, expected float64) bool {
	f, ok := toFloat64(matched)
	return ok && f == expected
}

// compareNumeric converts both values to float64 and applies cmp.
// Returns false if either value is not numeric.
func compareNumeric(matched, expected any, cmp func(a, b float64) bool) bool {
	m, mOk := toFloat64(matched)
	e, eOk := toFloat64(expected)
	if !mOk || !eOk {
		return false
	}
	return cmp(m, e)
}

// compareContains returns true when both matched and expected are strings and
// expected is a substring of matched.
func compareContains(matched, expected any) bool {
	mStr, mOk := matched.(string)
	eStr, eOk := expected.(string)
	if !mOk || !eOk {
		return false
	}
	return strings.Contains(mStr, eStr)
}

// toFloat64 converts a JSON-decoded numeric value to float64.
// JSON numbers decode as float64; integers from YAML decode as int or int64.
func toFloat64(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case uint64:
		return float64(n), true
	}
	return 0, false
}

// toString converts any value to its string representation for fallback comparison.
func toString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// evaluateBodyCheck applies a PollCheck against a raw JSON request body.
// Unlike evaluateCheck it parses the body directly — no MCP content-array
// unwrapping. Used for webhook payload filtering.
func evaluateBodyCheck(body []byte, check model.PollCheck) bool {
	parsed, err := oj.Parse(body)
	if err != nil {
		return false
	}
	expr, err := jp.ParseString(check.Path)
	if err != nil {
		return false
	}
	results := expr.Get(parsed)
	if len(results) == 0 {
		return false
	}
	matched := results[0]
	switch check.Comparator {
	case model.ComparatorEquals:
		return compareEqual(matched, check.Value)
	case model.ComparatorNotEquals:
		return !compareEqual(matched, check.Value)
	case model.ComparatorGreaterThan:
		return compareNumeric(matched, check.Value, func(a, b float64) bool { return a > b })
	case model.ComparatorLessThan:
		return compareNumeric(matched, check.Value, func(a, b float64) bool { return a < b })
	case model.ComparatorContains:
		return compareContains(matched, check.Value)
	}
	return false
}

// evaluateBodyChecks evaluates all checks against a raw JSON request body
// and applies the match mode. Used for webhook payload filtering.
func evaluateBodyChecks(body []byte, checks []model.PollCheck, match model.MatchMode) bool {
	for _, check := range checks {
		passed := evaluateBodyCheck(body, check)
		if match == model.MatchAny && passed {
			return true
		}
		if match == model.MatchAll && !passed {
			return false
		}
	}
	return match == model.MatchAll
}

// evaluateChecks runs all checks against pre-fetched tool results and applies
// the match mode. For MatchAll, every check must pass. For MatchAny, at least
// one must pass. Checks with a non-nil Err are treated as not-passed.
func evaluateChecks(results []checkResult, checks []model.PollCheck, match model.MatchMode) bool {
	for i, check := range checks {
		result := results[i]
		passed := result.Err == nil && evaluateCheck(result.Output, check)

		if match == model.MatchAny && passed {
			return true
		}
		if match == model.MatchAll && !passed {
			return false
		}
	}

	// For MatchAll: all passed (loop didn't return false).
	// For MatchAny: none passed (loop didn't return true).
	return match == model.MatchAll
}
