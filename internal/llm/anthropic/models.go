// Package anthropic: curated model list for the Anthropic Claude provider.
//
// Every entry in curatedModels must satisfy the #618 invariant: tool-use-capable,
// currently available, and must not throw errors simply by existing.
// 3.x models are intentionally excluded from the display list.
//
// IsReasoning must be true for every 5.x model: thinking is always on for that
// generation (it cannot be disabled), and IsReasoning is what selects adaptive
// thinking and the larger thinking-aware max_tokens default in client.go. A 5.x
// entry left at false would run with a 4096-token ceiling that thinking alone
// can exhaust, failing the run on max_tokens.
//
// validationAliases holds dated aliases that ValidateModelName must accept but
// that are NOT shown in the UI. This preserves backward compatibility for stored
// policies that reference dated model pins (e.g. schemas/policy.yaml:49).
package anthropic

import "github.com/felag-engineering/gleipnir/internal/llm"

// curatedModels is the display list returned by ListModels. The order here is
// the order users see in the UI — most capable first within each generation.
var curatedModels = []llm.ModelInfo{
	{Name: "claude-fable-5-1", DisplayName: "Claude Fable 5.1", IsReasoning: true},
	{Name: "claude-fable-5", DisplayName: "Claude Fable 5", IsReasoning: true},
	{Name: "claude-opus-5-5", DisplayName: "Claude Opus 5.5", IsReasoning: true},
	{Name: "claude-opus-5", DisplayName: "Claude Opus 5", IsReasoning: true},
	{Name: "claude-sonnet-5-5", DisplayName: "Claude Sonnet 5.5", IsReasoning: true},
	{Name: "claude-sonnet-5", DisplayName: "Claude Sonnet 5", IsReasoning: true},
	{Name: "claude-opus-4-8", DisplayName: "Claude Opus 4.8", IsReasoning: true},
	{Name: "claude-opus-4-7", DisplayName: "Claude Opus 4.7", IsReasoning: true},
	{Name: "claude-opus-4-6", DisplayName: "Claude Opus 4.6", IsReasoning: true},
	{Name: "claude-sonnet-4-6", DisplayName: "Claude Sonnet 4.6", IsReasoning: true},
	{Name: "claude-haiku-4-5", DisplayName: "Claude Haiku 4.5"},
	{Name: "claude-opus-4-5", DisplayName: "Claude Opus 4.5"},
	{Name: "claude-sonnet-4-5", DisplayName: "Claude Sonnet 4.5"},
}

// curatedModelsByName is a lookup index built from curatedModels at init time.
// ValidateModelName uses it for O(1) lookup instead of iterating the slice.
var curatedModelsByName map[string]llm.ModelInfo

func init() {
	curatedModelsByName = make(map[string]llm.ModelInfo, len(curatedModels))
	for _, m := range curatedModels {
		curatedModelsByName[m.Name] = m
	}
}

// validationAliases maps dated model aliases to their human-readable label.
// These are accepted by ValidateModelName but not returned by ListModels.
// Add new entries here when stored policies use a dated pin that would otherwise
// be rejected at run-launch time.
var validationAliases = map[string]string{
	"claude-sonnet-4-20250514": "Claude Sonnet 4",
}
