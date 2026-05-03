// Package manifest provides types and the canonical YAML marshaller for the
// Gleipnir plugin manifest format.
//
// A plugin manifest (manifest.yaml) declares which services the binary
// implements, the credential strategy, declared tools, event kinds, channels,
// and JSON schemas for per-instance configuration.
//
// The canonical Marshal function produces deterministic YAML (sorted keys,
// 2-space indent) so that signing hashes the same bytes for the same Go
// declarations. Unmarshal accepts both canonical and non-canonical YAML.
//
// See docs/developer/plugin-system-spec.md §5, §14.3 for the full manifest
// specification.
package manifest
