package trigger

import "context"

// stubDefaultModelResolver is a test double for the defaultModelResolver
// interface. Internal-package tests (notify_test.go) use this because they
// cannot share symbols with the external-package tests in testhelpers_test.go.
type stubDefaultModelResolver struct {
	provider string
	name     string
	err      error
}

func (s stubDefaultModelResolver) GetSystemDefault(_ context.Context) (string, string, error) {
	return s.provider, s.name, s.err
}
