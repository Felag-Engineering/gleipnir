package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestResolvePIDFile(t *testing.T) {
	tests := []struct {
		name   string
		flag   string
		envVar string
		want   string
	}{
		{
			name:   "explicit flag takes precedence over env and default",
			flag:   "/tmp/explicit.pid",
			envVar: "/tmp/from-env.pid",
			want:   "/tmp/explicit.pid",
		},
		{
			name:   "env var used when flag is empty",
			flag:   "",
			envVar: "/tmp/from-env.pid",
			want:   "/tmp/from-env.pid",
		},
		{
			name:   "default used when flag and env are empty",
			flag:   "",
			envVar: "",
			want:   defaultPIDFile,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("GLEIPNIR_PID_FILE", tc.envVar)
			got := resolvePIDFile(tc.flag)
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestShutdownCmd_FlagDefaults(t *testing.T) {
	root := newRootCmd()
	var shutdown *cobra.Command
	for _, c := range root.Commands() {
		if c.Use == "shutdown" {
			shutdown = c
			break
		}
	}
	if shutdown == nil {
		t.Fatal("shutdown command not registered")
	}

	timeout, err := shutdown.Flags().GetDuration("timeout")
	if err != nil {
		t.Fatalf("timeout flag not found: %v", err)
	}
	if timeout != defaultShutdownTimeout {
		t.Errorf("default timeout: got %v, want %v", timeout, defaultShutdownTimeout)
	}

	pidFile, err := shutdown.Flags().GetString("pid-file")
	if err != nil {
		t.Fatalf("pid-file flag not found: %v", err)
	}
	if pidFile != "" {
		t.Errorf("default pid-file: got %q, want empty string", pidFile)
	}
}

func TestShutdownCmd_MissingPIDFile(t *testing.T) {
	root := newRootCmd()
	root.SetArgs([]string{"shutdown", "--pid-file", "/nonexistent/gleipnir.pid"})
	err := root.Execute()
	if err == nil {
		t.Fatal("expected error for missing PID file, got nil")
	}
	if !strings.Contains(err.Error(), "could not read PID file") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestShutdownCmd_InvalidPIDContent(t *testing.T) {
	dir := t.TempDir()
	pidPath := filepath.Join(dir, "gleipnir.pid")
	if err := os.WriteFile(pidPath, []byte("not-a-number\n"), 0644); err != nil {
		t.Fatal(err)
	}

	root := newRootCmd()
	root.SetArgs([]string{"shutdown", "--pid-file", pidPath})
	err := root.Execute()
	if err == nil {
		t.Fatal("expected error for invalid PID content, got nil")
	}
	if !strings.Contains(err.Error(), "contains invalid PID") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestShutdownCmd_PIDFileFromEnv(t *testing.T) {
	dir := t.TempDir()
	pidPath := filepath.Join(dir, "gleipnir.pid")
	// Write a PID that won't exist; the error will come from signaling, not file reading.
	if err := os.WriteFile(pidPath, []byte("99999999\n"), 0644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GLEIPNIR_PID_FILE", pidPath)

	root := newRootCmd()
	root.SetArgs([]string{"shutdown"})
	err := root.Execute()
	if err == nil {
		t.Fatal("expected error for nonexistent PID, got nil")
	}
	// The error must come from the signal step, not from failing to find the file.
	if strings.Contains(err.Error(), "could not read PID file") {
		t.Errorf("PID file should have been read via env var, got: %v", err)
	}
}

func TestShutdownCmd_ShutdownRealProcess(t *testing.T) {
	proc := exec.Command("sleep", "60")
	if err := proc.Start(); err != nil {
		t.Fatal(err)
	}
	// Reap the child in the background so it doesn't linger as a zombie after
	// SIGTERM. A zombie still holds its PID entry, so Signal(0) returns success
	// and the polling loop would never detect the process as gone.
	go func() { _ = proc.Wait() }()
	t.Cleanup(func() { _ = proc.Process.Kill() })

	dir := t.TempDir()
	pidPath := filepath.Join(dir, "gleipnir.pid")
	content := strconv.Itoa(proc.Process.Pid) + "\n"
	if err := os.WriteFile(pidPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	root := newRootCmd()
	root.SetOut(&out)
	root.SetArgs([]string{"shutdown", "--pid-file", pidPath, "--timeout", "5s"})
	if err := root.Execute(); err != nil {
		t.Fatalf("expected clean shutdown, got: %v", err)
	}
	if !strings.Contains(out.String(), "server drained cleanly") {
		t.Errorf("unexpected output: %q", out.String())
	}
}
