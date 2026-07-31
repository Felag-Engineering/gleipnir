package schemanorm

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// validateStructure walks raw's token stream once, enforcing what a plain
// json.Decode into a generic tree cannot: no duplicate object key at any
// depth (see the package doc's "duplicate keys" sharp edge), plus the
// MaxDepth/MaxNodes bounds. It does not build any output value -- it exists
// purely to fail closed before decodeJSON silently accepts a document that
// changed meaning on the way in.
func validateStructure(raw json.RawMessage, lim Limits) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()

	tok, err := dec.Token()
	if err != nil {
		return newErr(ErrInvalidJSON, "", fmt.Sprintf("%v", err))
	}

	s := &structureScanner{lim: lim}
	if err := s.value(dec, tok, 1, ""); err != nil {
		return err
	}

	if _, err := dec.Token(); err != io.EOF {
		return newErr(ErrInvalidJSON, "", "trailing content after top-level value")
	}
	return nil
}

type structureScanner struct {
	lim   Limits
	nodes int
}

// value accounts tok as one node against MaxNodes/MaxDepth, then descends
// into it if it opens an object or array.
func (s *structureScanner) value(dec *json.Decoder, tok json.Token, depth int, ptr string) error {
	s.nodes++
	if s.nodes > s.lim.MaxNodes {
		return newErr(ErrNodeCountExceeded, ptr, fmt.Sprintf("exceeds limit of %d", s.lim.MaxNodes))
	}
	if depth > s.lim.MaxDepth {
		return newErr(ErrDepthExceeded, ptr, fmt.Sprintf("exceeds limit of %d", s.lim.MaxDepth))
	}

	delim, ok := tok.(json.Delim)
	if !ok {
		return nil // string / json.Number / bool / nil: no further descent
	}
	switch delim {
	case '{':
		return s.object(dec, depth, ptr)
	case '[':
		return s.array(dec, depth, ptr)
	}
	return nil
}

func (s *structureScanner) object(dec *json.Decoder, depth int, ptr string) error {
	seen := make(map[string]bool)
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return newErr(ErrInvalidJSON, ptr, fmt.Sprintf("%v", err))
		}
		key := keyTok.(string)
		if seen[key] {
			return newErr(ErrDuplicateKey, ptr, fmt.Sprintf("duplicate object key %q", key))
		}
		seen[key] = true

		valTok, err := dec.Token()
		if err != nil {
			return newErr(ErrInvalidJSON, ptr, fmt.Sprintf("%v", err))
		}
		if err := s.value(dec, valTok, depth+1, ptr+"/"+escapePointer(key)); err != nil {
			return err
		}
	}
	if _, err := dec.Token(); err != nil { // consume the closing '}'
		return newErr(ErrInvalidJSON, ptr, fmt.Sprintf("%v", err))
	}
	return nil
}

func (s *structureScanner) array(dec *json.Decoder, depth int, ptr string) error {
	for i := 0; dec.More(); i++ {
		tok, err := dec.Token()
		if err != nil {
			return newErr(ErrInvalidJSON, ptr, fmt.Sprintf("%v", err))
		}
		if err := s.value(dec, tok, depth+1, fmt.Sprintf("%s/%d", ptr, i)); err != nil {
			return err
		}
	}
	if _, err := dec.Token(); err != nil { // consume the closing ']'
		return newErr(ErrInvalidJSON, ptr, fmt.Sprintf("%v", err))
	}
	return nil
}

// escapePointer escapes an object key for use as an RFC 6901 JSON Pointer
// reference token: "~" -> "~0" (must run first, or a literal "~1" in the
// key would be re-escaped into "~01"), then "/" -> "~1".
func escapePointer(key string) string {
	key = strings.ReplaceAll(key, "~", "~0")
	key = strings.ReplaceAll(key, "/", "~1")
	return key
}
