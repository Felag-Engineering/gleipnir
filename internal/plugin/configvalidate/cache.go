package configvalidate

import (
	"crypto/sha256"
	"fmt"
	"sync"
)

// cache is a process-global store of compiled *Validator values. Keys are
// "<usesite>:<hex-sha256-of-json-schema-bytes>". Content-based keys give free
// de-dup across plugins that ship identical schemas and survive process-level
// hot-reloads where pointer identity would break.
var cache sync.Map

// cachedCompile returns a cached *Validator for the given usesite + JSON schema
// bytes, compiling and storing on first use.
func cachedCompile(usesite string, jsonBytes []byte) (*Validator, error) {
	sum := sha256.Sum256(jsonBytes)
	key := fmt.Sprintf("%s:%x", usesite, sum)

	if v, ok := cache.Load(key); ok {
		return v.(*Validator), nil
	}

	v, err := compile(jsonBytes)
	if err != nil {
		return nil, err
	}
	// LoadOrStore so concurrent callers share the winner; either result is valid.
	actual, _ := cache.LoadOrStore(key, v)
	return actual.(*Validator), nil
}

// ResetCache clears the process-global schema cache.
//
// Test-only. Call between subtests that share the process to avoid cross-test
// pollution from prior compilations.
func ResetCache() {
	cache.Range(func(k, _ any) bool {
		cache.Delete(k)
		return true
	})
}
