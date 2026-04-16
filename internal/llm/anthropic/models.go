// Package anthropic: curated model list for the Anthropic Claude provider.
//
// Every entry in curatedModels must satisfy the #618 invariant: tool-use-capable,
// currently available, and must not throw errors simply by existing.
// 3.x models are intentionally excluded from the display list.
//
// validationAliases holds dated aliases that ValidateModelName must accept but
// that are NOT shown in the UI. This preserves backward compatibility for stored
// policies that reference dated model pins (e.g. schemas/policy.yaml:49).
package anthropic

import "github.com/rapp992/gleipnir/internal/llm"

// curatedModels is the display list returned by ListModels. The order here is
// the order users see in the UI — most capable first within each generation.
var curatedModels = []llm.ModelInfo{
	{Name: "claude-opus-4-6", DisplayName: "Claude Opus 4.6"},
	{Name: "claude-sonnet-4-6", DisplayName: "Claude Sonnet 4.6"},
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
