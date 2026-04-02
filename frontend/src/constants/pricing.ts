// Pricing constants for estimating dollar cost from token counts.
// Keys match the display names returned by the backend timeseries endpoint
// (e.g. "Sonnet 4", not the raw API model ID "claude-sonnet-4-6").
//
// Cost estimation uses a blended rate (average of input and output per-token
// price) because the timeseries endpoint only has a combined token count, not
// an input/output split. This is an approximation suitable for dashboard charts.
export const MODEL_PRICING: Record<string, { input: number; output: number }> = {
  'Sonnet 4':  { input: 3.00 / 1_000_000, output: 15.00 / 1_000_000 },
  'Haiku 3.5': { input: 0.80 / 1_000_000, output: 4.00 / 1_000_000 },
  'Opus 4':    { input: 15.00 / 1_000_000, output: 75.00 / 1_000_000 },
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
