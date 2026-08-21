package hostendpoint

import (
	"fmt"
	"sort"
	"strings"

	"github.com/felag-engineering/gleipnir/internal/toolregistry"
)

// AssertHostPlane enforces the spec §8 host-plane invariant against the
// shared tool-namespace registry: no host-endpoint tool name may be
// registered there, because everything in that registry is discoverable and
// grantable to an agent, and a grantable host tool is an ADR-001 break.
//
// main.go calls this after the plugin runtime has made its reservations and
// refuses to start on an error. Failing at boot rather than logging is
// deliberate and follows the posture #871 established for the policy
// service: a check that nothing asserts is a check that silently stops
// running, and this one guards the capability boundary itself.
//
// Two shapes count as a leak:
//   - an exact host tool name ("host/log"), which would mean host-plane
//     registration happened directly; and
//   - a dot-name whose tool part is a host tool name ("slack.host/log"),
//     which would mean a source is offering a host tool for granting.
//
// A registry entry merely *containing* "host" ("slack.hostname_lookup") is
// not a leak — the match is on the full tool part, and ToolNamePrefix's `/`
// keeps that unambiguous (see tools.go).
func AssertHostPlane(reg *toolregistry.Registry) error {
	if reg == nil {
		// A nil registry means no shared namespace exists, so nothing is
		// discoverable and nothing can have leaked. Tests that construct a
		// bare server hit this; production always has the arbiter.
		return nil
	}

	hostNames := make(map[string]bool, len(ToolNames()))
	for _, name := range ToolNames() {
		hostNames[name] = true
	}

	var offenders []string
	for dotName, src := range reg.Snapshot() {
		toolPart := dotName
		if i := strings.LastIndex(dotName, "."); i >= 0 {
			toolPart = dotName[i+1:]
		}
		if hostNames[dotName] || hostNames[toolPart] {
			offenders = append(offenders, fmt.Sprintf("%s (owned by %s)", dotName, src.String()))
		}
	}
	if len(offenders) == 0 {
		return nil
	}
	sort.Strings(offenders)
	return fmt.Errorf(
		"host-plane invariant violated: host-endpoint tool names are registered in the shared tool namespace and would be grantable to agents: %s",
		strings.Join(offenders, ", "))
}
