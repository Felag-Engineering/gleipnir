package configvalidate_test

import (
	"context"
	"errors"
	"testing"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/plugin/configvalidate"
)

// fakeQuerier is a local fake for ManifestQuerier used in Snapshotter tests.
type fakeQuerier struct {
	plugins   map[string]db.Plugin
	instances map[string]db.PluginInstance
}

func (f *fakeQuerier) GetPluginByID(_ context.Context, id string) (db.Plugin, error) {
	p, ok := f.plugins[id]
	if !ok {
		return db.Plugin{}, errors.New("plugin not found: " + id)
	}
	return p, nil
}

func (f *fakeQuerier) GetPluginInstanceByID(_ context.Context, id string) (db.PluginInstance, error) {
	i, ok := f.instances[id]
	if !ok {
		return db.PluginInstance{}, errors.New("instance not found: " + id)
	}
	return i, nil
}

// notifyOnlyManifestYAML is a minimal valid manifest with implements_notify: true.
const notifyOnlyManifestYAML = `
id: test-plugin
name: Test Plugin
version: 1.0.0
services:
  channel: v1
channels:
  - implements_notify: true
    implements_request: false
`

// bothManifestYAML has both Notify and Request true.
const bothManifestYAML = `
id: test-plugin
name: Test Plugin
version: 1.0.0
services:
  channel: v1
channels:
  - implements_notify: true
    implements_request: true
`

func newFake(pluginID, manifestYAML string) *fakeQuerier {
	return &fakeQuerier{
		plugins: map[string]db.Plugin{
			pluginID: {ID: pluginID, ManifestSnapshot: manifestYAML},
		},
		instances: map[string]db.PluginInstance{},
	}
}

func TestSnapshotter_ForPluginID_ParsesOnMiss(t *testing.T) {
	q := newFake("p1", notifyOnlyManifestYAML)
	s := configvalidate.NewSnapshotter(q)

	m, err := s.ForPluginID(context.Background(), "p1")
	if err != nil {
		t.Fatalf("ForPluginID: %v", err)
	}
	if m.Services.Channel == "" {
		t.Error("expected Channel service to be set")
	}
}

func TestSnapshotter_ForPluginID_ReturnsSamePointerOnHit(t *testing.T) {
	q := newFake("p1", notifyOnlyManifestYAML)
	s := configvalidate.NewSnapshotter(q)

	m1, err := s.ForPluginID(context.Background(), "p1")
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	m2, err := s.ForPluginID(context.Background(), "p1")
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if m1 != m2 {
		t.Error("expected same pointer on cache hit, got different pointers")
	}
}

func TestSnapshotter_ForPluginID_ContentChangeYieldsNewValue(t *testing.T) {
	q := newFake("p1", notifyOnlyManifestYAML)
	s := configvalidate.NewSnapshotter(q)

	m1, err := s.ForPluginID(context.Background(), "p1")
	if err != nil {
		t.Fatalf("first call: %v", err)
	}

	// Update the plugin's manifest bytes.
	q.plugins["p1"] = db.Plugin{ID: "p1", ManifestSnapshot: bothManifestYAML}

	m2, err := s.ForPluginID(context.Background(), "p1")
	if err != nil {
		t.Fatalf("second call: %v", err)
	}

	if m1 == m2 {
		t.Error("expected different pointer after manifest bytes changed")
	}
	if !m2.Channels[0].ImplementsRequest {
		t.Error("expected updated manifest to have ImplementsRequest=true")
	}
}

func TestSnapshotter_ForPluginID_DedupIdenticalManifests(t *testing.T) {
	// Two distinct plugins shipping byte-identical manifests share one cache entry.
	q := &fakeQuerier{
		plugins: map[string]db.Plugin{
			"p1": {ID: "p1", ManifestSnapshot: notifyOnlyManifestYAML},
			"p2": {ID: "p2", ManifestSnapshot: notifyOnlyManifestYAML},
		},
		instances: map[string]db.PluginInstance{},
	}
	s := configvalidate.NewSnapshotter(q)

	m1, err := s.ForPluginID(context.Background(), "p1")
	if err != nil {
		t.Fatalf("p1: %v", err)
	}
	m2, err := s.ForPluginID(context.Background(), "p2")
	if err != nil {
		t.Fatalf("p2: %v", err)
	}
	if m1 != m2 {
		t.Error("expected same pointer for plugins with identical manifest bytes")
	}
}

func TestSnapshotter_ForInstanceID_ResolvesInstanceToPlugin(t *testing.T) {
	q := newFake("p1", notifyOnlyManifestYAML)
	q.instances["i1"] = db.PluginInstance{ID: "i1", PluginID: "p1"}
	s := configvalidate.NewSnapshotter(q)

	m, err := s.ForInstanceID(context.Background(), "i1")
	if err != nil {
		t.Fatalf("ForInstanceID: %v", err)
	}
	if m.Services.Channel == "" {
		t.Error("expected Channel service to be set")
	}
}

func TestSnapshotter_ForPluginID_ErrorPropagated(t *testing.T) {
	q := &fakeQuerier{
		plugins:   map[string]db.Plugin{},
		instances: map[string]db.PluginInstance{},
	}
	s := configvalidate.NewSnapshotter(q)

	_, err := s.ForPluginID(context.Background(), "missing")
	if err == nil {
		t.Fatal("expected error for missing plugin, got nil")
	}
	if !containsString(err.Error(), "missing") {
		t.Errorf("error = %q, want it to mention the plugin id", err.Error())
	}
}

func TestSnapshotter_MalformedYAMLReturnsError(t *testing.T) {
	q := newFake("p1", ":\nnot: [valid yaml\n")
	s := configvalidate.NewSnapshotter(q)

	_, err := s.ForPluginID(context.Background(), "p1")
	if err == nil {
		t.Fatal("expected parse error for malformed YAML, got nil")
	}
}
