// Pricing constants for estimating dollar cost from token counts.
//
// Keys must match the display names returned by the backend timeseries endpoint
// (internal/api/modelnames.go ModelDisplayNames values), not raw API model IDs.
// For example, "Sonnet 4.6" matches api ID "claude-sonnet-4-6".
//
// Cost estimation uses a blended rate (average of input and output per-token
// price) because the timeseries endpoint only has a combined token count, not
// an input/output split. Prices are approximate and sourced from provider
// public pricing pages — suitable for dashboard charts, not billing.
export const MODEL_PRICING: Record<string, { input: number; output: number }> = {
  // Anthropic curated models.
  'Opus 4.7':   { input: 5.00 / 1_000_000,  output: 25.00 / 1_000_000 },
  'Opus 4.6':   { input: 5.00 / 1_000_000,  output: 25.00 / 1_000_000 },
  'Sonnet 4.6': { input: 3.00 / 1_000_000,  output: 15.00 / 1_000_000 },
  'Haiku 4.5':  { input: 0.80 / 1_000_000,  output: 4.00 / 1_000_000 },
  'Opus 4.5':   { input: 15.00 / 1_000_000, output: 75.00 / 1_000_000 },
  'Sonnet 4.5': { input: 3.00 / 1_000_000,  output: 15.00 / 1_000_000 },

  // Anthropic legacy/alias display names — kept for historical run data.
  'Sonnet 4':  { input: 3.00 / 1_000_000,  output: 15.00 / 1_000_000 },
  'Haiku 3.5': { input: 0.80 / 1_000_000,  output: 4.00 / 1_000_000 },
  'Opus 4':    { input: 15.00 / 1_000_000, output: 75.00 / 1_000_000 },

  // Google curated models.
  'Gemini 3 Pro':          { input: 1.25 / 1_000_000,   output: 10.00 / 1_000_000 },
  'Gemini 3 Flash':        { input: 0.15 / 1_000_000,   output: 0.60 / 1_000_000 },
  'Gemini 2.5 Pro':        { input: 1.25 / 1_000_000,   output: 10.00 / 1_000_000 },
  'Gemini 2.5 Flash':      { input: 0.15 / 1_000_000,   output: 0.60 / 1_000_000 },
  'Gemini 2.5 Flash-Lite': { input: 0.075 / 1_000_000,  output: 0.30 / 1_000_000 },

  // Google legacy models — kept for historical run cost charts.
  'Gemini 2.0 Flash':      { input: 0.10 / 1_000_000,   output: 0.40 / 1_000_000 },
  'Gemini 2.0 Flash-Lite': { input: 0.075 / 1_000_000,  output: 0.30 / 1_000_000 },

  // OpenAI curated models.
  'GPT-5':        { input: 2.00 / 1_000_000, output: 8.00 / 1_000_000 },
  'GPT-5 Mini':   { input: 0.40 / 1_000_000, output: 1.60 / 1_000_000 },
  'GPT-5 Nano':   { input: 0.10 / 1_000_000, output: 0.40 / 1_000_000 },
  'GPT-4.1':      { input: 2.00 / 1_000_000, output: 8.00 / 1_000_000 },
  'GPT-4.1 Mini': { input: 0.40 / 1_000_000, output: 1.60 / 1_000_000 },
  'GPT-4.1 Nano': { input: 0.10 / 1_000_000, output: 0.40 / 1_000_000 },
}

// estimateCost converts a token count to an approximate dollar cost for a
// named model. Returns 0 for unknown models so unknown entries are still
// included in the chart without inflating costs.
export function estimateCost(displayName: string, tokens: number): number {
  const rates = MODEL_PRICING[displayName]
  if (!rates) return 0
  const blended = (rates.input + rates.output) / 2
  return tokens * blended
}
