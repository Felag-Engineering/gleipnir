package logcapture

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"github.com/felag-engineering/gleipnir/internal/plugin/container"
)

// Defaults for the capture caps. A container that spams its output must not be
// able to degrade the host, and the cap has to be low enough to matter while
// high enough that ordinary startup chatter survives — a plugin printing a few
// hundred lines while it boots is normal.
const (
	defaultLinesPerSecond = 50
	defaultBurst          = 200
	defaultRingSize       = 50
	dropReportInterval    = 30 * time.Second
)

// Target is one container to capture from.
type Target struct {
	InstanceID  string
	PluginID    string
	ContainerID container.ContainerID

	// Generation is the rotation generation this container belongs to. Captured
	// lines carry it because during a rotation two containers for one instance
	// are alive at once, and "which one said this" is the question.
	Generation int64
}

// LogSource is the narrow slice of the runtime this package needs.
type LogSource interface {
	Logs(ctx context.Context, id container.ContainerID, opts container.LogOptions) (io.ReadCloser, error)
}

// Config wires a Capturer.
type Config struct {
	Source LogSource

	// LinesPerSecond and Burst bound the capture path. Zero uses the defaults.
	LinesPerSecond float64
	Burst          int

	// RingSize is how many recent lines are retained per instance for the
	// crash-before-healthy path. Zero uses the default.
	RingSize int

	// Logger receives captured lines; nil uses slog's default.
	Logger *slog.Logger
}

// timeNow is the package's injectable clock (CLAUDE.md "Testing time-dependent
// code"). Tests swap it via t.Cleanup and must not call t.Parallel.
var timeNow = func() time.Time { return time.Now() }

// Capturer streams container output into the host logger and keeps a short
// tail per instance.
type Capturer struct {
	source   LogSource
	logger   *slog.Logger
	ringSize int

	rateLimit float64
	burst     int

	mu       sync.Mutex
	limiters map[string]*rate.Limiter
	rings    map[string]*ring
	drops    map[string]*dropCounter
}

func New(cfg Config) (*Capturer, error) {
	if cfg.Source == nil {
		return nil, fmt.Errorf("logcapture: Source is required")
	}
	c := &Capturer{
		source:    cfg.Source,
		logger:    cfg.Logger,
		ringSize:  cfg.RingSize,
		rateLimit: cfg.LinesPerSecond,
		burst:     cfg.Burst,
		limiters:  make(map[string]*rate.Limiter),
		rings:     make(map[string]*ring),
		drops:     make(map[string]*dropCounter),
	}
	if c.logger == nil {
		c.logger = slog.Default()
	}
	if c.ringSize <= 0 {
		c.ringSize = defaultRingSize
	}
	if c.rateLimit <= 0 {
		c.rateLimit = defaultLinesPerSecond
	}
	if c.burst <= 0 {
		c.burst = defaultBurst
	}
	return c, nil
}

// Follow streams a container's output until the stream ends or ctx is done.
//
// It returns nil on a clean end of stream: a container that exited closed its
// output, which is a normal thing for a container to do and not a capture
// failure.
func (c *Capturer) Follow(ctx context.Context, target Target) error {
	stream, err := c.source.Logs(ctx, target.ContainerID, container.LogOptions{Follow: true})
	if err != nil {
		return fmt.Errorf("logcapture: opening logs for instance %s: %w", target.InstanceID, err)
	}
	defer stream.Close()

	// Close the stream when the context ends so a blocked read unblocks —
	// otherwise Follow would outlive its caller waiting on a container that has
	// nothing more to say.
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = stream.Close()
		case <-done:
		}
	}()

	err = Decode(stream, func(line Line) { c.handle(ctx, target, line) })
	c.flushDrops(ctx, target)
	if ctx.Err() != nil {
		return nil
	}
	return err
}

// handle applies the cap, records the line in the instance's tail, and logs it.
func (c *Capturer) handle(ctx context.Context, target Target, line Line) {
	if !c.allow(target.InstanceID) {
		c.countDrop(ctx, target)
		return
	}

	c.record(target.InstanceID, line)
	capturedLines.WithLabelValues(string(line.Stream)).Inc()

	level := slog.LevelInfo
	if line.Stream == StreamStderr {
		level = slog.LevelWarn
	}
	c.logger.Log(ctx, level, "plugin container output",
		"plugin_id", target.PluginID,
		"instance_id", target.InstanceID,
		"generation", target.Generation,
		"stream", string(line.Stream),
		// Stated on every line rather than assumed from the message: this
		// channel carries no run_id or call_id by design (ADR-047), and a
		// reader correlating it to a run would be correlating nothing.
		"correlated", false,
		"output", line.Text,
	)
}

