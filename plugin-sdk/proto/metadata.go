package proto

// CallIDMetadataKey is the gRPC metadata key the host injects on every
// host→plugin service RPC. Plugins must propagate this value back on every
// Host RPC they call from within the handler scope so the host can correlate
// Log and EmitMetric writes to the originating run, policy, and step.
//
// Spec reference: plugin-system-spec.md §8.5.
// Note: no "x-" prefix — gRPC metadata does not follow HTTP header conventions.
const CallIDMetadataKey = "gleipnir-call-id"

// InstanceTokenMetadataKey is the gRPC metadata key the plugin subprocess
// appends to every outgoing Host RPC. The value is a 256-bit random token
// assigned by the host at subprocess launch (one token per generation). The
// host verifies the token on every incoming RPC to confirm the caller is the
// expected plugin instance (spec §8.4).
//
// Token rotation: the host issues a new token on each generation start;
// the old token is revoked atomically so a killed-generation process cannot
// impersonate its successor.
const InstanceTokenMetadataKey = "gleipnir-instance-token"
