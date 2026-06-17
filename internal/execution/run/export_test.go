package run

// MakeConcurrencyCheckErrorForTest wraps err in a concurrencyCheckError so
// tests in external packages can exercise IsConcurrencyCheckError without
// naming the unexported type.
func MakeConcurrencyCheckErrorForTest(err error) error {
	return &concurrencyCheckError{err: err}
}

// MakeEnqueueErrorForTest wraps err in an enqueueError so tests in external
// packages can exercise IsEnqueueError without naming the unexported type.
func MakeEnqueueErrorForTest(err error) error {
	return &enqueueError{err: err}
}
