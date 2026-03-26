package agent

import "github.com/rapp992/gleipnir/internal/event"

// Publisher is an alias for event.Publisher, kept for backward compatibility
// while callers migrate to importing event.Publisher directly.
type Publisher = event.Publisher
