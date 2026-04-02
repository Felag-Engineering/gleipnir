package api

// ModelDisplayNames maps API model IDs to human-friendly display names.
// Used by the timeseries handler to return display names in cost_by_model so
// the frontend receives "Sonnet 4" rather than "claude-sonnet-4-6".
var ModelDisplayNames = map[string]string{
	"claude-sonnet-4-6":         "Sonnet 4",
	"claude-sonnet-4-20250514":  "Sonnet 4",
	"claude-haiku-3-5-20241022": "Haiku 3.5",
	"claude-opus-4-20250515":    "Opus 4",
	"claude-opus-4-6":           "Opus 4",
}

// GetModelDisplayName returns the display name for a model API ID.
// Falls back to the raw API ID when no mapping exists, so unknown models
// still appear in the cost chart rather than being silently dropped.
func GetModelDisplayName(apiModelID string) string {
	if name, ok := ModelDisplayNames[apiModelID]; ok {
		return name
	}
	return apiModelID
}
