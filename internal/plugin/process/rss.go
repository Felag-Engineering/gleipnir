//go:build linux

package process

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// ReadRSS returns the resident set size of process pid in bytes by reading
// /proc/<pid>/statm. The second field in statm is RSS in pages; we multiply by
// os.Getpagesize() to convert to bytes.
//
// Returns (0, err) when pid <= 0, the file does not exist (process gone), or
// the file cannot be parsed. Callers should skip on error rather than treating
// it as fatal — a process exiting between the snapshot and the read is normal.
func ReadRSS(pid int) (uint64, error) {
	if pid <= 0 {
		return 0, fmt.Errorf("rss: invalid pid %d", pid)
	}

	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/statm", pid))
	if err != nil {
		return 0, fmt.Errorf("rss: read /proc/%d/statm: %w", pid, err)
	}

	fields := strings.Fields(string(data))
	if len(fields) < 2 {
		return 0, fmt.Errorf("rss: unexpected statm format for pid %d: %q", pid, string(data))
	}

	pages, err := strconv.ParseUint(fields[1], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("rss: parse rss pages for pid %d: %w", pid, err)
	}

	return pages * uint64(os.Getpagesize()), nil
}
