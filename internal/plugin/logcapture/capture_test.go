package logcapture

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/felag-engineering/gleipnir/internal/plugin/container"
)

// --- fixtures ---------------------------------------------------------------

// fakeSource serves a fixed byte stream as a container's log output.
type fakeSource struct {
	data []byte
	err  error

	mu     sync.Mutex
	opened int
	closed bool
}

func (f *fakeSource) Logs(context.Context, container.ContainerID, container.LogOptions) (io.ReadCloser, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.mu.Lock()
	f.opened++
	f.mu.Unlock()
	return &trackingReader{Reader: bytes.NewReader(f.data), src: f}, nil
}

type trackingReader struct {
	io.Reader
	src *fakeSource
}

func (t *trackingReader) Close() error {
	t.src.mu.Lock()
	t.src.closed = true
	t.src.mu.Unlock()
	return nil
}

// frame builds one multiplexed log frame, the shape a TTY-less container's
// output really arrives in.
func frame(stream byte, payload string) []byte {
	header := make([]byte, 8)
	header[0] = stream
	binary.BigEndian.PutUint32(header[4:], uint32(len(payload)))
	return append(header, payload...)
}

// captureLogger returns a logger writing JSON to a buffer, plus the buffer.
func captureLogger() (*slog.Logger, *lockedBuffer) {
	buf := &lockedBuffer{}
	return slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})), buf
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) records(t *testing.T) []map[string]any {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()

	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(b.buf.String()), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("log line %q does not parse: %v", line, err)
		}
		out = append(out, rec)
	}
	return out
}

func newCapturer(t *testing.T, cfg Config) (*Capturer, *lockedBuffer) {
	t.Helper()
	logger, buf := captureLogger()
	cfg.Logger = logger
	c, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c, buf
}

func target() Target {
	return Target{InstanceID: "inst-1", PluginID: "plug-1", ContainerID: "c1", Generation: 3}
}

// --- labels -----------------------------------------------------------------

// Every captured line carries the instance it came from and is marked
// uncorrelated. Attribution is the whole reason this channel is usable at all,
// and the uncorrelated flag is the reason it is not mistaken for the structured
// one (ADR-047).
func TestCapturer_LabelsEveryLine(t *testing.T) {
	source := &fakeSource{data: append(
		frame(1, "listening on :8080\n"),
		frame(2, "warning: no credentials configured\n")...,
	)}
	c, logs := newCapturer(t, Config{Source: source})

	if err := c.Follow(context.Background(), target()); err != nil {
		t.Fatalf("Follow: %v", err)
	}

	records := logs.records(t)
	if len(records) != 2 {
		t.Fatalf("got %d log records, want 2: %+v", len(records), records)
	}
	for _, rec := range records {
		if rec["instance_id"] != "inst-1" || rec["plugin_id"] != "plug-1" {
			t.Errorf("record missing identity labels: %+v", rec)
		}
		// During a rotation two containers for one instance are alive at once,
		// so "which one said this" needs answering.
		if rec["generation"] != float64(3) {
			t.Errorf("generation = %v, want 3", rec["generation"])
		}
		if rec["correlated"] != false {
			t.Errorf("correlated = %v, want false — this channel carries no run_id by design", rec["correlated"])
		}
	}

	if records[0]["stream"] != "stdout" || records[0]["output"] != "listening on :8080" {
		t.Errorf("stdout record = %+v", records[0])
	}
	// "It wrote to stderr" is often the whole diagnosis, so the distinction
	// survives capture and raises the level.
	if records[1]["stream"] != "stderr" || records[1]["level"] != "WARN" {
		t.Errorf("stderr record = %+v", records[1])
	}
}

// --- caps -------------------------------------------------------------------

// A log-spamming container must not degrade the host, and the drop must be
// visible: a truncated log that says nothing about being truncated reads as the
// container having gone quiet.
func TestCapturer_RateLimitDropsLoudly(t *testing.T) {
	frozen := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	timeNow = func() time.Time { return frozen }
	t.Cleanup(func() { timeNow = func() time.Time { return time.Now() } })

	var payload []byte
	for i := 0; i < 40; i++ {
		payload = append(payload, frame(1, "line\n")...)
	}
	source := &fakeSource{data: payload}
	// Burst 5 with a frozen clock: exactly five lines pass, the rest drop.
	c, logs := newCapturer(t, Config{Source: source, LinesPerSecond: 1, Burst: 5})

	if err := c.Follow(context.Background(), target()); err != nil {
		t.Fatalf("Follow: %v", err)
	}

	var captured, dropReports int
	var reportedDrops float64
	for _, rec := range logs.records(t) {
		switch rec["msg"] {
		case "plugin container output":
			captured++
		case "plugin container output dropped: capture rate limit exceeded":
			dropReports++
			reportedDrops += rec["dropped"].(float64)
		}
	}

	if captured != 5 {
		t.Errorf("captured %d lines, want the burst of 5", captured)
	}
	if dropReports == 0 {
		t.Fatal("no drop report; a silent drop is the failure this cap must not have")
	}
	if reportedDrops != 35 {
		t.Errorf("reported %v drops, want 35", reportedDrops)
	}
	// Only the retained lines are in the tail — the ring holds what was
	// captured, not what was emitted.
	if got := len(c.Tail("inst-1", 0)); got != 5 {
		t.Errorf("tail holds %d lines, want 5", got)
	}
}