func (c *Capturer) allow(instanceID string) bool {
	c.mu.Lock()
	limiter, ok := c.limiters[instanceID]
	if !ok {
		limiter = rate.NewLimiter(rate.Limit(c.rateLimit), c.burst)
		c.limiters[instanceID] = limiter
	}
	c.mu.Unlock()
	return limiter.AllowN(timeNow(), 1)
}

// countDrop tallies a dropped line and reports periodically.
//
// Loud, not silent: an operator looking at a truncated log needs to know the
// truncation happened, and a counter that is only visible in Prometheus is not
// enough when the thing being debugged is "why does this log stop".
func (c *Capturer) countDrop(ctx context.Context, target Target) {
	droppedLines.Inc()

	c.mu.Lock()
	counter, ok := c.drops[target.InstanceID]
	if !ok {
		counter = &dropCounter{}
		c.drops[target.InstanceID] = counter
	}
	counter.n++
	n := counter.n
	report := timeNow().Sub(counter.lastReport) >= dropReportInterval
	if report {
		counter.lastReport = timeNow()
		counter.n = 0
	}
	c.mu.Unlock()

	if report {
		c.logger.WarnContext(ctx, "plugin container output dropped: capture rate limit exceeded",
			"plugin_id", target.PluginID,
			"instance_id", target.InstanceID,
			"dropped", n,
			"limit_lines_per_second", c.rateLimit,
		)
	}
}

// flushDrops reports any pending drop count when a stream ends, so the last
// window's losses are not swallowed by the reporting interval.
func (c *Capturer) flushDrops(ctx context.Context, target Target) {
	c.mu.Lock()
	counter, ok := c.drops[target.InstanceID]
	var n int
	if ok {
		n = counter.n
		counter.n = 0
	}
	c.mu.Unlock()

	if n > 0 {
		c.logger.WarnContext(ctx, "plugin container output dropped: capture rate limit exceeded",
			"plugin_id", target.PluginID, "instance_id", target.InstanceID, "dropped", n)
	}
}

func (c *Capturer) record(instanceID string, line Line) {
	c.mu.Lock()
	defer c.mu.Unlock()
	r, ok := c.rings[instanceID]
	if !ok {
		r = newRing(c.ringSize)
		c.rings[instanceID] = r
	}
	r.push(line)
}

// Tail returns the most recent captured lines for an instance, oldest first.
//
// This is the "why won't my plugin start" surface: a container that exits
// before it is healthy leaves nothing behind except what it printed, and the
// operator-visible error state is where that belongs.
func (c *Capturer) Tail(instanceID string, n int) []Line {
	c.mu.Lock()
	defer c.mu.Unlock()
	r, ok := c.rings[instanceID]
	if !ok {
		return nil
	}
	return r.tail(n)
}

// Forget drops an instance's retained state. Called when an instance is
// removed — keeping a dead instance's tail forever is a leak, and keeping its
// limiter would make a re-created instance inherit an exhausted bucket.
func (c *Capturer) Forget(instanceID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.rings, instanceID)
	delete(c.limiters, instanceID)
	delete(c.drops, instanceID)
}

// StartupFailureDetail renders an instance's recent output as the health detail
// for a container that died before becoming healthy.
//
// Bounded to the last few lines on purpose: the health detail is a chip
// tooltip, not a log viewer, and the useful part of a crash is almost always
// the end of it.
func (c *Capturer) StartupFailureDetail(instanceID string, maxLines int) string {
	lines := c.Tail(instanceID, maxLines)
	if len(lines) == 0 {
		return "container exited before becoming healthy and produced no output"
	}
	var b []byte
	b = append(b, "container exited before becoming healthy; last output:"...)
	for _, line := range lines {
		b = append(b, '\n')
		if line.Stream != StreamUnknown {
			b = append(b, '[')
			b = append(b, line.Stream...)
			b = append(b, "] "...)
		}
		b = append(b, line.Text...)
	}
	return string(b)
}

type dropCounter struct {
	n          int
	lastReport time.Time
}

// ring is a fixed-size FIFO of the most recent lines.
type ring struct {
	items []Line
	next  int
	full  bool
}

func newRing(size int) *ring { return &ring{items: make([]Line, size)} }

func (r *ring) push(line Line) {
	r.items[r.next] = line
	r.next = (r.next + 1) % len(r.items)
	if r.next == 0 {
		r.full = true
	}
}

// tail returns up to n lines, oldest first.
func (r *ring) tail(n int) []Line {
	size := r.next
	if r.full {
		size = len(r.items)
	}
	if size == 0 {
		return nil
	}
	if n <= 0 || n > size {
		n = size
	}

	out := make([]Line, 0, n)
	start := r.next - n
	if start < 0 {
		start += len(r.items)
	}
	for i := 0; i < n; i++ {
		out = append(out, r.items[(start+i)%len(r.items)])
	}
	return out
}
