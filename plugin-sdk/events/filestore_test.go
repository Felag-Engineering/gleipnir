package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeClock is a manually-advanced clock for FileStore's single-goroutine
// retention tests — no wall-clock sleeps (docs/developer/testing-patterns.md).
// Not safe for concurrent use; see syncedFakeClock for the concurrent tests.
type fakeClock struct {
	now time.Time
}

func (c *fakeClock) Now() time.Time { return c.now }
func (c *fakeClock) Advance(d time.Duration) {
	c.now = c.now.Add(d)
}

// syncedFakeClock is fakeClock's concurrency-safe sibling, for tests where
// one goroutine advances the clock while others are concurrently calling
// FileStore methods that read it (Append, Since) — those calls happen under
// FileStore's own mutex, but the test's own Advance calls do not, so the
// clock itself needs its own lock to stay race-clean.
type syncedFakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *syncedFakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *syncedFakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// writeRawSegment hand-writes a segment file directly (bypassing Append),
// so a test can construct an on-disk layout FileStore itself would never
// produce — e.g. deliberate corruption in a specific segment.
func writeRawSegment(t *testing.T, dir string, firstSeq uint64, lines []string) {
	t.Helper()
	path := segmentPath(dir, firstSeq)
	content := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write raw segment %s: %v", path, err)
	}
}

// fileRecordLine renders rec exactly as Append would write it (minus the
// trailing newline), for use with writeRawSegment.
func fileRecordLine(t *testing.T, rec fileRecord) string {
	t.Helper()
	b, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("marshal fileRecord: %v", err)
	}
	return string(b)
}

func TestFileStore_AppendAndSince(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	store, err := NewFileStore(dir, time.Hour)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	for i := 1; i <= 3; i++ {
		e := StoredEvent{Seq: uint64(i), Type: "k", ID: fmt.Sprintf("e%d", i), Source: "src", Time: time.Now()}
		if err := store.Append(ctx, e); err != nil {
			t.Fatalf("Append(%d): %v", i, err)
		}
	}

	got, err := store.Since(ctx, 0, 0)
	if err != nil {
		t.Fatalf("Since(0): %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d events, want 3", len(got))
	}
	for i, e := range got {
		if e.Seq != uint64(i+1) {
			t.Errorf("got[%d].Seq = %d, want %d", i, e.Seq, i+1)
		}
	}

	got, err = store.Since(ctx, 1, 0)
	if err != nil {
		t.Fatalf("Since(1): %v", err)
	}
	if len(got) != 2 || got[0].Seq != 2 || got[1].Seq != 3 {
		t.Fatalf("Since(1) = %+v, want seqs [2,3]", got)
	}

	got, err = store.Since(ctx, 3, 0)
	if err != nil {
		t.Fatalf("Since(caught up): %v", err)
	}
	if len(got) != 0 {
		t.Errorf("Since(caught up) = %+v, want none", got)
	}

	got, err = store.Since(ctx, 1, 1)
	if err != nil {
		t.Fatalf("Since(1, limit 1): %v", err)
	}
	if len(got) != 1 || got[0].Seq != 2 {
		t.Fatalf("Since(1, limit 1) = %+v, want just seq 2", got)
	}

	if _, err := store.Since(ctx, 999, 0); !errors.Is(err, ErrCursorUnknown) {
		t.Errorf("Since(unissued cursor) err = %v, want ErrCursorUnknown", err)
	}

	latest, err := store.LatestSeq(ctx)
	if err != nil {
		t.Fatalf("LatestSeq: %v", err)
	}
	if latest != 3 {
		t.Errorf("LatestSeq = %d, want 3", latest)
	}
}

func TestFileStore_RejectsOutOfOrderAppend(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	store, err := NewFileStore(dir, time.Hour)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	if err := store.Append(ctx, StoredEvent{Seq: 5, Type: "k", Time: time.Now()}); err != nil {
		t.Fatalf("Append(5): %v", err)
	}
	if err := store.Append(ctx, StoredEvent{Seq: 5, Type: "k", Time: time.Now()}); err == nil {
		t.Error("Append(5) again: want an error (not after latest), got nil")
	}
	if err := store.Append(ctx, StoredEvent{Seq: 3, Type: "k", Time: time.Now()}); err == nil {
		t.Error("Append(3) after 5: want an error (not after latest), got nil")
	}
}

