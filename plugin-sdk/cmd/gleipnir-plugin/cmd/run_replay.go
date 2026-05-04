package cmd

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"
)

// replayOpts holds the options for --replay mode.
type replayOpts struct {
	inFile        string
	maxEvents     int
	filter        string
	continueOnErr bool
}

// replayLineHeader is what we expect at sequence -1 in the JSONL file.
type replayLineHeader struct {
	Sequence             int    `json:"sequence"`
	CaptureFormatVersion int    `json:"capture_format_version"`
	Binary               string `json:"binary"`
	CapturedAt           string `json:"captured_at"`
}

// replayLineEvent is what we expect for event lines (sequence >= 1).
type replayLineEvent struct {
	Sequence    int    `json:"sequence"`
	EventID     string `json:"event_id"`
	EventKind   string `json:"event_kind"`
	PayloadJSON string `json:"payload_json"`
}

// runReplay is the entry point for --replay mode.
//
// Replay contract: the plugin binary must implement a --replay-event flag.
// When invoked with that flag the host pipes the JSON event payload to the
// plugin's stdin; the plugin must io.ReadAll(os.Stdin) and parse it. The
// plugin should process the event as if it had received it from the substrate,
// and exit 0 on success or non-zero on failure. Using stdin avoids ARG_MAX
// (~2MB on Linux) which would silently truncate large webhook payloads passed
// as CLI arguments. This convention is documented in the plugin spec (§14.4)
// and in the README.
//
// This runner opens the JSONL file, validates the header, and re-executes the
// binary once per event that passes the optional --filter. It prints a
// per-event summary line and exits 1 on the first failure unless
// --continue-on-error is set.
func runReplay(cmd *cobra.Command, binary string, opts replayOpts) error {
	return runReplayCtx(context.Background(), cmd, binary, opts)
}

func runReplayCtx(ctx context.Context, cmd *cobra.Command, binary string, opts replayOpts) error {
	f, err := os.Open(opts.inFile)
	if err != nil {
		return fmt.Errorf("open replay file: %w", err)
	}
	defer f.Close()

	lines, err := readJSONLLines(f)
	if err != nil {
		return fmt.Errorf("read replay file: %w", err)
	}
	if len(lines) == 0 {
		return fmt.Errorf("replay file is empty")
	}

	// First line must be the header (sequence = -1).
	var header replayLineHeader
	if err := json.Unmarshal([]byte(lines[0]), &header); err != nil {
		return fmt.Errorf("replay file line 1: invalid JSON: %w", err)
	}
	if header.Sequence != -1 {
		return fmt.Errorf("replay file line 1: expected header (sequence=-1), got sequence=%d", header.Sequence)
	}
	if header.CaptureFormatVersion != 1 {
		return fmt.Errorf("replay file: unsupported capture_format_version %d (want 1)", header.CaptureFormatVersion)
	}

	// Parse the event_kind filter if provided.
	var filterKind string
	if opts.filter != "" {
		const prefix = "event_kind="
		if !strings.HasPrefix(opts.filter, prefix) {
			return fmt.Errorf("--filter must be in the form event_kind=<kind>, got %q", opts.filter)
		}
		filterKind = strings.TrimPrefix(opts.filter, prefix)
		if filterKind == "" {
			return fmt.Errorf("--filter event_kind= requires a non-empty kind value, e.g. event_kind=github.push")
		}
	}

	var succeeded, failed, skipped int

	for lineIdx, line := range lines[1:] {
		lineNum := lineIdx + 2 // human-readable 1-based, offset by header

		var evt replayLineEvent
		if err := json.Unmarshal([]byte(line), &evt); err != nil {
			return fmt.Errorf("replay file line %d: invalid JSON: %w", lineNum, err)
		}

		// Apply kind filter.
		if filterKind != "" && evt.EventKind != filterKind {
			skipped++
			continue
		}

		// Respect max-events limit.
		if opts.maxEvents > 0 && (succeeded+failed) >= opts.maxEvents {
			break
		}

		exitErr := replayEvent(ctx, binary, evt.PayloadJSON, cmd.OutOrStdout(), cmd.ErrOrStderr())
		if exitErr == nil {
			succeeded++
			fmt.Fprintf(cmd.OutOrStdout(), "[%d] event_id=%s kind=%s OK\n", evt.Sequence, evt.EventID, evt.EventKind)
		} else {
			failed++
			fmt.Fprintf(cmd.OutOrStdout(), "[%d] event_id=%s kind=%s FAILED: %v\n", evt.Sequence, evt.EventID, evt.EventKind, exitErr)
			if !opts.continueOnErr {
				return fmt.Errorf("replay stopped at sequence %d (use --continue-on-error to proceed past failures)", evt.Sequence)
			}
		}
	}

	fmt.Fprintf(cmd.OutOrStdout(), "\nreplay complete: %d OK, %d FAILED, %d skipped\n", succeeded, failed, skipped)
	if failed > 0 {
		return fmt.Errorf("%d event(s) failed during replay", failed)
	}
	return nil
}

// replayEvent executes the plugin binary with --replay-event and pipes the
// payloadJSON to its stdin.
//
// The --replay-event convention is a documented SDK contract: plugin authors
// who implement this flag can use `gleipnir-plugin run --replay` to iterate on
// their payload parsing in a tight loop without a live host. Plugins that do
// not implement this flag will produce a non-zero exit, which the replay runner
// reports as FAILED.
//
// The payload is delivered via stdin rather than as a CLI argument to avoid the
// ~2MB ARG_MAX limit on Linux (E2BIG) — a critical constraint given the JSONL
// scanner supports up to 16MB payloads.
//
// Subprocess stderr is always forwarded to errW so the author can see parse
// errors and panics. Subprocess stdout is captured and forwarded to outW only
// on failure, keeping successful output quiet by default.
func replayEvent(ctx context.Context, binary, payloadJSON string, outW, errW io.Writer) error {
	var stdoutBuf bytes.Buffer
	cmd := exec.CommandContext(ctx, binary, "--replay-event")
	cmd.Stdin = strings.NewReader(payloadJSON)
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = errW
	err := cmd.Run()
	if err != nil {
		// Show captured stdout so the author can see any diagnostic output the
		// plugin wrote before exiting non-zero.
		if stdoutBuf.Len() > 0 {
			_, _ = io.Copy(outW, &stdoutBuf)
		}
	}
	return err
}

// readJSONLLines reads all non-empty lines from r.
//
// The scanner buffer is sized at 1MB initial / 16MB max to accommodate large
// webhook payloads that exceed bufio's default 64KB token limit.
func readJSONLLines(r io.Reader) ([]string, error) {
	var lines []string
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1024*1024), 16*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line != "" {
			lines = append(lines, line)
		}
	}
	if err := sc.Err(); err != nil {
		if errors.Is(err, bufio.ErrTooLong) {
			return nil, fmt.Errorf("a line in the JSONL file exceeds 16MB; consider splitting the capture file")
		}
		return nil, err
	}
	return lines, nil
}
