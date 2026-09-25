package events

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// segmentMaxRecords bounds how many records one segment file holds before
// FileStore rotates to a new one. Small enough that retention eviction
// (whole-segment granularity) never has to wait long past its window for a
// segment to close off; large enough that a busy event source does not
// rotate constantly.
const segmentMaxRecords = 1000

// segmentFileExt is the on-disk suffix for a FileStore segment file. Chosen
// to be self-describing without implying a stricter format than "one JSON
// object per line" actually is.
const segmentFileExt = ".jsonl"

// fileRecord is FileStore's own on-disk record shape. Deliberately not
// StoredEvent itself: the persistence format is this package's private
// concern, not something a caller of the Store interface should have to
// keep in sync with an in-memory type's field tags.
type fileRecord struct {
	Seq    uint64    `json:"seq"`
	Type   string    `json:"type"`
	ID     string    `json:"id"`
	Source string    `json:"source"`
	Time   time.Time `json:"time"`
	Data   any       `json:"data,omitempty"`
}

func toFileRecord(e StoredEvent) fileRecord {
	return fileRecord{Seq: e.Seq, Type: e.Type, ID: e.ID, Source: e.Source, Time: e.Time, Data: e.Data}
}

func (r fileRecord) toStoredEvent() StoredEvent {
	return StoredEvent{Seq: r.Seq, Type: r.Type, ID: r.ID, Source: r.Source, Time: r.Time, Data: r.Data}
}

// segmentMeta is the in-memory index FileStore keeps per segment file, so
// Since and retention compaction never have to re-scan a file just to learn
// its bounds.
type segmentMeta struct {
	path       string
	firstSeq   uint64
	lastSeq    uint64
	newestTime time.Time // Time of the segment's last record; retention's cutoff
	count      int
}

// FileStore is a durable, file-backed Store implementation: an append-only
// segment log under one directory on the plugin's instance volume. It is
// the backing NewBufferWithStore needs to survive a container restart —
// the in-memory ring NewBuffer keeps loses everything across one.
//
// Crash safety: every Append does an ordinary write followed by an fsync
// before returning success. Write-then-rename (the usual pattern for a
// small file whose entire content changes atomically) does not fit here —
// a segment grows one record at a time, and rewriting the whole file on
// every Append to get rename's atomicity would turn an O(1) append into an
// O(n) one. fsync-per-append gives the property this format actually
// needs instead: once Append returns nil, that record is durable, and the
// only record a crash can ever leave torn is the one being written when
// the crash happened — never an earlier one. NewFileStore's recovery scan
// relies on exactly that: a malformed final line in the newest segment is
// evidence of a torn write in progress and is dropped; the same condition
// anywhere else on disk is real corruption and is reported as an error.
//
// The zero value is not usable; construct with NewFileStore.
type FileStore struct {
	mu        sync.Mutex
	dir       string
	retention time.Duration
	clock     func() time.Time

	segments  []*segmentMeta // ascending by firstSeq; always non-empty once anything has ever been appended
	current   *segmentMeta   // == segments[len(segments)-1] when segments is non-empty
	file      *os.File       // open handle for current, nil until the first Append
	latestSeq uint64
	oldestSeq uint64 // firstSeq of segments[0]; 0 iff segments is empty
}

// FileStoreOption configures a FileStore constructed by NewFileStore.
type FileStoreOption func(*FileStore)

// WithClock overrides the clock FileStore reads to decide whether a
// segment has aged out of retention. Tests inject a fake clock here rather
// than depending on wall-clock timing (docs/developer/testing-patterns.md).
func WithClock(clock func() time.Time) FileStoreOption {
	return func(s *FileStore) { s.clock = clock }
}

