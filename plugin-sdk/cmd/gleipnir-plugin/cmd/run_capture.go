package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	handshakev1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/handshake/v1"
	hostv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/host/v1"
	triggerv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/trigger/v1"
	"github.com/felag-engineering/gleipnir/plugin-sdk/hostwire"
	"github.com/felag-engineering/gleipnir/plugin-sdk/internal/fakehost"
)

// captureOpts holds the options for --capture mode.
type captureOpts struct {
	outFile    string
	watchScope string
	maxEvents  int
}

// captureHeader is the first JSONL line written to a capture file. It records
// metadata about the capture session so the reader can validate compatibility.
type captureHeader struct {
	Sequence             int    `json:"sequence"`               // always -1
	CaptureFormatVersion int    `json:"capture_format_version"` // always 1
	Binary               string `json:"binary"`
	CapturedAt           string `json:"captured_at"`
}

// captureRecord is one event line in the JSONL file.
type captureRecord struct {
	CapturedAt  string `json:"captured_at"`
	Sequence    int    `json:"sequence"`
	EventID     string `json:"event_id"`
	EventKind   string `json:"event_kind"`
	PayloadJSON string `json:"payload_json"`
	// WatchScopeJSON is the watch scope passed to Trigger.Start by the
	// capture host (--watch-scope CLI flag). It is the same value for every
	// record in a single capture file; it does NOT vary per emitted event.
	WatchScopeJSON string `json:"watch_scope_json,omitempty"`
}

// runCapture is the entry point for --capture mode. It launches the plugin,
// calls Trigger.Start to kick off its substrate loop, and writes every
// EmitEvent call to a JSONL file.
//
// The capture stops on SIGINT or when maxEvents is reached (if > 0).
func runCapture(ctx context.Context, cmd *cobra.Command, binary string, opts captureOpts) error {
	outFile, err := os.Create(opts.outFile)
	if err != nil {
		return fmt.Errorf("create capture file: %w", err)
	}
	defer outFile.Close()

	// sequence is incremented atomically for each event so concurrent callbacks
	// produce stable, non-repeating sequence numbers.
	var sequence atomic.Int64
	var writeMu sync.Mutex // serializes JSONL writes; sequence is atomic but file.Write is not
	var stopCh = make(chan struct{})

	// Write the JSONL header before starting the plugin.
	header := captureHeader{
		Sequence:             -1,
		CaptureFormatVersion: 1,
		Binary:               binary,
		CapturedAt:           time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := writeJSONLLine(outFile, header); err != nil {
		return fmt.Errorf("write capture header: %w", err)
	}

	hostInstance := newFakeHost(fakehost.Options{
		OnEmitEvent: func(req *hostv1.EmitEventRequest) {
			seq := int(sequence.Add(1))
			rec := captureRecord{
				CapturedAt:     time.Now().UTC().Format(time.RFC3339Nano),
				Sequence:       seq,
				EventID:        req.GetEventId(),
				EventKind:      req.GetEventKind(),
				PayloadJSON:    req.GetPayloadJson(),
				WatchScopeJSON: opts.watchScope,
			}
			// Errors here are not surfaced; a write failure (e.g. disk full)
			// will appear as a missing record in the output file.
			writeMu.Lock()
			_ = writeJSONLLine(outFile, rec)
			writeMu.Unlock()

			if opts.maxEvents > 0 && seq >= opts.maxEvents {
				select {
				case <-stopCh:
				default:
					close(stopCh)
				}
			}
		},
	})

	pluginClient, teardown, err := launchPlugin(ctx, binary, hostInstance, hostwire.Options{})
	if err != nil {
		return fmt.Errorf("launch plugin: %w", err)
	}
	defer teardown()

	// Negotiate to confirm the plugin exposes TRIGGER capability.
	resp, err := pluginClient.Handshake.Negotiate(ctx, &handshakev1.NegotiateRequest{
		HostVersion: "0.0.0-dev",
		ExpectedCapabilities: []handshakev1.ServiceCapability{
			handshakev1.ServiceCapability_SERVICE_CAPABILITY_TRIGGER,
		},
	})
	if err != nil {
		return fmt.Errorf("handshake negotiate: %w", err)
	}
	if !resp.GetOk() {
		return fmt.Errorf("plugin rejected handshake: %s", resp.GetErrorDetail())
	}

	hasTrigger := false
	for _, cap := range resp.GetActualCapabilities() {
		if cap == handshakev1.ServiceCapability_SERVICE_CAPABILITY_TRIGGER {
			hasTrigger = true
			break
		}
	}
	if !hasTrigger {
		return fmt.Errorf("plugin does not declare TRIGGER capability — cannot capture")
	}

	// Start the trigger loop. Trigger.Start is a server-streaming RPC that runs
	// until the context is cancelled or the plugin exits.
	triggerCtx, triggerCancel := context.WithCancel(ctx)
	defer triggerCancel()

	stream, err := pluginClient.Trigger.Start(triggerCtx, &triggerv1.StartRequest{
		WatchScopeJson: opts.watchScope,
	})
	if err != nil {
		return fmt.Errorf("trigger start: %w", err)
	}

	// Listen for SIGINT so the user can stop capture with Ctrl+C.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	fmt.Fprintf(cmd.OutOrStdout(), "capturing events to %s (Ctrl+C to stop)...\n", opts.outFile)

	// Drain the stream in a goroutine so we can also listen for stop signals.
	// Trigger.Start stream messages are emitted by some plugin implementations
	// in addition to calling EmitEvent directly. We drain the stream to keep the
	// connection alive but the authoritative capture tap is OnEmitEvent.
	drainDone := make(chan error, 1)
	go func() {
		for {
			_, err := stream.Recv()
			if err != nil {
				drainDone <- err
				return
			}
		}
	}()

	select {
	case <-sigCh:
		fmt.Fprintf(cmd.OutOrStdout(), "\ninterrupted — closing capture\n")
	case <-stopCh:
		fmt.Fprintf(cmd.OutOrStdout(), "\nmax-events reached — closing capture\n")
	case err := <-drainDone:
		// io.EOF is the normal end-of-stream signal when the plugin's
		// Trigger.Start returns cleanly; only surface real errors.
		if err != nil && !errors.Is(err, io.EOF) {
			fmt.Fprintf(cmd.OutOrStdout(), "stream ended: %v\n", err)
		}
	case <-ctx.Done():
		fmt.Fprintf(cmd.OutOrStdout(), "timeout\n")
	}

	total := int(sequence.Load())
	fmt.Fprintf(cmd.OutOrStdout(), "captured %d event(s) to %s\n", total, opts.outFile)
	return nil
}

// writeJSONLLine marshals v to JSON and writes it as a single line followed by
// a newline to w.
func writeJSONLLine(w *os.File, v interface{}) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	_, err = w.Write(b)
	return err
}
