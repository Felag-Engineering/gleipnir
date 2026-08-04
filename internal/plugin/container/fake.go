package container

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Fake is an in-process Runtime for unit tests. It runs the same
// ValidateCreate/ValidateCreateNetwork self-constraint checks the real
// DockerRuntime does — a test asserting on Fake's rejection is asserting on
// the actual rule, not a test double's approximation of it.
//
// Real-daemon coverage is a later, CI-gated issue; Fake is what lets the
// reconciler (and this package's own tests) exercise create/diff/converge
// logic without a socket at all.
type Fake struct {
	mu         sync.Mutex
	containers map[ContainerID]*fakeContainer
	networks   map[NetworkID]NetworkInfo
	nextID     int
	closed     bool

	// CreateErr, when non-nil, is returned by Create instead of succeeding —
	// lets tests simulate a socket-level failure after validation passes.
	CreateErr error
}

type fakeContainer struct {
	info ContainerInfo
	logs string
}

// NewFake returns an empty Fake runtime.
func NewFake() *Fake {
	return &Fake{
		containers: make(map[ContainerID]*fakeContainer),
		networks:   make(map[NetworkID]NetworkInfo),
	}
}

func (f *Fake) Create(_ context.Context, opts CreateOptions) (ContainerID, error) {
	if err := ValidateCreate(opts); err != nil {
		return "", err
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if f.CreateErr != nil {
		return "", f.CreateErr
	}

	f.nextID++
	id := ContainerID("fake-" + strconv.Itoa(f.nextID))
	f.containers[id] = &fakeContainer{
		info: ContainerInfo{
			ID:     id,
			Name:   opts.Name,
			Image:  opts.Image,
			Labels: opts.Labels,
			State:  ContainerStateCreated,
		},
	}
	return id, nil
}

func (f *Fake) Start(_ context.Context, id ContainerID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.containers[id]
	if !ok {
		return fmt.Errorf("container: fake: no such container %q", id)
	}
	c.info.State = ContainerStateRunning
	return nil
}

func (f *Fake) Stop(_ context.Context, id ContainerID, _ time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.containers[id]
	if !ok {
		return fmt.Errorf("container: fake: no such container %q", id)
	}
	c.info.State = ContainerStateExited
	return nil
}

func (f *Fake) Remove(_ context.Context, id ContainerID, force bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.containers[id]
	if !ok {
		return fmt.Errorf("container: fake: no such container %q", id)
	}
	if c.info.State == ContainerStateRunning && !force {
		return fmt.Errorf("container: fake: container %q is running; stop it or pass force", id)
	}
	delete(f.containers, id)
	return nil
}

func (f *Fake) Inspect(_ context.Context, id ContainerID) (ContainerInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.containers[id]
	if !ok {
		return ContainerInfo{}, fmt.Errorf("container: fake: no such container %q", id)
	}
	return c.info, nil
}

func (f *Fake) Stats(_ context.Context, id ContainerID) (ContainerStats, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.containers[id]; !ok {
		return ContainerStats{}, fmt.Errorf("container: fake: no such container %q", id)
	}
	// Fixed, deterministic values — tests that need specific numbers set
	// them via SetStats rather than relying on this default.
	return ContainerStats{}, nil
}

func (f *Fake) Logs(_ context.Context, id ContainerID, _ LogOptions) (io.ReadCloser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.containers[id]
	if !ok {
		return nil, fmt.Errorf("container: fake: no such container %q", id)
	}
	return io.NopCloser(strings.NewReader(c.logs)), nil
}

func (f *Fake) ListByLabel(_ context.Context, key, value string) ([]ContainerInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []ContainerInfo
	for _, c := range f.containers {
		if c.info.Labels[key] == value {
			out = append(out, c.info)
		}
	}
	return out, nil
}

func (f *Fake) CreateNetwork(_ context.Context, opts NetworkOptions) (NetworkID, error) {
	if err := ValidateCreateNetwork(opts); err != nil {
		return "", err
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	id := NetworkID("fake-net-" + strconv.Itoa(f.nextID))
	f.networks[id] = NetworkInfo{
		ID:       id,
		Name:     opts.Name,
		Labels:   opts.Labels,
		Internal: opts.Internal,
	}
	return id, nil
}

func (f *Fake) RemoveNetwork(_ context.Context, id NetworkID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.networks[id]; !ok {
		return fmt.Errorf("container: fake: no such network %q", id)
	}
	delete(f.networks, id)
	return nil
}

func (f *Fake) ListNetworksByLabel(_ context.Context, key, value string) ([]NetworkInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []NetworkInfo
	for _, n := range f.networks {
		if n.Labels[key] == value {
			out = append(out, n)
		}
	}
	return out, nil
}

func (f *Fake) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	return nil
}

// Closed reports whether Close has been called. Test-only introspection.
func (f *Fake) Closed() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closed
}

// SetLogs seeds the log output Logs returns for id. Test-only helper.
func (f *Fake) SetLogs(id ContainerID, logs string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if c, ok := f.containers[id]; ok {
		c.logs = logs
	}
}

var _ Runtime = (*Fake)(nil)
var _ Runtime = (*ReadOnlyRuntime)(nil)
