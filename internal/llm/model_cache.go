package llm

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

// ModelCache stores a fetched-and-cached map of model name → display name for
// a single provider. It is safe for concurrent use.
type ModelCache struct {
	providerName string
	// isReasoning, when set, classifies each cached model's IsReasoning flag in
	// ListModels. It is nil for providers that fetch an opaque model list with no
	// way to tell reasoning models apart — those report IsReasoning=false.
	isReasoning func(modelName string) bool
	mu          sync.RWMutex
	cache       map[string]string // model name → display name
	err         error
	loaded      bool
}

// NewModelCache returns a ModelCache that will use providerName in error
// messages (e.g. "Anthropic", "Google"). Models listed from this cache always
// report IsReasoning=false; use NewModelCacheWithReasoning to classify them.
func NewModelCache(providerName string) ModelCache {
	return ModelCache{providerName: providerName}
}

// NewModelCacheWithReasoning returns a ModelCache that classifies each model's
// IsReasoning flag via the isReasoning predicate when building ListModels output.
// Providers (e.g. openaicompat) that fetch a flat model list and infer reasoning
// support from the model ID supply their heuristic here so the admin model picker
// flags reasoning-capable backends correctly. A nil predicate behaves exactly
// like NewModelCache.
func NewModelCacheWithReasoning(providerName string, isReasoning func(modelName string) bool) ModelCache {
	return ModelCache{providerName: providerName, isReasoning: isReasoning}
}

// LoadOnce fetches models via the provided closure if not already loaded.
// The write lock is held for the entire check-fetch-store sequence to prevent
// a race where two goroutines both observe loaded=false and both call the API.
// Subsequent calls are no-ops regardless of whether the first call succeeded.
func (mc *ModelCache) LoadOnce(fetch func() (map[string]string, error)) {
	mc.mu.Lock()
	defer mc.mu.Unlock()

	if mc.loaded {
		return
	}

	cache, err := fetch()
	if err != nil {
		mc.err = err
	} else {
		mc.cache = cache
	}
	mc.loaded = true
}

// Invalidate clears the cache so the next LoadOnce call fetches fresh data.
func (mc *ModelCache) Invalidate() {
	mc.mu.Lock()
	defer mc.mu.Unlock()
	mc.cache = nil
	mc.err = nil
	mc.loaded = false
}

// ValidateModelName returns nil if modelName is present in the cache. If the
// cache failed to load it returns a wrapped error. If the model is not found
// it returns a descriptive error listing known models.
//
// Callers must call LoadOnce before ValidateModelName.
func (mc *ModelCache) ValidateModelName(modelName string) error {
	mc.mu.RLock()
	defer mc.mu.RUnlock()

	if mc.err != nil {
		return fmt.Errorf("could not validate model name: %w", mc.err)
	}

	if _, ok := mc.cache[modelName]; ok {
		return nil
	}

	known := make([]string, 0, len(mc.cache))
	for name := range mc.cache {
		known = append(known, name)
	}
	sort.Strings(known)
	return fmt.Errorf("unknown %s model %q; known models: %s", mc.providerName, modelName, strings.Join(known, ", "))
}

// ListModels returns a sorted slice of ModelInfo built from the cache.
// Callers must call LoadOnce before ListModels.
func (mc *ModelCache) ListModels() ([]ModelInfo, error) {
	mc.mu.RLock()
	defer mc.mu.RUnlock()

	if mc.err != nil {
		return nil, mc.err
	}

	models := make([]ModelInfo, 0, len(mc.cache))
	for name, displayName := range mc.cache {
		info := ModelInfo{Name: name, DisplayName: displayName}
		if mc.isReasoning != nil {
			info.IsReasoning = mc.isReasoning(name)
		}
		models = append(models, info)
	}
	sort.Slice(models, func(i, j int) bool { return models[i].Name < models[j].Name })
	return models, nil
}