// TestFileStore_RestartContinuity is the DoD's core requirement: the
// sequence stays monotonic across a reopen, and a reopened store still
// serves everything a cursor from before the restart is entitled to.
func TestFileStore_RestartContinuity(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	store, err := NewFileStore(dir, time.Hour)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	for i := 1; i <= 3; i++ {
		if err := store.Append(ctx, StoredEvent{Seq: uint64(i), Type: "k", Time: time.Now()}); err != nil {
			t.Fatalf("Append(%d): %v", i, err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened, err := NewFileStore(dir, time.Hour)
	if err != nil {
		t.Fatalf("NewFileStore (reopen): %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })

	latest, err := reopened.LatestSeq(ctx)
	if err != nil {
		t.Fatalf("LatestSeq: %v", err)
	}
	if latest != 3 {
		t.Fatalf("LatestSeq after reopen = %d, want 3 (monotonic across a reopen)", latest)
	}

	// A Buffer resuming from the store's LatestSeq assigns the next event 4,
	// continuing the sequence rather than colliding with pre-restart seqs.
	if err := reopened.Append(ctx, StoredEvent{Seq: 4, Type: "k", Time: time.Now()}); err != nil {
		t.Fatalf("Append(4) after reopen: %v", err)
	}

	got, err := reopened.Since(ctx, 0, 0)
	if err != nil {
		t.Fatalf("Since(0) after reopen: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("Since(0) after reopen = %+v, want all 4 events surviving the restart", got)
	}

	// A cursor acked before the restart still resolves correctly afterward.
	got, err = reopened.Since(ctx, 2, 0)
	if err != nil {
		t.Fatalf("Since(2) after reopen: %v", err)
	}
	if len(got) != 2 || got[0].Seq != 3 || got[1].Seq != 4 {
		t.Fatalf("Since(2) after reopen = %+v, want seqs [3,4]", got)
	}
}

// TestFileStore_RestartContinuityAcrossManySegments proves the same thing
// as TestFileStore_RestartContinuity but forces multiple segment rotations,
// so recovery has to reassemble the sequence from more than one file.
func TestFileStore_RestartContinuityAcrossManySegments(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	store, err := NewFileStore(dir, time.Hour)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	const n = segmentMaxRecords + 5 // forces at least one rotation
	for i := 1; i <= n; i++ {
		if err := store.Append(ctx, StoredEvent{Seq: uint64(i), Type: "k", Time: time.Now()}); err != nil {
			t.Fatalf("Append(%d): %v", i, err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	matches, err := filepath.Glob(filepath.Join(dir, "*"+segmentFileExt))
	if err != nil {
		t.Fatalf("Glob: %v", err)
	}
	if len(matches) < 2 {
		t.Fatalf("got %d segment files, want at least 2 (rotation should have happened)", len(matches))
	}

	reopened, err := NewFileStore(dir, time.Hour)
	if err != nil {
		t.Fatalf("NewFileStore (reopen): %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })

	latest, err := reopened.LatestSeq(ctx)
	if err != nil {
		t.Fatalf("LatestSeq: %v", err)
	}
	if latest != uint64(n) {
		t.Fatalf("LatestSeq after reopen = %d, want %d", latest, n)
	}

	got, err := reopened.Since(ctx, 0, 0)
	if err != nil {
		t.Fatalf("Since(0) after reopen: %v", err)
	}
	if len(got) != n {
		t.Fatalf("Since(0) after reopen returned %d events, want %d", len(got), n)
	}
}

// TestFileStore_RetentionEvictionYieldsCursorUnknown is the DoD's second
// core requirement: an evicted cursor comes back as ErrCursorUnknown, and
// (via NewBufferWithStore) that is what fires the host's -32001 cursor-reset
// path.
func TestFileStore_RetentionEvictionYieldsCursorUnknown(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	clock := &fakeClock{now: time.Now()}

	const retention = time.Hour
	store, err := NewFileStore(dir, retention, WithClock(clock.Now))
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	// First segment: ages out and gets evicted.
	for i := 1; i <= segmentMaxRecords; i++ {
		e := StoredEvent{Seq: uint64(i), Type: "k", Time: clock.Now()}
		if err := store.Append(ctx, e); err != nil {
			t.Fatalf("Append(%d): %v", i, err)
		}
	}

	clock.Advance(2 * retention)

	// A second segment, appended after the clock jump, becomes the "always
	// keep the newest" survivor once compaction runs.
	secondSegmentFirst := uint64(segmentMaxRecords + 1)
	if err := store.Append(ctx, StoredEvent{Seq: secondSegmentFirst, Type: "k", Time: clock.Now()}); err != nil {
		t.Fatalf("Append after clock jump: %v", err)
	}

	if _, err := store.Since(ctx, 1, 0); !errors.Is(err, ErrCursorUnknown) {
		t.Errorf("Since(evicted cursor) err = %v, want ErrCursorUnknown", err)
	}
	// A cursor mid-way through the evicted segment: everything it still
	// needs (up through the segment's last record) is gone, so this is a
	// gap too — unlike a cursor sitting exactly at the evicted segment's
	// last record, which needs nothing further from it and is NOT a gap
	// (checked below).
	if _, err := store.Since(ctx, uint64(segmentMaxRecords/2), 0); !errors.Is(err, ErrCursorUnknown) {
		t.Errorf("Since(mid-evicted-segment cursor) err = %v, want ErrCursorUnknown", err)
	}

	// A cursor sitting exactly at the evicted segment's last record has
	// nothing left to replay from that segment — the very next record
	// (secondSegmentFirst) is present and contiguous, so this is NOT a gap.
	got, err := store.Since(ctx, uint64(segmentMaxRecords), 0)
	if err != nil {
		t.Fatalf("Since(boundary cursor at the evicted segment's last record): %v", err)
	}
	if len(got) != 1 || got[0].Seq != secondSegmentFirst {
		t.Fatalf("Since(boundary cursor) = %+v, want just seq %d", got, secondSegmentFirst)
	}

	// after == 0 ("from the beginning") also returns only what survived —
	// eviction, not an error, is how "the beginning" moves forward.
	got, err = store.Since(ctx, 0, 0)
	if err != nil {
		t.Fatalf("Since(0) after eviction: %v", err)
	}
	if len(got) != 1 || got[0].Seq != secondSegmentFirst {
		t.Fatalf("Since(0) after eviction = %+v, want just seq %d", got, secondSegmentFirst)
	}

	// LatestSeq is unaffected by eviction — monotonicity survives it too.
	latest, err := store.LatestSeq(ctx)
	if err != nil {
		t.Fatalf("LatestSeq: %v", err)
	}
	if latest != secondSegmentFirst {
		t.Errorf("LatestSeq = %d, want %d", latest, secondSegmentFirst)
	}
}

// TestFileStore_RetentionNeverEvictsToZeroSegments proves the "always keep
// the newest segment" rule: even an idle store, well past retention with no
// new Appends, keeps its sequence watermark rather than resetting to 0 on a
// later reopen.
func TestFileStore_RetentionNeverEvictsToZeroSegments(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	clock := &fakeClock{now: time.Now()}

	store, err := NewFileStore(dir, time.Hour, WithClock(clock.Now))
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	if err := store.Append(ctx, StoredEvent{Seq: 1, Type: "k", Time: clock.Now()}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	clock.Advance(24 * time.Hour) // deep past retention; nothing appended since

	reopened, err := NewFileStore(dir, time.Hour, WithClock(clock.Now))
	if err != nil {
		t.Fatalf("NewFileStore (reopen): %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })

	latest, err := reopened.LatestSeq(ctx)
	if err != nil {
		t.Fatalf("LatestSeq: %v", err)
	}
	if latest != 1 {
		t.Fatalf("LatestSeq after an idle reopen = %d, want 1 (the watermark must survive)", latest)
	}

	if err := reopened.Append(ctx, StoredEvent{Seq: 2, Type: "k", Time: clock.Now()}); err != nil {
		t.Fatalf("Append(2): %v", err)
	}
}

// TestFileStore_TruncatedTrailingRecordTolerated simulates a crash mid-write:
// the last line of the newest segment is a torn, unparseable fragment. A
// reopen must recover everything before it and drop the fragment silently,
// not fail to open.
func TestFileStore_TruncatedTrailingRecordTolerated(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	store, err := NewFileStore(dir, time.Hour)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	for i := 1; i <= 2; i++ {
		if err := store.Append(ctx, StoredEvent{Seq: uint64(i), Type: "k", Time: time.Now()}); err != nil {
			t.Fatalf("Append(%d): %v", i, err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	matches, err := filepath.Glob(filepath.Join(dir, "*"+segmentFileExt))
	if err != nil || len(matches) != 1 {
		t.Fatalf("Glob: %v matches=%v", err, matches)
	}
	segPath := matches[0]

	f, err := os.OpenFile(segPath, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatalf("open segment for corruption: %v", err)
	}
	// A torn write: a partial JSON object, no trailing newline — exactly
	// what a crash mid-Append leaves behind, since Append writes the full
	// line and only then fsyncs.
	if _, err := f.WriteString(`{"seq":3,"type":"k","id":`); err != nil {
		t.Fatalf("write torn record: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close corrupted segment: %v", err)
	}

	reopened, err := NewFileStore(dir, time.Hour)
	if err != nil {
		t.Fatalf("NewFileStore should tolerate a torn trailing record, got: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })

	latest, err := reopened.LatestSeq(ctx)
	if err != nil {
		t.Fatalf("LatestSeq: %v", err)
	}
	if latest != 2 {
		t.Fatalf("LatestSeq after recovering from a torn write = %d, want 2 (the fragment is dropped)", latest)
	}

	got, err := reopened.Since(ctx, 0, 0)
	if err != nil {
		t.Fatalf("Since(0): %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("Since(0) = %+v, want the 2 recoverable events", got)
	}

	// The store is still writable after recovery — the repaired file's
	// clean trailing newline lets the next Append continue the segment.
	if err := reopened.Append(ctx, StoredEvent{Seq: 3, Type: "k", ID: "recovered-3", Time: time.Now()}); err != nil {
		t.Fatalf("Append(3) after recovery: %v", err)
	}
	got, err = reopened.Since(ctx, 0, 0)
	if err != nil {
		t.Fatalf("Since(0) after Append(3): %v", err)
	}
	if len(got) != 3 || got[2].ID != "recovered-3" {
		t.Fatalf("Since(0) after Append(3) = %+v", got)
	}
}

func TestFileStore_RejectsNonPositiveRetention(t *testing.T) {
	dir := t.TempDir()
	if _, err := NewFileStore(dir, 0); err == nil {
		t.Error("NewFileStore(retention=0): want an error, got nil")
	}
	if _, err := NewFileStore(dir, -time.Second); err == nil {
		t.Error("NewFileStore(retention=-1s): want an error, got nil")
	}
}

// TestNewBufferWithStore_FileStore_CursorUnknownAfterRestart is the DoD's
// second bullet end to end: NewBufferWithStore over a FileStore reproduces
// the host's -32001 cursor-reset trigger (ErrCursorUnknown) after a real
// restart, not just against the in-memory ring.
func TestNewBufferWithStore_FileStore_CursorUnknownAfterRestart(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	store, err := NewFileStore(dir, time.Hour)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	buf, err := NewBufferWithStore(ctx, store)
	if err != nil {
		t.Fatalf("NewBufferWithStore: %v", err)
	}
	if _, err := buf.Publish(ctx, Event{Type: "k", ID: "e1"}, "src"); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// A second process incarnation reopens the same directory.
	reopenedStore, err := NewFileStore(dir, time.Hour)
	if err != nil {
		t.Fatalf("NewFileStore (reopen): %v", err)
	}
	t.Cleanup(func() { _ = reopenedStore.Close() })
	reopenedBuf, err := NewBufferWithStore(ctx, reopenedStore)
	if err != nil {
		t.Fatalf("NewBufferWithStore (reopen): %v", err)
	}

	// The sequence resumes rather than restarting at 1.
	seq, err := reopenedBuf.Publish(ctx, Event{Type: "k", ID: "e2"}, "src")
	if err != nil {
		t.Fatalf("Publish after reopen: %v", err)
	}
	if seq.Seq != 2 {
		t.Fatalf("seq after reopen = %d, want 2 (monotonic across the restart)", seq.Seq)
	}

	// A cursor this contract never issued comes back as ErrCursorUnknown —
	// Handler translates this into the doc's -32001 refusal.
	if _, err := reopenedBuf.Since(ctx, 999); !errors.Is(err, ErrCursorUnknown) {
		t.Errorf("Since(bogus cursor) err = %v, want ErrCursorUnknown", err)
	}
}

// TestFileStore_EmptySegmentIsRemovedOnLoad covers loadSegments' "nothing
// was ever durably promised for this file" branch: a segment file with zero
// recoverable records (not torn — genuinely empty, e.g. a rotation that
// crashed before a single record synced) is silently dropped rather than
// treated as corruption, whether or not it is the newest segment on disk.
func TestFileStore_EmptySegmentIsRemovedOnLoad(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	emptyPath := segmentPath(dir, 1)
	if err := os.WriteFile(emptyPath, nil, 0o600); err != nil {
		t.Fatalf("write empty segment: %v", err)
	}
	writeRawSegment(t, dir, 2, []string{fileRecordLine(t, fileRecord{Seq: 2, Type: "k", Time: time.Now()})})

	store, err := NewFileStore(dir, time.Hour)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	if _, err := os.Stat(emptyPath); !os.IsNotExist(err) {
		t.Errorf("empty segment %s still exists after load, want it removed (stat err = %v)", emptyPath, err)
	}

	latest, err := store.LatestSeq(ctx)
	if err != nil {
		t.Fatalf("LatestSeq: %v", err)
	}
	if latest != 2 {
		t.Errorf("LatestSeq = %d, want 2 (the empty segment contributes nothing)", latest)
	}

	got, err := store.Since(ctx, 0, 0)
	if err != nil {
		t.Fatalf("Since(0): %v", err)
	}
	if len(got) != 1 || got[0].Seq != 2 {
		t.Fatalf("Since(0) = %+v, want just seq 2", got)
	}
}

// TestFileStore_CorruptionInNonNewestSegmentIsAHardError proves the flip
// side of "a torn trailing record is tolerated": the same malformed line
// anywhere other than the very last line of the very last segment is real
// corruption, and NewFileStore refuses to open rather than silently
// dropping data mid-file.
func TestFileStore_CorruptionInNonNewestSegmentIsAHardError(t *testing.T) {
	dir := t.TempDir()

	// firstSeq=1 is NOT the newest segment (firstSeq=3 is), and the bad
	// line is not even this file's own last line — both facts independently
	// rule out "torn trailing write" tolerance.
	writeRawSegment(t, dir, 1, []string{
		"not valid json",
		fileRecordLine(t, fileRecord{Seq: 2, Type: "k", Time: time.Now()}),
	})
	writeRawSegment(t, dir, 3, []string{
		fileRecordLine(t, fileRecord{Seq: 3, Type: "k", Time: time.Now()}),
	})

	_, err := NewFileStore(dir, time.Hour)
	if err == nil {
		t.Fatal("NewFileStore: want an error for corruption in a non-newest segment, got nil")
	}
	wantPath := segmentPath(dir, 1)
	if !strings.Contains(err.Error(), wantPath) {
		t.Errorf("error = %v, want it to name the corrupt segment %s", err, wantPath)
	}
}

// TestFileStore_ConcurrentSinceDuringRetentionEviction is the -race
// regression test for the Since/Append data race: one writer keeps
// appending and periodically jumps a shared fake clock far enough to force
// retention eviction, while several readers hammer Since concurrently with
// a mix of cursors — some certainly evicted, some certainly current, some
// never issued. Since must never return anything other than nil or
// ErrCursorUnknown, and the run must be clean under -race.
func TestFileStore_ConcurrentSinceDuringRetentionEviction(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	clock := &syncedFakeClock{now: time.Now()}
	const retention = time.Hour

	// Pre-seed a full, already-old first segment directly (one os.WriteFile
	// call, not segmentMaxRecords individual fsyncs) so the concurrent
	// section below only has to Append past it to trigger an immediate
	// rotation — this keeps the test's own fsync count small while still
	// exercising a real multi-segment eviction race, not a slow synthetic
	// one.
	seedLines := make([]string, segmentMaxRecords)
	for i := range seedLines {
		seedLines[i] = fileRecordLine(t, fileRecord{Seq: uint64(i + 1), Type: "k", Time: clock.Now()})
	}
	writeRawSegment(t, dir, 1, seedLines)

	store, err := NewFileStore(dir, retention, WithClock(clock.Now))
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	const newEvents = 200 // > 0, triggers rotation on the very first Append
	firstNewSeq := segmentMaxRecords + 1
	lastSeq := segmentMaxRecords + newEvents
	done := make(chan struct{})

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(done)
		for seq := firstNewSeq; seq <= lastSeq; seq++ {
			e := StoredEvent{Seq: uint64(seq), Type: "k", Time: clock.Now()}
			if err := store.Append(ctx, e); err != nil {
				t.Errorf("Append(%d): %v", seq, err)
				return
			}
			if (seq-firstNewSeq)%20 == 0 {
				// Jumps well past retention repeatedly, so compaction has
				// a real, already-full segment to evict while readers are
				// mid-flight against it.
				clock.Advance(2 * retention)
			}
		}
	}()

	const readers = 8
	wg.Add(readers)
	for r := 0; r < readers; r++ {
		go func(seed int) {
			defer wg.Done()
			cursor := uint64(seed)
			for {
				select {
				case <-done:
					return
				default:
				}
				if _, err := store.Since(ctx, cursor, 0); err != nil && !errors.Is(err, ErrCursorUnknown) {
					t.Errorf("Since(%d) = %v, want nil or ErrCursorUnknown", cursor, err)
					return
				}
				cursor = (cursor + 7) % uint64(lastSeq+1)
			}
		}(r)
	}

	wg.Wait()
}
