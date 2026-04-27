// Package openai: curated model list for the premium OpenAI Responses API provider.
//
// Every entry must satisfy the #618 invariant: tool-use-capable, currently
// available via the Responses API, and must not throw errors simply by existing.
//
// o3 and o4-mini are not supported: reasoning-model quirks with the Responses
// API mean we cannot guarantee the invariant for these models.
package openai

import "github.com/felag-engineering/gleipnir/internal/llm"

// curatedModels is the display list returned by ListModels. The order here is
// the order users see in the UI — most capable first within each generation.
var curatedModels = []llm.ModelInfo{
	{Name: "gpt-5", DisplayName: "GPT-5", IsReasoning: true},
	{Name: "gpt-5-mini", DisplayName: "GPT-5 Mini", IsReasoning: true},
	{Name: "gpt-5-nano", DisplayName: "GPT-5 Nano", IsReasoning: true},
	{Name: "gpt-4.1", DisplayName: "GPT-4.1"},
	{Name: "gpt-4.1-mini", DisplayName: "GPT-4.1 Mini"},
	{Name: "gpt-4.1-nano", DisplayName: "GPT-4.1 Nano"},
}

// curatedModelsByName is a map built from curatedModels for O(1) lookups by
// model name. It is the authoritative source for model metadata at runtime.
var curatedModelsByName = func() map[string]llm.ModelInfo {
	m := make(map[string]llm.ModelInfo, len(curatedModels))
	for _, model := range curatedModels {
		m[model.Name] = model
	}
	return m
}()
