package oauth

import "sync"

// instanceLocks maintains a per-instance *sync.Mutex so the refresh path can
// prevent lost-update races between the background scanner and live
// GetCredentials traffic. The map grows as new instances are seen and never
// shrinks — plugin instance counts are tiny (tens, not thousands).
type instanceLocks struct {
	mu sync.Mutex
	m  map[string]*sync.Mutex
}

// Get returns the mutex for instanceID, creating it on first call.
func (l *instanceLocks) Get(id string) *sync.Mutex {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.m == nil {
		l.m = make(map[string]*sync.Mutex)
	}
	if _, ok := l.m[id]; !ok {
		l.m[id] = &sync.Mutex{}
	}
	return l.m[id]
}
