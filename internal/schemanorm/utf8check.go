package schemanorm

import (
	"fmt"
	"strconv"
	"unicode/utf8"
)

// checkValidUTF8 rejects raw if it contains a raw invalid UTF-8 byte
// sequence, or a "\uXXXX" escape forming an unpaired UTF-16 surrogate,
// inside a string literal. Both checks must run against raw bytes, before
// any JSON decoding: encoding/json silently substitutes both problem
// classes with U+FFFD on decode (verified empirically -- see the package
// doc), so by the time a value has become a Go string the information
// needed to reject it is already gone.
//
// The CALL ORDER below is load-bearing: firstInvalidUTF8Byte MUST run before
// firstUnpairedSurrogateEscape. firstUnpairedSurrogateEscape tracks string
// boundaries by scanning for an unescaped '"' byte, which is only a sound way
// to find string boundaries once UTF-8 validity is already established --
// continuation bytes (0x80-0xBF) cannot be mistaken for the ASCII '"' or '\'
// bytes the scan keys off, but only because well-formed UTF-8 never produces
// an ASCII byte as part of a multi-byte sequence. Reordering these two calls,
// or merging them into a single pass, would let raw invalid UTF-8 desync
// firstUnpairedSurrogateEscape's string-boundary tracking and open a hole.
func checkValidUTF8(raw []byte) error {
	if off := firstInvalidUTF8Byte(raw); off >= 0 {
		return newErr(ErrInvalidUTF8, "", fmt.Sprintf("invalid UTF-8 byte sequence at offset %d", off))
	}
	if off := firstUnpairedSurrogateEscape(raw); off >= 0 {
		return newErr(ErrInvalidUTF8, "", fmt.Sprintf("unpaired UTF-16 surrogate escape at offset %d", off))
	}
	return nil
}

// firstInvalidUTF8Byte returns the byte offset of the first invalid UTF-8
// encoding in raw, or -1 if raw is entirely valid UTF-8. utf8.DecodeRune
// reports an invalid encoding as (RuneError, 1); a legitimately-encoded
// U+FFFD character is (RuneError, 3), so checking size == 1 alongside
// r == RuneError distinguishes "invalid bytes" from "the document actually
// contains a literal replacement character".
func firstInvalidUTF8Byte(raw []byte) int {
	for i := 0; i < len(raw); {
		r, size := utf8.DecodeRune(raw[i:])
		if r == utf8.RuneError && size == 1 {
			return i
		}
		i += size
	}
	return -1
}

// firstUnpairedSurrogateEscape scans raw for "\uXXXX" escape sequences
// inside string literals and returns the byte offset of the first unpaired
// UTF-16 surrogate: a high surrogate (U+D800-U+DBFF) not immediately
// followed by a low-surrogate escape (U+DC00-U+DFFF), or a low surrogate
// with no preceding high surrogate. Returns -1 if every surrogate escape is
// properly paired (including the case where raw contains none at all).
//
// Escape sequences only occur inside JSON string literals, so this scans
// the whole byte stream without separately tracking "am I inside a
// structural token", but it does track "am I inside a string" (toggled on
// an unescaped '"') so that plain '\' bytes elsewhere in the document (which
// would make it invalid JSON for reasons this function does not need to
// diagnose -- the decoder catches that) do not confuse the scan. Escaped
// backslashes ("\\") are consumed two bytes at a time so a literal "\\u0041"
// (a backslash followed by the four characters "u0041") is never mistaken
// for a "A" escape.
func firstUnpairedSurrogateEscape(raw []byte) int {
	inString := false
	pendingHigh := false
	pendingHighPos := -1

	for i := 0; i < len(raw); {
		b := raw[i]
		if !inString {
			if b == '"' {
				inString = true
			}
			i++
			continue
		}

		switch {
		case b == '"':
			if pendingHigh {
				return pendingHighPos
			}
			inString = false
			i++

		case b == '\\' && i+1 < len(raw) && raw[i+1] == 'u' && i+6 <= len(raw):
			unit, err := strconv.ParseUint(string(raw[i+2:i+6]), 16, 32)
			if err != nil {
				// Malformed hex digits: not this function's job to
				// diagnose -- the JSON decoder will report it as a syntax
				// error. Returning -1 here means "no unpaired surrogate
				// found", abandoning the rest of the scan; that is only
				// fail-closed BY COMPOSITION, not by construction --
				// validateStructure (see validate.go) independently rejects
				// any input that is not syntactically valid JSON, including
				// this one, so the malformed escape can never reach
				// decodeJSON regardless of what this function returns.
				return -1
			}
			switch {
			case pendingHigh:
				if unit < 0xDC00 || unit > 0xDFFF {
					return pendingHighPos // high surrogate not followed by a low surrogate
				}
				pendingHigh = false
			case unit >= 0xD800 && unit <= 0xDBFF:
				pendingHigh = true
				pendingHighPos = i
			case unit >= 0xDC00 && unit <= 0xDFFF:
				return i // lone low surrogate, no preceding high
			}
			i += 6

		case b == '\\' && i+1 < len(raw):
			if pendingHigh {
				return pendingHighPos // high surrogate not immediately followed by another escape
			}
			i += 2 // skip the backslash and the escaped character

		default:
			if pendingHigh {
				return pendingHighPos // high surrogate not immediately followed by an escape
			}
			i++
		}
	}

	if pendingHigh {
		return pendingHighPos
	}
	return -1
}
