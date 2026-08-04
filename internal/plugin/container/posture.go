package container

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// Posture identifies which container-runtime socket Gleipnir is talking to,
// in the order spec §7 recommends them.
type Posture string

const (
	// PostureRootlessPodman is the recommended posture: a rootless Podman
	// socket, where the socket does not carry root-on-host trust.
	PostureRootlessPodman Posture = "rootless-podman"
	// PostureDocker is the fallback posture. The Docker socket is
	// approximately root on host — self-constraint (ValidateCreate) is the
	// only thing standing between a hostile create call and the host, so it
	// is not optional under this posture either.
	PostureDocker Posture = "docker"
	// PostureManual is the escape hatch: the operator declares plugin
	// containers in their own compose file. Gleipnir may still read the
	// socket for discovery/health-checks (a later issue), but every write
	// operation must be impossible — see ReadOnlyRuntime.
	PostureManual Posture = "manual"
)

// Standard socket locations probed when GLEIPNIR_CONTAINER_RUNTIME_MODE is
// unset or "auto".
const (
	dockerSocketPath        = "/var/run/docker.sock"
	rootlessPodmanSocketDir = "podman/podman.sock"
)

// DetectConfig holds the (already-gathered) inputs to posture detection.
// LoadDetectConfig populates one from the environment and filesystem;
// DetectPosture itself is a pure function so tests can exercise every
// env/socket-path permutation without touching the real filesystem.
type DetectConfig struct {
	// Mode is the operator's explicit override, from
	// GLEIPNIR_CONTAINER_RUNTIME_MODE. One of "" / "auto" (probe standard
	// locations), "rootless-podman", "docker", or "manual".
	Mode string

	// SocketOverride is GLEIPNIR_CONTAINER_SOCKET: an explicit socket path
	// that replaces the standard-location probe for whichever posture Mode
	// selects. Only meaningful together with an explicit (non-auto) Mode.
	SocketOverride string

	// PodmanSocketPath is the standard-location candidate for the rootless
	// Podman socket (typically $XDG_RUNTIME_DIR/podman/podman.sock).
	// PodmanSocketExists reports whether a socket file was found there.
	PodmanSocketPath   string
	PodmanSocketExists bool

	// DockerSocketPath is the standard-location candidate for the Docker
	// socket (/var/run/docker.sock). DockerSocketExists reports whether a
	// socket file was found there.
	DockerSocketPath   string
	DockerSocketExists bool
}

// PostureResult is what DetectPosture resolves to: the posture Gleipnir
// should operate under, and the socket path to dial (empty for manual mode,
// since a manual-mode Runtime may have no socket connection at all).
type PostureResult struct {
	Posture    Posture
	SocketPath string
}

// LoadDetectConfig reads GLEIPNIR_CONTAINER_RUNTIME_MODE,
// GLEIPNIR_CONTAINER_SOCKET, and XDG_RUNTIME_DIR from the environment and
// stats the standard socket locations, producing the inputs DetectPosture
// needs. This is the only impure entry point in this file — DetectPosture
// itself takes a DetectConfig value so tests never need a real socket.
func LoadDetectConfig() DetectConfig {
	podmanPath := ""
	if xdg := os.Getenv("XDG_RUNTIME_DIR"); xdg != "" {
		podmanPath = filepath.Join(xdg, rootlessPodmanSocketDir)
	}

	cfg := DetectConfig{
		Mode:             os.Getenv("GLEIPNIR_CONTAINER_RUNTIME_MODE"),
		SocketOverride:   os.Getenv("GLEIPNIR_CONTAINER_SOCKET"),
		PodmanSocketPath: podmanPath,
		DockerSocketPath: dockerSocketPath,
	}
	if podmanPath != "" {
		cfg.PodmanSocketExists = socketExists(podmanPath)
	}
	cfg.DockerSocketExists = socketExists(dockerSocketPath)
	return cfg
}