// NewFileStore opens (or creates) a durable event store rooted at dir. A
// segment whose newest record is older than retention is evicted the next
// time Append or Since runs — except the single newest segment, which is
// always kept regardless of age, so LatestSeq (and therefore the Buffer's
// sequence numbering) survives an idle period longer than retention
// without resetting to zero after a restart (see the DoD: the sequence
// stays monotonic across a reopen). retention must be positive: FileStore
// exists to keep disk use bounded, so there is no "keep everything forever"
// mode to opt into by accident.
//
// There is deliberately no total-size ceiling. Disk use is bounded by two
// things instead: retention (time) and segmentMaxRecords (a cap on how much
// of one segment can be "the always-kept newest one" while idle). A busy
// event source with a long retention window can still grow the directory
// large between compactions — an explicit byte quota is the kind of thing a
// homelab-scale single-tenant plugin instance does not need, and adding one
// would mean a second, independent way to lose events (quota eviction)
// alongside the one this contract already specifies (retention).
func NewFileStore(dir string, retention time.Duration, opts ...FileStoreOption) (*FileStore, error) {
	if retention <= 0 {
		return nil, fmt.Errorf("events: filestore retention must be positive, got %s", retention)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("events: filestore: create %s: %w", dir, err)
	}

	s := &FileStore{dir: dir, retention: retention, clock: time.Now}
	for _, opt := range opts {
		opt(s)
	}

	if err := s.loadSegments(); err != nil {
		return nil, err
	}
	// s is not shared yet, so calling the *Locked helper without s.mu held
	// is safe here — this evicts anything that aged out while the process
	// was down, before the store is handed to a caller.
	s.compactLocked(s.clock())
	if err := s.openCurrentForAppend(); err != nil {
		return nil, err
	}
	return s, nil
}

// Close releases the open segment file handle. Safe to call on a FileStore
// with nothing yet appended.
func (s *FileStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file == nil {
		return nil
	}
	err := s.file.Close()
	s.file = nil
	return err
}

// Append implements Store. It fsyncs before returning (see FileStore's doc
// comment for why that — not write-then-rename — is this format's crash
// safety mechanism).
func (s *FileStore) Append(_ context.Context, e StoredEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if e.Seq <= s.latestSeq {
		return fmt.Errorf("events: filestore: append seq %d is not after latest %d", e.Seq, s.latestSeq)
	}

	if s.current == nil || s.current.count >= segmentMaxRecords {
		if err := s.rotateLocked(e.Seq); err != nil {
			return fmt.Errorf("events: filestore: rotate segment: %w", err)
		}
	}

	line, err := json.Marshal(toFileRecord(e))
	if err != nil {
		return fmt.Errorf("events: filestore: marshal event %d: %w", e.Seq, err)
	}
	line = append(line, '\n')

	if _, err := s.file.Write(line); err != nil {
		return fmt.Errorf("events: filestore: write event %d: %w", e.Seq, err)
	}
	if err := s.file.Sync(); err != nil {
		return fmt.Errorf("events: filestore: fsync event %d: %w", e.Seq, err)
	}

	s.current.lastSeq = e.Seq
	s.current.newestTime = e.Time
	s.current.count++
	s.latestSeq = e.Seq
	if s.oldestSeq == 0 {
		s.oldestSeq = e.Seq
	}

	s.compactLocked(s.clock())
	return nil
}

