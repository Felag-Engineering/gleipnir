package cmd

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/felag-engineering/gleipnir/plugin-sdk/internal/fakehost"
	"github.com/felag-engineering/gleipnir/plugin-sdk/internal/hostwire"
)

// launchPlugin and newFakeHost are package-level variables so tests can replace
// them with stubs without spawning real subprocesses or gRPC servers.
var (
	launchPlugin = hostwire.Launch
	newFakeHost  = fakehost.New
)

// runOpts collects all flags for the `run` subcommand.
type runOpts struct {
	scenario       string
	capture        string
	replay         string
	maxEvents      int
	watchScope     string
	timeout        time.Duration
	filter         string
	continueOnErr  bool
}

// NewRunCmd returns the cobra.Command for the `run` subcommand.
//
// `gleipnir-plugin run <binary>` boots the plugin against an in-process fake
// host so authors can test their plugin without a live Gleipnir instance.
//
// Exactly one mode flag must be supplied:
//   - --scenario <file.yaml>  — scripted RPC calls with assertions
//   - --capture <file.jsonl>  — record EmitEvent output to disk
//   - --replay <file.jsonl>   — re-execute plugin for each captured event
func NewRunCmd() *cobra.Command {
	var opts runOpts

	cmd := &cobra.Command{
		Use:   "run <binary>",
		Short: "Run a plugin binary against a local fake host",
		Long: `Boot a plugin binary as a go-plugin subprocess connected to an in-process fake
host. Three mutually exclusive modes are available:

  --scenario <file.yaml>
      Non-interactive batch mode. Reads a YAML script of RPC calls and
      expected responses, executes them against the loaded plugin, and
      prints a pass/fail summary. Useful for CI smoke tests and bug repros.

  --capture <file.jsonl>
      Runs the plugin's Trigger.Start loop and writes every EmitEvent call
      the plugin makes to <file.jsonl> in JSONL format. Stop with SIGINT or
      --max-events N. Use this to record a real payload shape from a dev
      Gleipnir instance for later replay.

  --replay <file.jsonl>
      Feeds each captured event back into the plugin by re-exec-ing the
      binary with --replay-event <json>. The plugin must implement that flag
      (see docs/developer/plugin-system-spec.md §14.4). Prints a per-event
      OK/FAILED summary. Use this to iterate on payload parsing offline.

Does NOT simulate signature verification, version mismatch, or a real
LLM/SQLite. No interactive REPL mode.

See plugin-sdk/cmd/gleipnir-plugin/README.md §run for the scenario YAML
schema, JSONL capture format, and the --replay-event convention.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runRun(cmd, args[0], opts)
		},
	}

	cmd.Flags().StringVar(&opts.scenario, "scenario", "", "YAML scenario script to execute against the plugin")
	cmd.Flags().StringVar(&opts.capture, "capture", "", "JSONL file to write captured EmitEvent output to")
	cmd.Flags().StringVar(&opts.replay, "replay", "", "JSONL capture file to replay against the plugin")
	cmd.Flags().IntVar(&opts.maxEvents, "max-events", 0, "stop capture/replay after N events (0 = unlimited)")
	cmd.Flags().StringVar(&opts.watchScope, "watch-scope", "{}", "JSON watch_scope passed to Trigger.Start during capture")
	cmd.Flags().DurationVar(&opts.timeout, "timeout", 30*time.Second, "timeout for the entire run operation")
	cmd.Flags().StringVar(&opts.filter, "filter", "", "event_kind filter for replay, e.g. event_kind=github.push")
	cmd.Flags().BoolVar(&opts.continueOnErr, "continue-on-error", false, "continue replay after a plugin non-zero exit")

	return cmd
}

// runRun validates flags and dispatches to the appropriate mode runner.
func runRun(cmd *cobra.Command, binary string, opts runOpts) error {
	// Exactly one mode flag must be set.
	modeCount := 0
	if opts.scenario != "" {
		modeCount++
	}
	if opts.capture != "" {
		modeCount++
	}
	if opts.replay != "" {
		modeCount++
	}
	if modeCount == 0 {
		return errors.New("one of --scenario, --capture, or --replay is required")
	}
	if modeCount > 1 {
		return errors.New("--scenario, --capture, and --replay are mutually exclusive")
	}

	ctx, cancel := context.WithTimeout(context.Background(), opts.timeout)
	defer cancel()

	switch {
	case opts.scenario != "":
		return runScenario(ctx, cmd, binary, opts.scenario)
	case opts.capture != "":
		return runCapture(ctx, cmd, binary, captureOpts{
			outFile:    opts.capture,
			watchScope: opts.watchScope,
			maxEvents:  opts.maxEvents,
		})
	case opts.replay != "":
		return runReplay(cmd, binary, replayOpts{
			inFile:        opts.replay,
			maxEvents:     opts.maxEvents,
			filter:        opts.filter,
			continueOnErr: opts.continueOnErr,
		})
	}
	return fmt.Errorf("internal error: no mode selected")
}
