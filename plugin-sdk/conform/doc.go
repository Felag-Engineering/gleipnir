// Package conform checks a running plugin server against the conformance
// checklists of the capability profiles its manifest declares.
//
// A plugin author points Run at their server and its manifest; Run selects
// the checks for each profile the manifest declares, runs them over the
// stateless 2026-07-28 MCP transport, and returns a Report with one Result
// per check. A profile the manifest does not declare contributes no checks:
// an event-source-only plugin is never asked about tools.
//
// The checklists themselves are the authority on what is checked. Each check
// carries the ID of a checklist item (TP-* in checklists/tool-provider.md),
// and checklist_test.go pins the registry to the checklist so the two cannot
// drift: an `auto` item with no check, or a check with no item, fails the
// build.
//
// Two kinds of check live here:
//
//   - Profile checks (checks_tool.go): the tool-provider profile's TP-*
//     items, per docs/developer/profile-tool-provider.md.
//   - Manifest-agreement checks (checks_manifest.go): the signed manifest
//     against what the running server serves, e.g. EV-GL-01 (event kinds).
//
// conform is a client only and imports nothing from the host. It does not
// repeat what the host does at install time: byte-level schema
// canonicalization, for one, stays there.
package conform
