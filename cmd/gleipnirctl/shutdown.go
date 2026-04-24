package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
)

const (
	defaultShutdownTimeout = 6 * time.Minute
	defaultPIDFile         = "/var/run/gleipnir.pid"
	pollInterval           = 500 * time.Millisecond
	progressInterval       = 10 * time.Second
)

func newShutdownCmd() *cobra.Command {
	var (
		timeout time.Duration
		pidFile string
	)
	cmd := &cobra.Command{
		Use:   "shutdown",
		Short: "Send SIGTERM to the Gleipnir server and wait for it to exit cleanly",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runShutdown(cmd, timeout, pidFile)
		},
	}
	cmd.Flags().DurationVar(&timeout, "timeout", defaultShutdownTimeout, "how long to wait for the process to exit")
	cmd.Flags().StringVar(&pidFile, "pid-file", "", "path to the PID file (default: GLEIPNIR_PID_FILE env var, then /var/run/gleipnir.pid)")
	return cmd
}

func runShutdown(cmd *cobra.Command, timeout time.Duration, pidFile string) error {
	resolved := resolvePIDFile(pidFile)

	data, err := os.ReadFile(resolved)
	if err != nil {
		return fmt.Errorf("could not read PID file %q: %w", resolved, err)
	}

	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return fmt.Errorf("PID file %q contains invalid PID: %w", resolved, err)
	}

	p, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("could not find process %d: %w", pid, err)
	}

	if err := p.Signal(syscall.SIGTERM); err != nil {
		return fmt.Errorf("could not send SIGTERM to process %d: %w", pid, err)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "sent SIGTERM to process %d, waiting up to %s for exit...\n", pid, timeout)

	deadline := time.Now().Add(timeout)
	lastProgress := time.Now()

	for {
		// Signal(0) checks liveness without sending a real signal; ESRCH means gone.
		if err := p.Signal(syscall.Signal(0)); err != nil {
			fmt.Fprintln(cmd.OutOrStdout(), "server drained cleanly")
			return nil
		}

		if time.Now().After(deadline) {
			return fmt.Errorf("server did not exit within %s", timeout)
		}

		if time.Since(lastProgress) >= progressInterval {
			fmt.Fprintln(cmd.OutOrStdout(), "still waiting for process to exit...")
			lastProgress = time.Now()
		}

		time.Sleep(pollInterval)
	}
}

// resolvePIDFile applies the precedence: explicit flag > GLEIPNIR_PID_FILE env var > default path.
func resolvePIDFile(flag string) string {
	if flag != "" {
		return flag
	}
	if v := os.Getenv("GLEIPNIR_PID_FILE"); v != "" {
		return v
	}
	return defaultPIDFile
}
