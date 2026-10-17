package conform

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/felag-engineering/gleipnir/plugin-sdk/manifestv2"
)

// Target is a running plugin server and the manifest it is checked against.
type Target struct {
	// BaseURL is the server's MCP endpoint, e.g. "http://127.0.0.1:8080/".
	BaseURL string

	// Header is sent on every request in addition to the transport headers,
	// e.g. an Authorization header for a server behind one. Optional.
	Header http.Header

	// Manifest is the plugin's v2 manifest. Its declared profiles select the
	// checks, and the manifest-agreement checks compare it to the server.
	Manifest *manifestv2.Manifest

	// HTTPClient sends the requests. Optional; defaults to a client with a
	// per-request timeout (see newClient).
	HTTPClient *http.Client
}

// Status is the outcome of one check.
type Status string

const (
	// StatusPass: the server meets the checklist item.
	StatusPass Status = "pass"
	// StatusFail: the server does not meet it; Result.Detail says how.
	StatusFail Status = "fail"
	// StatusSkip: the check could not run because something it depends on
	// failed (usually discovery), which that check's own Result reports.
	StatusSkip Status = "skip"
)

// Result is one check's outcome.
type Result struct {
	ID      string
	Profile string
	Status  Status
	Detail  string
}

// Report is the outcome of a run.
type Report struct {
	// Profiles are the profiles the manifest declares, in manifest order.
	Profiles []string
	// Results holds one Result per selected check, in ID order.
	Results []Result
}

// Passed reports whether no check failed. A skipped check is not a pass on
// its own, but it is always the consequence of a failure reported elsewhere.
func (r Report) Passed() bool {
	for _, res := range r.Results {
		if res.Status != StatusPass {
			return false
		}
	}
	return true
}

// String renders the report one check per line, failures with their detail.
func (r Report) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "profiles: %s\n", strings.Join(r.Profiles, ", "))
	for _, res := range r.Results {
		fmt.Fprintf(&b, "%-4s %-12s %s", res.Status, res.ID, res.Profile)
		if res.Detail != "" {
			fmt.Fprintf(&b, ": %s", res.Detail)
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// Check is one conformance check, tied to one checklist item.
type Check struct {
	// ID is the checklist item ID, e.g. "TP-NAME-01".
	ID string
	// Profile is the manifest profile name that selects the check.
	Profile string
	// Run returns nil when the server meets the item, an error describing the
	// violation when it does not, and an error wrapping errSkip when a
	// prerequisite failed.
	Run func(ctx context.Context, s *Session) error
}

// errSkip marks a check that could not run because a prerequisite failed.
var errSkip = errors.New("skipped")

// skipf returns an error that makes a check report StatusSkip.
func skipf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", errSkip, fmt.Sprintf(format, args...))
}

// registry is every check, in ID order. Each checks_*.go file declares its
// checks in one slice, so a profile's whole set reads in one place.
func registry() []Check {
	all := slices.Concat(toolProviderChecks, manifestChecks)
	slices.SortFunc(all, func(a, b Check) int { return strings.Compare(a.ID, b.ID) })
	return all
}

// Checks returns every registered check, in ID order.
func Checks() []Check {
	return registry()
}

// Run checks t against every check whose profile its manifest declares.
//
// It never returns early: every selected check runs and reports, so one run
// tells an author everything to fix. Discovery results are shared across
// checks through one Session, so the server sees each discovery request once.
func Run(ctx context.Context, t Target) (Report, error) {
	if t.Manifest == nil {
		return Report{}, errors.New("conform: target has no manifest; the manifest's declared profiles select the checks")
	}
	if t.BaseURL == "" {
		return Report{}, errors.New("conform: target has no base URL")
	}

	declared := t.Manifest.Gleipnir.Profiles.Declared()
	s := newSession(t)
	report := Report{Profiles: declared}
	for _, c := range registry() {
		if !slices.Contains(declared, c.Profile) {
			continue
		}
		report.Results = append(report.Results, runCheck(ctx, s, c))
	}
	return report, nil
}

func runCheck(ctx context.Context, s *Session, c Check) Result {
	res := Result{ID: c.ID, Profile: c.Profile, Status: StatusPass}
	err := c.Run(ctx, s)
	switch {
	case err == nil:
	case errors.Is(err, errSkip):
		res.Status = StatusSkip
		res.Detail = err.Error()
	default:
		res.Status = StatusFail
		res.Detail = err.Error()
	}
	return res
}
