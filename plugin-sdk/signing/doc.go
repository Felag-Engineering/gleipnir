// Package signing implements a Minisign-compatible Ed25519 signing library for
// Gleipnir plugin bundles.
//
// This is the single source of truth for the Minisign format used in both the
// gleipnir-plugin CLI (sign/keygen/package subcommands) and the host-side
// plugin loader (#186). Keeping one implementation in plugin-sdk ensures both
// sides stay format-compatible.
//
// Signing scheme (spec §5.2):
//   - Signed payload = sha256(binary) || sha256(manifest) — 64 bytes total.
//   - Use PluginPayload to compute the payload from raw bytes.
//   - Non-prehashed Ed25519 (SigAlgED25519, "Ed") is the only produced format.
//
// Bundle layout (spec §14.5):
//   - <name>-<version>.tar.gz contains binary, manifest.yaml,
//     <manifest.Name>.minisig, signing.pub, and optional sbom.cyclonedx.json.
//
// This code is fresh-written (not vendored from jedisct1/go-minisign).
// It produces and parses upstream-compatible .minisig and signing.pub files.
package signing
