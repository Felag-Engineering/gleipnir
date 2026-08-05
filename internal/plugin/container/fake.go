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

	// images is what ImageInspect answers from, keyed by every reference an
	// image answers to (ID, tags, repo digests).
	images map[string]ImageInfo

	// PendingImages is what a successful ImageLoad makes present. Setting it
	// to an image whose ID is NOT the one a manifest pins is how a test
	// expresses "the archive contained something else".
	PendingImages []ImageInfo

	// LoadErr, when non-nil, fails ImageLoad after the archive has been read.
	LoadErr error

	// Loads and LoadedBytes record what ImageLoad saw, so a test can assert
	// that manual mode did not touch the socket at all.
	Loads       int
	LoadedBytes int64
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

// AddImage makes the Fake report an image as locally present, keyed by every
// reference it answers to. Tests use it to describe what an archive contained
// — including the hostile case where it contained something other than the
// digest a manifest pinned.
func (f *Fake) AddImage(info ImageInfo) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.images == nil {
		f.images = make(map[string]ImageInfo)
	}
	f.images[info.ID] = info
	for _, ref := range info.RepoTags {
		f.images[ref] = info
	}
	for _, ref := range info.RepoDigests {
		f.images[ref] = info
	}
}

// ImageLoad records the load and makes PendingImages present. It reads the
// archive to completion so a test's reader is exercised the same way the real
// daemon exercises it — a caller that forgets to rewind a file sees it here.
func (f *Fake) ImageLoad(_ context.Context, archive io.Reader) error {
	n, err := io.Copy(io.Discard, archive)
	if err != nil {
		return fmt.Errorf("container: fake image load: %w", err)
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if f.LoadErr != nil {
		return f.LoadErr
	}
	f.LoadedBytes += n
	f.Loads++
	for _, info := range f.PendingImages {
		if f.images == nil {
			f.images = make(map[string]ImageInfo)
		}
		f.images[info.ID] = info
		for _, ref := range info.RepoTags {
			f.images[ref] = info
		}
		for _, ref := range info.RepoDigests {
			f.images[ref] = info
		}
	}
	return nil
}

func (f *Fake) ImageInspect(_ context.Context, ref string) (ImageInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	info, ok := f.images[ref]
	if !ok {
		return ImageInfo{}, fmt.Errorf("%w: %s", ErrImageNotFound, ref)
	}
	return info, nil
}
