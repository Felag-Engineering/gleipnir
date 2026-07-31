package schemanorm

import (
	"errors"
	"fmt"
	"strings"
)

// Sentinel error kinds returned (wrapped inside *Error) by Normalize and
// NormalizeWithLimits. Callers classify a failure with errors.Is against one
// of these; errors.As(&*Error) recovers the Pointer/Detail context.
var (
	// ErrInvalidJSON means raw itself could not be decoded as JSON (empty
	// input, malformed syntax, or trailing content after the top-level
	// value).
	ErrInvalidJSON = errors.New("schemanorm: input is not valid JSON")

	// ErrDuplicateKey means some object in raw declares the same key twice.
	// encoding/json's own decode is silently last-wins on a duplicate key,
	// which would make Normalize's output depend on which duplicate a
	// validator happens to keep -- see the package doc's "duplicate keys"
	// sharp edge.
	ErrDuplicateKey = errors.New("schemanorm: input contains a duplicate object key")

	// ErrInvalidUTF8 means raw contains either a raw invalid UTF-8 byte
	// sequence, or a "\uXXXX" escape forming an unpaired UTF-16 surrogate,
	// inside a string literal. encoding/json silently substitutes both with
	// U+FFFD on decode, changing the string's value -- see the package
	// doc's "invalid UTF-8 / lone surrogates" sharp edge.
	ErrInvalidUTF8 = errors.New("schemanorm: input contains invalid UTF-8")

	// ErrByteSizeExceeded means len(raw) exceeded Limits.MaxBytes. Checked
	// before any JSON parsing.
	ErrByteSizeExceeded = errors.New("schemanorm: input exceeds maximum byte size")

	// ErrDepthExceeded means the input's object/array nesting exceeded
	// Limits.MaxDepth.
	ErrDepthExceeded = errors.New("schemanorm: maximum depth exceeded")

	// ErrNodeCountExceeded means the total number of JSON values in the
	// input exceeded Limits.MaxNodes.
	ErrNodeCountExceeded = errors.New("schemanorm: maximum node count exceeded")
)

// Error is the concrete error type returned by Normalize and
// NormalizeWithLimits. Kind is always one of the sentinels above; Pointer is
// an RFC 6901 JSON Pointer into the input document ("" denotes the root);
// Detail is a human-readable description of the specific violation.
type Error struct {
	Kind    error
	Pointer string
	Detail  string
}

func newErr(kind error, pointer, detail string) *Error {
	return &Error{Kind: kind, Pointer: pointer, Detail: detail}
}

// Error implements the error interface: "schemanorm: <kind text> at
// <pointer>: <detail>".
//
// Pointer is rendered with %q, not %s: it is built from raw object-key text
// (see escapePointer in validate.go), and a JSON string key may legally
// contain control characters via a "\u00XX" escape -- a key containing an
// escaped newline would otherwise forge a second line in whatever log this
// error is written to, and a key containing an escaped ESC ("\u001b") would
// inject a raw ANSI control sequence into an operator's terminal. %q quotes
// and escapes those bytes instead of passing them through verbatim.
func (e *Error) Error() string {
	kind := strings.TrimPrefix(e.Kind.Error(), "schemanorm: ")
	return fmt.Sprintf("schemanorm: %s at %s: %s", kind, displayPointer(e.Pointer), e.Detail)
}

// Unwrap returns e.Kind so callers can use errors.Is(err, schemanorm.ErrXxx).
func (e *Error) Unwrap() error {
	return e.Kind
}

// displayPointer renders an RFC 6901 pointer for the Error() message: the
// empty string (root) as "<root>", anything else quoted with %q (see the
// Error method doc for why quoting -- not raw %s -- is required here).
func displayPointer(ptr string) string {
	if ptr == "" {
		return "<root>"
	}
	return fmt.Sprintf("%q", ptr)
}