// Since implements Store. It mirrors Buffer's own ringSince gap detection
// (buffer.go), against segments on disk instead of an in-memory ring.
//
// s.mu is held for the whole call, including the segment file reads — not
// just the in-memory bookkeeping. Releasing it early (snapshotting the
// segment list, then reading files without the lock) let a concurrent
// Append rotate or evict segments out from under an in-flight Since: two
// goroutines touching the same *segmentMeta without synchronization is a
// real data race (caught by -race), and a segment retention evicted
// between the snapshot and the read would surface as a bare os.IsNotExist
// rather than the ErrCursorUnknown that condition actually is. These are
// local files on the plugin's own instance volume, so serializing a read
// behind the same mutex Append uses is cheap enough to be the right trade.
func (s *FileStore) Since(_ context.Context, after uint64, limit int) ([]StoredEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.compactLocked(s.clock())

	if after == 0 {
		return s.readSegmentsLocked(0, limit)
	}
	if after > s.latestSeq {
		// Refers to a sequence number this store never issued.
		return nil, fmt.Errorf("events: filestore: %w", ErrCursorUnknown)
	}
	if after == s.latestSeq {
		return nil, nil // caught up; nothing new yet
	}
	if s.oldestSeq == 0 || s.oldestSeq > after+1 {
		// The event immediately after the acked point is missing: either
		// retention evicted it, or (oldestSeq == 0) nothing survived at all.
		return nil, fmt.Errorf("events: filestore: %w", ErrCursorUnknown)
	}
	return s.readSegmentsLocked(after, limit)
}

// LatestSeq implements Store.
func (s *FileStore) LatestSeq(_ context.Context) (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.latestSeq, nil
}

// rotateLocked closes the current segment file (if any) and opens a new
// one whose name embeds firstSeq. Caller must hold s.mu.
func (s *FileStore) rotateLocked(firstSeq uint64) error {
	if s.file != nil {
		if err := s.file.Close(); err != nil {
			return fmt.Errorf("close previous segment: %w", err)
		}
	}

	path := segmentPath(s.dir, firstSeq)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create segment %s: %w", path, err)
	}

	// A file's own fsync (Append does one per record) only guarantees the
	// file's CONTENTS are durable — it says nothing about the directory
	// entry that makes the file discoverable at all. Without this, a crash
	// right after rotation could leave every record Append went on to
	// fsync into this segment fully durable on disk, yet the directory
	// itself forgets the file ever existed, and NewFileStore's recovery
	// scan silently loses the whole segment. Syncing here, before the
	// rotation reports success, closes that gap.
	if err := syncDir(s.dir); err != nil {
		_ = f.Close()
		return fmt.Errorf("fsync directory after creating segment %s: %w", path, err)
	}

	meta := &segmentMeta{path: path, firstSeq: firstSeq}
	s.segments = append(s.segments, meta)
	s.current = meta
	s.file = f
	return nil
}

// syncDir fsyncs a directory's own metadata (which files it currently
// contains), as distinct from fsyncing any one file inside it. POSIX does
// not make a file's directory entry durable as a side effect of fsyncing
// the file — the directory needs its own fsync, both after creating a new
// entry (rotateLocked) and after removing one (compactLocked), or a crash
// can make the on-disk file listing disagree with what every individual
// file fsync already promised.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// openCurrentForAppend opens the newest recovered segment (if any) in
// append mode so the next Append continues it rather than rotating
// immediately. Caller must NOT hold s.mu (only called from NewFileStore,
// before s is shared).
func (s *FileStore) openCurrentForAppend() error {
	if s.current == nil {
		return nil // brand new store; the first Append rotates into existence
	}
	f, err := os.OpenFile(s.current.path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("events: filestore: reopen segment %s: %w", s.current.path, err)
	}
	s.file = f
	return nil
}

// compactLocked evicts every segment except the newest whose newest record
// is older than retention, per FileStore's doc comment. Caller must hold
// s.mu.
func (s *FileStore) compactLocked(now time.Time) {
	evicted := false
	for len(s.segments) > 1 && now.Sub(s.segments[0].newestTime) > s.retention {
		// Retention is a best-effort disk bound, not a correctness
		// requirement Since depends on beyond what oldestSeq already
		// tracks: a failed removal (already gone, permissions) would waste
		// disk, not corrupt state, and a plugin process has no operator
		// console to surface it to. Drop the segment from the index either
		// way so oldestSeq/Since agree that it is gone.
		_ = os.Remove(s.segments[0].path)
		s.segments = s.segments[1:]
		evicted = true
	}
	if evicted {
		// Closes the removal-side gap syncDir's own doc comment names: a
		// crash right after os.Remove, before the directory's own metadata
		// reaches disk, could leave the removed segment's directory entry
		// resurrected on the next mount — a later NewFileStore recovery
		// scan would then find a segment this process, and every operator
		// watching it, already believed evicted past retention, dragging
		// stale data back into what Since reports gap-free. Best-effort
		// like the removal itself: this is retention's disk bound, not a
		// correctness guarantee, so a sync failure is not reported.
		_ = syncDir(s.dir)
	}
	if len(s.segments) == 0 {
		s.oldestSeq = 0
	} else {
		s.oldestSeq = s.segments[0].firstSeq
	}
}

