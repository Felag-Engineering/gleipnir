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
	// GPT-6 family: Astra is the flagship, Sol the everyday tier, Luna the
	// high-volume tier. GPT-6.1 Sol is a point release of Sol only.
	{Name: "gpt-6-astra", DisplayName: "GPT-6 Astra", IsReasoning: true},
	{Name: "gpt-6.1-sol", DisplayName: "GPT-6.1 Sol", IsReasoning: true},
	{Name: "gpt-6-sol", DisplayName: "GPT-6 Sol", IsReasoning: true},
	{Name: "gpt-6-luna", DisplayName: "GPT-6 Luna", IsReasoning: true},
	{Name: "gpt-5.6-sol", DisplayName: "GPT-5.6 Sol", IsReasoning: true},
	{Name: "gpt-5.6-terra", DisplayName: "GPT-5.6 Terra", IsReasoning: true},
	{Name: "gpt-5.6-luna", DisplayName: "GPT-5.6 Luna", IsReasoning: true},
	{Name: "gpt-5.5", DisplayName: "GPT-5.5", IsReasoning: true},
	{Name: "gpt-5.4", DisplayName: "GPT-5.4", IsReasoning: true},
	{Name: "gpt-5.4-mini", DisplayName: "GPT-5.4 Mini", IsReasoning: true},
	{Name: "gpt-5.4-nano", DisplayName: "GPT-5.4 Nano", IsReasoning: true},
	{Name: "gpt-5", DisplayName: "GPT-5", IsReasoning: true},
	{Name: "gpt-5-mini", DisplayName: "GPT-5 Mini", IsReasoning: true},
	{Name: "gpt-5-nano", DisplayName: "GPT-5 Nano", IsReasoning: true},
	{Name: "gpt-4.1", DisplayName: "GPT-4.1"},
	{Name: "gpt-4.1-mini", DisplayName: "GPT-4.1 Mini"},
	// OpenAI shuts gpt-4.1-nano down on 2026-10-23; remove it then (keep its
	// display name in internal/http/api/modelnames.go for historical runs).
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
