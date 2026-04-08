// Package openai: curated model list for the premium OpenAI Responses API provider.
//
// Every entry must satisfy the #618 invariant: tool-use-capable, currently
// available via the Responses API, and must not throw errors simply by existing.
//
// o3 and o4-mini are intentionally excluded: reasoning-model quirks mean we
// cannot guarantee the invariant. Add them here once verified.
package openai

import "github.com/rapp992/gleipnir/internal/llm"

// curatedModels is the display list returned by ListModels. The order here is
// the order users see in the UI — most capable first within each generation.
var curatedModels = []llm.ModelInfo{
	{Name: "gpt-5", DisplayName: "GPT-5"},
	{Name: "gpt-5-mini", DisplayName: "GPT-5 Mini"},
	{Name: "gpt-5-nano", DisplayName: "GPT-5 Nano"},
	{Name: "gpt-4.1", DisplayName: "GPT-4.1"},
	{Name: "gpt-4.1-mini", DisplayName: "GPT-4.1 Mini"},
	{Name: "gpt-4.1-nano", DisplayName: "GPT-4.1 Nano"},
}
