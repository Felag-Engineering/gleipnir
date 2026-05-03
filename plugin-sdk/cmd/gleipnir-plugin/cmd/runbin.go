package cmd

import (
	"bytes"
	"fmt"
	"os/exec"
)

// runBinary is the function used to invoke a plugin binary with args and
// capture its combined stdout output. It is a package-level variable so tests
// can replace it with a stub without spawning real subprocesses.
//
// The default implementation runs the binary as a subprocess and returns its
// stdout on success, or a wrapped error (containing stderr) on failure.
var runBinary = defaultRunBinary

// defaultRunBinary is the production subprocess runner.
func defaultRunBinary(binary string, args []string) ([]byte, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.Command(binary, args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("run %s %v: %w\nstderr: %s", binary, args, err, stderr.String())
	}
	return stdout.Bytes(), nil
}
