package events

// Kind describes one event kind a server declares it may emit, per the
// events/discover half of the contract
// (docs/developer/extension-io-gleipnir-events.md §6).
type Kind struct {
	// Kind is the event-kind identifier. Echoed verbatim as the CloudEvents
	// "type" on every event of this kind (doc §7.3).
	Kind string

	// Guidance is server-authored prose shown to an operator binding a
	// policy to this kind. Untrusted, server-controlled text — the host
	// renders it as content, never as instructions.
	Guidance string

	// BindingSchema is the JSON Schema for the typed binding filters a
	// policy may set on this kind (ADR-048). Marshaled to JSON as-is by
	// events/discover; pass a value encoding/json can marshal (e.g. a
	// map[string]any or a json.RawMessage), or nil to declare no filters.
	BindingSchema any

	// Operators is the ADR-052 allowed-operator set per binding field: a
	// map from field name to the operator names a policy may use against
	// it (e.g. {"priority": {"eq", "in"}}). Carried on the wire; ADR-052
	// decided operator selectability but deferred implementation, so this
	// is not consumed by any shipped host client yet.
	Operators map[string][]string
}

// eventKindWire is the wire shape of one events/discover result entry
// (doc §6).
type eventKindWire struct {
	Kind          string              `json:"kind"`
	Guidance      string              `json:"guidance,omitempty"`
	BindingSchema any                 `json:"binding_schema,omitempty"`
	Operators     map[string][]string `json:"operators,omitempty"`
}

// discoverResult renders the events/discover result for kinds. Declaring
// kinds once as a []Kind and handing that same slice to NewHandler is what
// keeps this rendering and the handler's actual behavior from drifting
// apart — there is no second copy of the kind list for this function to
// disagree with.
func discoverResult(kinds []Kind) map[string]any {
	wire := make([]eventKindWire, len(kinds))
	for i, k := range kinds {
		wire[i] = eventKindWire(k)
	}
	return map[string]any{"kinds": wire}
}
