//go:build !linux

package process

import "errors"

// ReadRSS is not supported on non-Linux platforms. The Docker container is
// always Linux, so this stub is only reached in local development on macOS/Windows.
func ReadRSS(pid int) (uint64, error) {
	return 0, errors.New("rss: unsupported platform")
}
