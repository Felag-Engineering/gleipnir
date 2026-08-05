// Package logcapture streams a plugin container's stdout/stderr into the
// host's structured logger (ADR-056 spec §7 logs; issue #816).
//
// This is the FALLBACK channel, and saying so is load-bearing. Correlated
// logging — the lines carrying run_id and call_id — rides the host endpoint's
// Log method, and ADR-047 chose that over stdout deliberately: stdout has no
// ordering or attribution guarantees, so a line arriving on it cannot be tied
// to the work that produced it. What lands here is everything that never
// reaches structured logging: pre-handshake panics, a misbehaving image, a
// runtime that killed the process before it said anything.
//
// So every captured line is tagged with the instance it came from and marked
// as uncorrelated. An operator reading one should know it is evidence about a
// container, not about a run.
package logcapture

import (
	"bufio"
	"encoding/binary"
	"errors"
	"io"
	"strings"
)

// Stream identifies which of the container's two output streams a line came
// from. The distinction survives capture because "it wrote to stderr" is often
// the whole diagnosis.
type Stream string

const (
	StreamStdout  Stream = "stdout"
	StreamStderr  Stream = "stderr"
	StreamUnknown Stream = "unknown"
)

// Line is one captured output line.
type Line struct {
	Stream Stream
	Text   string
}

// dockerFrameHeader is the 8-byte header the runtime prepends to each chunk
// when the container has no TTY: [stream_type, 0, 0, 0, len_be32].
const dockerFrameHeader = 8

const (
	frameStdout = 1
	frameStderr = 2
)

// maxLineBytes bounds a single captured line. A container emitting one
// enormous line is the same denial-of-service as one emitting many, and a
// scanner with no bound would buffer the whole thing before deciding.
//
// Truncation is marked in the text rather than silent — a line that was cut is
// different evidence from a line that ended.
const maxLineBytes = 8 << 10

const truncationMarker = " …[truncated]"

// Decode reads the runtime's log stream and yields lines.
//
// It handles both framings the runtime can produce. A TTY-less container's
// output is multiplexed with an 8-byte header per chunk; a TTY container's is
// raw. Rather than requiring the caller to know which, this sniffs: a plausible
// header (known stream byte, three zero bytes, a length that is not absurd) is
// treated as one, and anything else is read as raw text. Guessing wrong on raw
// output would corrupt the first line of a crash message, which is the line
// that matters most, so the sniff is deliberately conservative.
func Decode(r io.Reader, emit func(Line)) error {
	buffered := bufio.NewReaderSize(r, 64<<10)

	for {
		header, err := buffered.Peek(dockerFrameHeader)
		if err != nil {
			if len(header) == 0 && isEnd(err) {
				return nil
			}
			// Not enough bytes for a header: whatever is left is raw text.
			return drainRaw(buffered, emit)
		}
		if !looksLikeFrame(header) {
			return drainRaw(buffered, emit)
		}

		if _, err := io.ReadFull(buffered, header[:0:0]); err != nil && !isEnd(err) {
			return err
		}
		if _, err := buffered.Discard(dockerFrameHeader); err != nil {
			return err
		}

		size := binary.BigEndian.Uint32(header[4:8])
		stream := StreamUnknown
		switch header[0] {
		case frameStdout:
			stream = StreamStdout
		case frameStderr:
			stream = StreamStderr
		}

		payload := make([]byte, size)
		if _, err := io.ReadFull(buffered, payload); err != nil {
			if isEnd(err) {
				emitLines(string(payload), stream, emit)
				return nil
			}
			return err
		}
		emitLines(string(payload), stream, emit)
	}
}

// looksLikeFrame reports whether the 8 bytes are plausibly a stream header.
func looksLikeFrame(header []byte) bool {
	if len(header) < dockerFrameHeader {
		return false
	}
	if header[0] != frameStdout && header[0] != frameStderr {
		return false
	}
	if header[1] != 0 || header[2] != 0 || header[3] != 0 {
		return false
	}
	// A frame larger than the runtime ever emits is a sign these bytes are
	// text that happened to start with 0x01 or 0x02.
	const maxPlausibleFrame = 1 << 20
	size := binary.BigEndian.Uint32(header[4:8])
	return size <= maxPlausibleFrame
}

// drainRaw reads the remainder as unframed text.
func drainRaw(r io.Reader, emit func(Line)) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 4<<10), maxLineBytes+len(truncationMarker))
	for scanner.Scan() {
		emit(Line{Stream: StreamUnknown, Text: truncate(scanner.Text())})
	}
	err := scanner.Err()
	if err != nil && errors.Is(err, bufio.ErrTooLong) {
		// A single line longer than the cap. Report what was read rather than
		// abandoning the stream — the oversize line is itself a symptom.
		emit(Line{Stream: StreamUnknown, Text: "[line exceeded the capture limit and was dropped]"})
		return nil
	}
	if isEnd(err) {
		return nil
	}
	return err
}

// emitLines splits a chunk into lines. A chunk boundary is not a line
// boundary, but the runtime frames on write boundaries and a plugin writing
// partial lines is already producing output nothing can interpret.
func emitLines(chunk string, stream Stream, emit func(Line)) {
	for _, text := range strings.Split(strings.TrimRight(chunk, "\n"), "\n") {
		if text == "" {
			continue
		}
		emit(Line{Stream: stream, Text: truncate(text)})
	}
}

func truncate(s string) string {
	if len(s) <= maxLineBytes {
		return s
	}
	return s[:maxLineBytes] + truncationMarker
}

func isEnd(err error) bool {
	return err == nil || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
}