// A single enormous line is the same denial-of-service as many, so it is
// bounded too — and the truncation is marked, because a line that was cut is
// different evidence from a line that ended.
func TestCapturer_TruncatesAnOversizeLine(t *testing.T) {
	huge := strings.Repeat("x", maxLineBytes*2)
	source := &fakeSource{data: frame(1, huge+"\n")}
	c, _ := newCapturer(t, Config{Source: source})

	if err := c.Follow(context.Background(), target()); err != nil {
		t.Fatalf("Follow: %v", err)
	}

	lines := c.Tail("inst-1", 0)
	if len(lines) != 1 {
		t.Fatalf("got %d lines, want 1", len(lines))
	}
	if !strings.HasSuffix(lines[0].Text, truncationMarker) {
		t.Error("oversize line was not marked as truncated")
	}
	if len(lines[0].Text) > maxLineBytes+len(truncationMarker) {
		t.Errorf("line is %d bytes, want it bounded", len(lines[0].Text))
	}
}

// --- crash before healthy ---------------------------------------------------

// The "why won't my plugin start" surface. A container that exits before it is
// healthy leaves nothing behind except what it printed.
func TestCapturer_StartupFailureDetailSurfacesTheLastLines(t *testing.T) {
	source := &fakeSource{data: bytes.Join([][]byte{
		frame(1, "starting\n"),
		frame(2, "config error: SLACK_TOKEN is not set\n"),
		frame(2, "exiting\n"),
	}, nil)}
	c, _ := newCapturer(t, Config{Source: source})

	if err := c.Follow(context.Background(), target()); err != nil {
		t.Fatalf("Follow: %v", err)
	}

	detail := c.StartupFailureDetail("inst-1", 2)
	if !strings.Contains(detail, "SLACK_TOKEN is not set") {
		t.Errorf("detail does not carry the diagnosis:\n%s", detail)
	}
	if !strings.Contains(detail, "[stderr]") {
		t.Errorf("detail does not mark the stream:\n%s", detail)
	}
	// Bounded — the health detail is a chip tooltip, not a log viewer.
	if strings.Contains(detail, "starting") {
		t.Errorf("detail exceeded the requested 2 lines:\n%s", detail)
	}
}

// A container that produced nothing at all still gets a usable message rather
// than an empty string that reads as "no error".
func TestCapturer_StartupFailureDetailWithNoOutput(t *testing.T) {
	c, _ := newCapturer(t, Config{Source: &fakeSource{}})
	detail := c.StartupFailureDetail("inst-unknown", 5)
	if !strings.Contains(detail, "no output") {
		t.Errorf("detail = %q, want it to say there was no output", detail)
	}
}

// --- lifecycle --------------------------------------------------------------

// A cancelled context ends the follow rather than leaving it blocked on a
// container that has nothing more to say.
func TestCapturer_ContextCancellationEndsTheFollow(t *testing.T) {
	source := &fakeSource{data: frame(1, "hello\n")}
	c, _ := newCapturer(t, Config{Source: source})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := c.Follow(ctx, target()); err != nil {
		t.Errorf("Follow after cancel = %v, want nil", err)
	}
}

// A container that exited closed its output. That is a normal thing for a
// container to do, not a capture failure.
func TestCapturer_CleanEndOfStreamIsNotAnError(t *testing.T) {
	c, _ := newCapturer(t, Config{Source: &fakeSource{data: frame(1, "bye\n")}})
	if err := c.Follow(context.Background(), target()); err != nil {
		t.Errorf("Follow = %v, want nil on a clean end", err)
	}
}

func TestCapturer_OpenFailureIsReported(t *testing.T) {
	c, _ := newCapturer(t, Config{Source: &fakeSource{err: errors.New("socket gone")}})
	if err := c.Follow(context.Background(), target()); err == nil {
		t.Error("Follow succeeded against a failing source")
	}
}

// A re-created instance must not inherit the dead one's exhausted bucket or
// its output.
func TestCapturer_ForgetClearsInstanceState(t *testing.T) {
	c, _ := newCapturer(t, Config{Source: &fakeSource{data: frame(1, "hello\n")}})
	if err := c.Follow(context.Background(), target()); err != nil {
		t.Fatalf("Follow: %v", err)
	}
	if len(c.Tail("inst-1", 0)) == 0 {
		t.Fatal("nothing captured")
	}

	c.Forget("inst-1")
	if got := c.Tail("inst-1", 0); got != nil {
		t.Errorf("tail after Forget = %v, want nil", got)
	}
}

func TestNew_RequiresSource(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Error("New accepted a config with no Source")
	}
}

// --- the ring ---------------------------------------------------------------

func TestRing_KeepsTheMostRecent(t *testing.T) {
	r := newRing(3)
	for _, text := range []string{"a", "b", "c", "d", "e"} {
		r.push(Line{Text: text})
	}

	got := r.tail(0)
	want := []string{"c", "d", "e"}
	if len(got) != len(want) {
		t.Fatalf("tail = %v, want %v", got, want)
	}
	for i := range want {
		if got[i].Text != want[i] {
			t.Errorf("tail[%d] = %q, want %q", i, got[i].Text, want[i])
		}
	}

	if last := r.tail(2); len(last) != 2 || last[0].Text != "d" || last[1].Text != "e" {
		t.Errorf("tail(2) = %v, want the last two", last)
	}
	if n := r.tail(99); len(n) != 3 {
		t.Errorf("tail(99) = %v, want it clamped to what is held", n)
	}
	if empty := newRing(3).tail(0); empty != nil {
		t.Errorf("empty ring tail = %v, want nil", empty)
	}
}
