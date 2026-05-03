package proto

// CallIDMetadataKey is the gRPC metadata key the host injects on every
// host→plugin service RPC. Plugins must propagate this value back on every
// Host RPC they call from within the handler scope so the host can correlate
// Log, EmitMetric, and WriteAuditStep writes to the originating run, policy,
// and step.
//
// Spec reference: plugin-system-spec.md §8.5.
// Note: no "x-" prefix — gRPC metadata does not follow HTTP header conventions.
const CallIDMetadataKey = "gleipnir-call-id"