// socketExists reports whether path names an existing filesystem entry.
// It does not check that the entry is actually a Unix socket — a dangling
// dial attempt against a wrong-typed file surfaces its own clear error later,
// and requiring a syscall-level socket check here would make this untestable
// without a real socket.
func socketExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// DetectPosture resolves cfg to a PostureResult, or returns an error when an
// explicit mode's required socket is missing, or when auto-detection finds
// neither standard socket.
//
// Detection order mirrors spec §7: rootless Podman first (recommended),
// Docker as the fallback, manual only on explicit operator request (it is
// never auto-selected — that would silently disable writes the operator did
// not ask to disable).
func DetectPosture(cfg DetectConfig) (PostureResult, error) {
	mode := normalizeMode(cfg.Mode)

	switch mode {
	case "manual":
		return PostureResult{Posture: PostureManual}, nil

	case "rootless-podman":
		path := firstNonEmpty(cfg.SocketOverride, cfg.PodmanSocketPath)
		if path == "" || (cfg.SocketOverride == "" && !cfg.PodmanSocketExists) {
			return PostureResult{}, fmt.Errorf(
				"container: GLEIPNIR_CONTAINER_RUNTIME_MODE=rootless-podman but no socket found at %q; "+
					"set GLEIPNIR_CONTAINER_SOCKET to override", cfg.PodmanSocketPath)
		}
		return PostureResult{Posture: PostureRootlessPodman, SocketPath: path}, nil

	case "docker":
		path := firstNonEmpty(cfg.SocketOverride, cfg.DockerSocketPath)
		if path == "" || (cfg.SocketOverride == "" && !cfg.DockerSocketExists) {
			return PostureResult{}, fmt.Errorf(
				"container: GLEIPNIR_CONTAINER_RUNTIME_MODE=docker but no socket found at %q; "+
					"set GLEIPNIR_CONTAINER_SOCKET to override", cfg.DockerSocketPath)
		}
		return PostureResult{Posture: PostureDocker, SocketPath: path}, nil

	case "auto", "":
		if cfg.PodmanSocketExists {
			return PostureResult{Posture: PostureRootlessPodman, SocketPath: cfg.PodmanSocketPath}, nil
		}
		if cfg.DockerSocketExists {
			return PostureResult{Posture: PostureDocker, SocketPath: cfg.DockerSocketPath}, nil
		}
		return PostureResult{}, fmt.Errorf(
			"container: no container-runtime socket found at %q or %q; "+
				"set GLEIPNIR_CONTAINER_RUNTIME_MODE=manual to manage plugin containers via your own compose file",
			cfg.PodmanSocketPath, cfg.DockerSocketPath)

	default:
		return PostureResult{}, fmt.Errorf(
			"container: invalid GLEIPNIR_CONTAINER_RUNTIME_MODE %q; want one of auto, rootless-podman, docker, manual",
			cfg.Mode)
	}
}

func normalizeMode(mode string) string {
	return strings.ToLower(strings.TrimSpace(mode))
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// LogPosture records the resolved posture at startup, plainly stating the
// Docker fallback's trust implication (spec §7: "socket ≈ root on host; no
// socket-proxy truly fixes create-with-hostile-binds") rather than burying it
// in documentation nobody reads at deploy time.
func LogPosture(logger *slog.Logger, result PostureResult) {
	switch result.Posture {
	case PostureDocker:
		logger.Warn(
			"container runtime posture is docker: the daemon socket is approximately root on the host; "+
				"self-constraint validates every create call but cannot fix a compromised daemon — "+
				"prefer rootless Podman where available",
			"posture", string(result.Posture),
			"socket", result.SocketPath,
		)
	case PostureManual:
		logger.Info(
			"container runtime posture is manual: Gleipnir will not write to any container-runtime socket",
			"posture", string(result.Posture),
		)
	default:
		logger.Info(
			"container runtime posture resolved",
			"posture", string(result.Posture),
			"socket", result.SocketPath,
		)
	}
}