// loadSegments scans dir for existing segment files (oldest first),
// rebuilds each one's index, and tolerates a torn trailing write in the
// single newest segment (see FileStore's doc comment). Caller must NOT hold
// s.mu (only called from NewFileStore, before s is shared).
func (s *FileStore) loadSegments() error {
	matches, err := filepath.Glob(filepath.Join(s.dir, "*"+segmentFileExt))
	if err != nil {
		return fmt.Errorf("events: filestore: list segments in %s: %w", s.dir, err)
	}

	type found struct {
		path     string
		firstSeq uint64
	}
	var files []found
	for _, path := range matches {
		firstSeq, ok := parseSegmentFirstSeq(path)
		if !ok {
			continue // not one of our segment files; ignore
		}
		files = append(files, found{path: path, firstSeq: firstSeq})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].firstSeq < files[j].firstSeq })

	for i, f := range files {
		isNewest := i == len(files)-1
		recs, torn, err := readSegmentFile(f.path)
		if err != nil {
			return fmt.Errorf("events: filestore: read segment %s: %w", f.path, err)
		}
		if torn && !isNewest {
			return fmt.Errorf("events: filestore: segment %s has a malformed record it cannot recover from (only the newest segment may end in a torn write)", f.path)
		}
		if torn {
			if err := rewriteSegmentFile(f.path, recs); err != nil {
				return fmt.Errorf("events: filestore: repair torn segment %s: %w", f.path, err)
			}
		}
		if len(recs) == 0 {
			// A rotation that never got a single record synced before a
			// crash (or a repaired segment left with nothing recoverable).
			// Nothing was ever durably promised for this file; drop it.
			if err := os.Remove(f.path); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("events: filestore: remove empty segment %s: %w", f.path, err)
			}
			continue
		}

		meta := &segmentMeta{
			path:       f.path,
			firstSeq:   recs[0].Seq,
			lastSeq:    recs[len(recs)-1].Seq,
			newestTime: recs[len(recs)-1].Time,
			count:      len(recs),
		}
		s.segments = append(s.segments, meta)
	}

	if len(s.segments) > 0 {
		s.current = s.segments[len(s.segments)-1]
		s.latestSeq = s.current.lastSeq
		s.oldestSeq = s.segments[0].firstSeq
	}
	return nil
}

// readSegmentsLocked reads every record with Seq > after out of s.segments
// (already ordered oldest-first), stopping at limit if positive. Caller
// must hold s.mu — see Since's doc comment for why the lock now spans the
// disk reads too.
func (s *FileStore) readSegmentsLocked(after uint64, limit int) ([]StoredEvent, error) {
	var out []StoredEvent
	for _, seg := range s.segments {
		if seg.lastSeq <= after {
			continue // every record in this segment was already delivered
		}
		recs, _, err := readSegmentFile(seg.path)
		if err != nil {
			if os.IsNotExist(err) {
				// Held s.mu the whole call, so nothing in THIS process
				// could have removed seg.path out from under us — this
				// path exists for a segment that vanished some other way
				// (an operator's `rm`, a filesystem restore mid-flight). A
				// caller has no useful recovery from a bare I/O error here
				// either way, and "the store cannot prove this range is
				// gap-free" is exactly what ErrCursorUnknown means.
				return nil, fmt.Errorf("events: filestore: %w", ErrCursorUnknown)
			}
			return nil, fmt.Errorf("events: filestore: read segment %s: %w", seg.path, err)
		}
		for _, rec := range recs {
			if rec.Seq <= after {
				continue
			}
			out = append(out, rec.toStoredEvent())
			if limit > 0 && len(out) >= limit {
				return out, nil
			}
		}
	}
	return out, nil
}

