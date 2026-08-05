package logcapture

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"
)

func collect(t *testing.T, data []byte) []Line {
	t.Helper()
	var lines []Line
	if err := Decode(bytes.NewReader(data), func(l Line) { lines = append(lines, l) }); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	return lines
}

// The multiplexed framing a TTY-less container really produces.
func TestDecode_FramedStream(t *testing.T) {
	data := bytes.Join([][]byte{
		frame(1, "one\ntwo\n"),
		frame(2, "boom\n"),
		frame(1, "three\n"),
	}, nil)

	lines := collect(t, data)
	want := []Line{
		{StreamStdout, "one"},
		{StreamStdout, "two"},
		{StreamStderr, "boom"},
		{StreamStdout, "three"},
	}
	if len(lines) != len(want) {
		t.Fatalf("lines = %v, want %v", lines, want)
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Errorf("[%d] = %+v, want %+v", i, lines[i], want[i])
		}
	}
}

// A TTY container's output is not framed. Guessing wrong here would corrupt the
// first line of a crash message, which is the line that matters most, so the
// sniff has to fall back cleanly.
func TestDecode_RawStream(t *testing.T) {
	lines := collect(t, []byte("panic: nil map\ngoroutine 1 [running]:\n"))
	if len(lines) != 2 {
		t.Fatalf("lines = %v, want 2", lines)
	}
	if lines[0].Text != "panic: nil map" || lines[0].Stream != StreamUnknown {
		t.Errorf("[0] = %+v", lines[0])
	}
}

// Text that happens to begin with a byte a frame header could start with must
// not be mistaken for one. The three-zero-bytes and plausible-length checks are
// what make the sniff conservative.
func TestDecode_TextThatLooksLikeAFrameHeader(t *testing.T) {
	// 0x01 followed by ordinary text: bytes 1..3 are not zero, so this is raw.
	data := append([]byte{0x01}, []byte("normal log output here\n")...)
	lines := collect(t, data)
	if len(lines) != 1 {
		t.Fatalf("lines = %v, want 1", lines)
	}
	if !strings.Contains(lines[0].Text, "normal log output here") {
		t.Errorf("raw text was mangled: %q", lines[0].Text)
	}
}

// A header claiming a frame larger than the runtime ever emits is text, not a
// frame.
func TestDecode_ImplausibleFrameLengthIsTreatedAsText(t *testing.T) {
	header := make([]byte, 8)
	header[0] = 1
	binary.BigEndian.PutUint32(header[4:], 1<<30)
	lines := collect(t, append(header, []byte("tail\n")...))
	// It must not hang or allocate a gigabyte; whatever it produces, it read
	// the stream as text.
	for _, l := range lines {
		if l.Stream != StreamUnknown {
			t.Errorf("line %+v was decoded as a frame", l)
		}
	}
}

// A frame truncated mid-payload — the shape of a stream cut off when a
// container is killed. What was read is still evidence.
func TestDecode_TruncatedFrameYieldsWhatWasRead(t *testing.T) {
	full := frame(2, "fatal: out of mem\n")
	lines := collect(t, full[:len(full)-6])
	if len(lines) == 0 {
		t.Fatal("a truncated frame produced nothing; the partial line is the evidence")
	}
}

func TestDecode_EmptyStream(t *testing.T) {
	if lines := collect(t, nil); len(lines) != 0 {
		t.Errorf("empty stream produced %v", lines)
	}
}

func TestDecode_BlankLinesAreSkipped(t *testing.T) {
	lines := collect(t, frame(1, "a\n\n\nb\n"))
	if len(lines) != 2 {
		t.Errorf("lines = %v, want the two non-empty ones", lines)
	}
}

func TestTruncate(t *testing.T) {
	short := "fine"
	if truncate(short) != short {
		t.Error("a short line was altered")
	}
	long := strings.Repeat("y", maxLineBytes+10)
	got := truncate(long)
	if !strings.HasSuffix(got, truncationMarker) {
		t.Error("truncation was not marked")
	}
	if len(got) != maxLineBytes+len(truncationMarker) {
		t.Errorf("truncated length = %d", len(got))
	}
}
