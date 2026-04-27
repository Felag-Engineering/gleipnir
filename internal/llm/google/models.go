// Package google: curated model list for the Google Gemini provider.
//
// Every entry must satisfy the #618 invariant: tool-use-capable, currently
// available, and must not throw errors simply by existing.
package google

import "github.com/felag-engineering/gleipnir/internal/llm"

// curatedModels is the display list returned by ListModels. The order here is
// the order users see in the UI — most capable first within each generation.
var curatedModels = []llm.ModelInfo{
	{Name: "gemini-3-pro-preview", DisplayName: "Gemini 3 Pro (Preview)"},
	{Name: "gemini-3-flash-preview", DisplayName: "Gemini 3 Flash (Preview)"},
	{Name: "gemini-2.5-pro", DisplayName: "Gemini 2.5 Pro"},
	{Name: "gemini-2.5-flash", DisplayName: "Gemini 2.5 Flash"},
	{Name: "gemini-2.5-flash-lite", DisplayName: "Gemini 2.5 Flash-Lite"},
}
