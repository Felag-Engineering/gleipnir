package events

import "time"

// Event is the author-facing event a plugin publishes through
// Handler.Publish or Buffer.Publish.
//
// It is deliberately NOT a CloudEvents envelope. "source", "specversion",
// and "gleipnirseq" are the SDK's to fill in, never the author's: an author
// who could set gleipnirseq could forge the host's own resume-cursor
// bookkeeping (doc §7.3), and a plugin process restarting with its own idea
// of "source" would let one instance impersonate another's event stream.
type Event struct {
	// Type is the event kind, matching a Kind.Kind this handler declared.
	// Rendered as the CloudEvents "type".
	Type string

	// ID is the CloudEvents "id" — the dedup key consumed downstream by
	// the host's dedup layer. Callers should assign a value that is unique
	// per logical occurrence (a webhook delivery ID, a source-side event
	// ID); redelivery of the same ID is expected and is the downstream
	// consumer's problem to dedup (doc §7.3), not this package's.
	ID string

	// Time is the event's own timestamp, rendered as the CloudEvents
	// "time". The zero value is filled in with the publish-time clock by
	// whichever Buffer accepts the event.
	Time time.Time

	// Data is the event payload, marshaled as the CloudEvents "data".
	// Host-captured; per doc §5 it reaches model context only if a policy
	// author explicitly templates it into a task prompt.
	Data any
}
