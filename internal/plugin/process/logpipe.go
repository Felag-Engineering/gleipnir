package process

import (
	"bufio"
	"context"
	"io"
	"log/slog"
)

const (
	// logpipeMaxTokenSize is the per-line read buffer for plugin stderr. Stack
	// traces and structured JSON blobs can be large; 1 MiB accommodates them
	// without unbounded memory growth.
	logpipeMaxTokenSize = 1 * 1024 * 1024 // 1 MiB
)

// PipeLines returns a WriteCloser and a done channel. Lines written to the
// WriteCloser are split on newline boundaries and logged via logger at the
// given level with an extra "stream" attribute set to stream (e.g. "stderr").
// The done channel is closed when the write end is closed, signalling that the
// upstream has finished writing (e.g. the plugin process exited and closed its
// stderr pipe).
//
// The scanner buffer is capped at logpipeMaxTokenSize. Lines that exceed that
// limit are logged as a single warning and dropped; they do not interrupt the
// remaining output.
//
// Callers should call Close() on the returned writer when done to release
// goroutine resources and unblock any reader waiting on the done channel.
//
// ctx is captured by the goroutine so that log lines carry run_id/policy_id
// correlation from the parent span even though the goroutine outlives the call.
func PipeLines(ctx context.Context, logger *slog.Logger, level slog.Level, stream string) (io.WriteCloser, <-chan struct{}) {
	pr, pw := io.Pipe()
	done := make(chan struct{})

	go func() {
		defer close(done)

		scanner := bufio.NewScanner(pr)
		scanner.Buffer(make([]byte, logpipeMaxTokenSize), logpipeMaxTokenSize)

		for scanner.Scan() {
			line := scanner.Text()
			logger.Log(ctx, level, line, "stream", stream)
		}

		if err := scanner.Err(); err != nil {
			// The most common cause is a line too long for the buffer. Log it
			// once and continue draining; the scanner cannot recover after
			// ErrTooLong, so we log and break.
			logger.WarnContext(ctx, "plugin log line too long, truncated", "stream", stream, "err", err)
		}

		// Drain any remaining bytes from the pipe so the writer is not blocked
		// waiting for us to consume data after scanner stops.
		_, _ = io.Copy(io.Discard, pr)
	}()

	return pw, done
}
