package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sync"
)

// eventScope is the instance-level coarse filter an admin sets once for the
// instance, as opposed to a policy's binding. The host sends it as the scope
// param of events/listen; its shape is subscriptionSchema in manifest.yaml.
type eventScope struct {
	// Channels limits channel messages to these channel IDs. Empty = all.
	Channels []string `json:"channels,omitempty"`
	// MentionOnly limits channel messages to those that mention the bot.
	MentionOnly bool `json:"mention_only,omitempty"`
	// DirectMessages enables 1:1 DMs. DM channel IDs (D...) cannot be in
	// Channels, so this flag is the only way to receive them.
	DirectMessages bool `json:"direct_messages,omitempty"`

	// SlashCommands and Shortcuts exist so an instance used only for commands
	// or shortcuts can still save a non-empty scope; the host does not open an
	// events stream for an empty one. They do not gate delivery: a command or
	// shortcut is explicit intent from any surface, never channel-scoped.
	SlashCommands bool `json:"slash_commands,omitempty"`
	Shortcuts     bool `json:"shortcuts,omitempty"`
}

// allowsMessage reports whether a message event is in scope.
//
// A DM is decided by DirectMessages alone: it has no channel ID the allow-list
// could name and is never a mention, so applying either filter would block DMs
// even when the admin enabled them.
func (s eventScope) allowsMessage(channelID string, mentioned, isDM bool) bool {
	if isDM {
		return s.DirectMessages
	}
	if s.MentionOnly && !mentioned {
		return false
	}
	return len(s.Channels) == 0 || slices.Contains(s.Channels, channelID)
}

// scopeStore holds the scope of the host's most recent events/listen.
//
// The scope only ever arrives on that request, but events have to be filtered
// when Slack delivers them, including while the host is disconnected, so the
// last scope is kept and written to disk: a restarted plugin keeps filtering
// until the host reconnects. Before any listen there is no scope at all, and
// nothing is emitted, since nobody has subscribed to anything.
type scopeStore struct {
	path string

	mu    sync.RWMutex
	scope eventScope
	known bool
}

// newScopeStore loads the persisted scope from dir, if there is one.
func newScopeStore(dir string) (*scopeStore, error) {
	s := &scopeStore{path: filepath.Join(dir, "event-scope.json")}
	raw, err := os.ReadFile(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", s.path, err)
	}
	if err := json.Unmarshal(raw, &s.scope); err != nil {
		return nil, fmt.Errorf("decode %s: %w", s.path, err)
	}
	s.known = true
	return s, nil
}

// current returns the scope and whether a host has ever supplied one.
func (s *scopeStore) current() (eventScope, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.scope, s.known
}

// set replaces the scope and persists it atomically, so a crash mid-write
// leaves the previous scope rather than a torn file that would stop the next
// start. An unchanged scope is not rewritten.
func (s *scopeStore) set(scope eventScope) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, err := json.Marshal(scope)
	if err != nil {
		return fmt.Errorf("encode scope: %w", err)
	}
	if s.known {
		if old, err := json.Marshal(s.scope); err == nil && bytes.Equal(old, raw) {
			return nil
		}
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return fmt.Errorf("replace %s: %w", s.path, err)
	}
	s.scope = scope
	s.known = true
	return nil
}

// maxListenRequestBytes bounds how much of an events/listen body is read to
// find the scope; the real request is a few hundred bytes.
const maxListenRequestBytes = 1 << 20

// captureScope wraps the events/listen handler to record the scope each listen
// carries. The events handler treats scope as opaque, so this is the only
// place the plugin can see it. The request is passed on untouched.
func captureScope(next http.Handler, store *scopeStore) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxListenRequestBytes))
		if err != nil {
			http.Error(w, "request body too large or unreadable", http.StatusBadRequest)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))

		// A body that does not parse is next's to refuse, with the proper
		// JSON-RPC error.
		var req struct {
			Params struct {
				Scope json.RawMessage `json:"scope"`
			} `json:"params"`
		}
		if err := json.Unmarshal(body, &req); err == nil {
			var scope eventScope
			if len(req.Params.Scope) == 0 || json.Unmarshal(req.Params.Scope, &scope) == nil {
				if err := store.set(scope); err != nil {
					log.Printf("slack-v2: keep event scope: %v", err)
				}
			} else {
				log.Printf("slack-v2: events/listen carried a scope that is not a subscription scope; keeping the previous one")
			}
		}
		next.ServeHTTP(w, r)
	})
}