// readSegmentFile parses one segment file's newline-delimited JSON records.
// A line that fails to unmarshal is tolerated ONLY when it is the file's
// last non-empty line (torn == true is returned for it) — anywhere else, a
// malformed line means real corruption and is a hard error. This same
// tolerance rule covers two distinct callers: NewFileStore's crash-recovery
// scan (a genuinely torn write left by a prior crash) and a concurrent
// Since racing a live Append to the currently-open segment (a write that
// simply has not finished yet). Both look identical on disk, and both are
// handled the same way: drop the incomplete line, keep everything before
// it.
func readSegmentFile(path string) (recs []fileRecord, torn bool, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false, err
	}

	lines := bytes.Split(bytes.TrimSuffix(data, []byte("\n")), []byte("\n"))
	for i, line := range lines {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var rec fileRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			if i == len(lines)-1 {
				return recs, true, nil
			}
			return nil, false, fmt.Errorf("malformed record at line %d: %w", i+1, err)
		}
		recs = append(recs, rec)
	}
	return recs, false, nil
}

// rewriteSegmentFile replaces path's contents with exactly recs, so a
// repaired segment's on-disk bytes agree with the in-memory index
// NewFileStore just built for it and the next Append appends cleanly after
// the last good record rather than after a lingering torn tail.
//
// Unlike Append's fsync-per-record (this file's doc comment on FileStore
// explains why that fits the append-only case), a repair replaces the
// file's ENTIRE content in one shot — exactly the case write-then-rename
// exists for. Truncating and rewriting path in place would leave a window,
// mid-write, where a second crash finds the file neither the original torn
// version nor the repaired one: writing to a fresh temp file, fsyncing it,
// then renaming it over path makes the repair atomic — a reader (or a
// second crash) only ever sees the old content or the new content, never a
// mix — and the closing directory fsync makes that rename itself durable
// (see syncDir's doc comment).
func rewriteSegmentFile(path string, recs []fileRecord) error {
	var buf bytes.Buffer
	for _, rec := range recs {
		line, err := json.Marshal(rec)
		if err != nil {
			return fmt.Errorf("marshal record %d: %w", rec.Seq, err)
		}
		buf.Write(line)
		buf.WriteByte('\n')
	}

	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".repair-*")
	if err != nil {
		return fmt.Errorf("create repair temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) // no-op once the rename below has succeeded

	if _, err := tmp.Write(buf.Bytes()); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write repaired segment: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("fsync repaired segment: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close repaired segment: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("rename repaired segment into place: %w", err)
	}
	if err := syncDir(dir); err != nil {
		return fmt.Errorf("fsync directory after repair rename: %w", err)
	}
	return nil
}

// segmentPath renders the on-disk file name for a segment starting at
// firstSeq: zero-padded so lexical and numeric ordering agree, which lets
// loadSegments rely on filepath.Glob's platform-sorted-enough listing
// without depending on it (it still explicitly sorts by parsed firstSeq).
func segmentPath(dir string, firstSeq uint64) string {
	return filepath.Join(dir, fmt.Sprintf("%020d%s", firstSeq, segmentFileExt))
}

// parseSegmentFirstSeq extracts the firstSeq embedded in a segment file
// name written by segmentPath, or ok == false if path is not one.
func parseSegmentFirstSeq(path string) (firstSeq uint64, ok bool) {
	base := filepath.Base(path)
	base = strings.TrimSuffix(base, segmentFileExt)
	n, err := strconv.ParseUint(base, 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}
