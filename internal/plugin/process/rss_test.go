//go:build linux

package process

import (
	"os"
	"testing"
)

func TestReadRSS(t *testing.T) {
	t.Run("self process has non-zero RSS", func(t *testing.T) {
		pid := os.Getpid()
		rss, err := ReadRSS(pid)
		if err != nil {
			t.Fatalf("ReadRSS(%d): unexpected error: %v", pid, err)
		}
		if rss == 0 {
			t.Errorf("ReadRSS(%d): expected RSS > 0, got 0", pid)
		}
	})

	t.Run("non-existent PID returns error", func(t *testing.T) {
		// PID 2147483647 (max int32) is extremely unlikely to exist.
		_, err := ReadRSS(2147483647)
		if err == nil {
			t.Error("ReadRSS(2147483647): expected error for non-existent PID, got nil")
		}
	})

	t.Run("pid zero returns error", func(t *testing.T) {
		_, err := ReadRSS(0)
		if err == nil {
			t.Error("ReadRSS(0): expected error, got nil")
		}
	})

	t.Run("negative pid returns error", func(t *testing.T) {
		_, err := ReadRSS(-1)
		if err == nil {
			t.Error("ReadRSS(-1): expected error, got nil")
		}
	})
}
