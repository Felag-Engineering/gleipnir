package container

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDetectPosture(t *testing.T) {
	cases := []struct {
		name        string
		cfg         DetectConfig
		wantPosture Posture
		wantSocket  string
		wantErrSub  string
	}{
		{
			name: "auto prefers rootless podman when both sockets exist",
			cfg: DetectConfig{
				Mode:               "auto",
				PodmanSocketPath:   "/run/user/1000/podman/podman.sock",
				PodmanSocketExists: true,
				DockerSocketPath:   "/var/run/docker.sock",
				DockerSocketExists: true,
			},
			wantPosture: PostureRootlessPodman,
			wantSocket:  "/run/user/1000/podman/podman.sock",
		},
		{
			name: "auto falls back to docker when podman socket absent",
			cfg: DetectConfig{
				Mode:               "auto",
				PodmanSocketPath:   "/run/user/1000/podman/podman.sock",
				PodmanSocketExists: false,
				DockerSocketPath:   "/var/run/docker.sock",
				DockerSocketExists: true,
			},
			wantPosture: PostureDocker,
			wantSocket:  "/var/run/docker.sock",
		},
		{
			name: "empty mode behaves like auto",
			cfg: DetectConfig{
				DockerSocketPath:   "/var/run/docker.sock",
				DockerSocketExists: true,
			},
			wantPosture: PostureDocker,
			wantSocket:  "/var/run/docker.sock",
		},
		{
			name: "auto errors when neither socket exists",
			cfg: DetectConfig{
				Mode:             "auto",
				PodmanSocketPath: "/run/user/1000/podman/podman.sock",
				DockerSocketPath: "/var/run/docker.sock",
			},
			wantErrSub: "no container-runtime socket found",
		},
		{
			name: "explicit manual mode never touches socket state",
			cfg: DetectConfig{
				Mode:               "manual",
				PodmanSocketExists: true,
				DockerSocketExists: true,
			},
			wantPosture: PostureManual,
			wantSocket:  "",
		},
		{
			name: "explicit rootless-podman succeeds when socket exists",
			cfg: DetectConfig{
				Mode:               "rootless-podman",
				PodmanSocketPath:   "/run/user/1000/podman/podman.sock",
				PodmanSocketExists: true,
			},
			wantPosture: PostureRootlessPodman,
			wantSocket:  "/run/user/1000/podman/podman.sock",
		},
		{
			name: "explicit rootless-podman errors when socket missing",
			cfg: DetectConfig{
				Mode:             "rootless-podman",
				PodmanSocketPath: "/run/user/1000/podman/podman.sock",
			},
			wantErrSub: "no socket found",
		},
		{
			name: "explicit docker succeeds when socket exists",
			cfg: DetectConfig{
				Mode:               "docker",
				DockerSocketPath:   "/var/run/docker.sock",
				DockerSocketExists: true,
			},
			wantPosture: PostureDocker,
			wantSocket:  "/var/run/docker.sock",
		},
		{
			name: "explicit docker errors when socket missing",
			cfg: DetectConfig{
				Mode:             "docker",
				DockerSocketPath: "/var/run/docker.sock",
			},
			wantErrSub: "no socket found",
		},
		{
			name: "socket override bypasses the standard-location check",
			cfg: DetectConfig{
				Mode:           "docker",
				SocketOverride: "/custom/docker.sock",
			},
			wantPosture: PostureDocker,
			wantSocket:  "/custom/docker.sock",
		},
		{
			name: "mode is case-insensitive and trims whitespace",
			cfg: DetectConfig{
				Mode:               "  Manual  ",
				PodmanSocketExists: true,
			},
			wantPosture: PostureManual,
		},
		{
			name: "invalid mode is rejected",
			cfg: DetectConfig{
				Mode: "swarm",
			},
			wantErrSub: "invalid",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := DetectPosture(tc.cfg)

			if tc.wantErrSub != "" {
				if err == nil {
					t.Fatalf("DetectPosture() = %+v, nil; want error containing %q", result, tc.wantErrSub)
				}
				if !strings.Contains(err.Error(), tc.wantErrSub) {
					t.Fatalf("DetectPosture() error = %q, want substring %q", err.Error(), tc.wantErrSub)
				}
				return
			}

			if err != nil {
				t.Fatalf("DetectPosture() unexpected error: %v", err)
			}
			if result.Posture != tc.wantPosture {
				t.Errorf("Posture = %q, want %q", result.Posture, tc.wantPosture)
			}
			if result.SocketPath != tc.wantSocket {
				t.Errorf("SocketPath = %q, want %q", result.SocketPath, tc.wantSocket)
			}
		})
	}
}

// TestLoadDetectConfig exercises the one impure entry point in this file: it
// reads real env vars and stats a real (temp-dir) socket path, then checks
// that the values land in the right DetectConfig fields.
func TestLoadDetectConfig(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", dir)
	t.Setenv("GLEIPNIR_CONTAINER_RUNTIME_MODE", "auto")
	t.Setenv("GLEIPNIR_CONTAINER_SOCKET", "")

	// No socket file yet: LoadDetectConfig should report it absent.
	cfg := LoadDetectConfig()
	if cfg.PodmanSocketExists {
		t.Fatalf("PodmanSocketExists = true before the socket file was created")
	}
	wantPath := filepath.Join(dir, "podman", "podman.sock")
	if cfg.PodmanSocketPath != wantPath {
		t.Fatalf("PodmanSocketPath = %q, want %q", cfg.PodmanSocketPath, wantPath)
	}

	// A regular file at the standard location is enough for socketExists —
	// it only checks presence, not that it's actually a Unix socket.
	if err := touchFile(wantPath); err != nil {
		t.Fatalf("touchFile: %v", err)
	}

	cfg = LoadDetectConfig()
	if !cfg.PodmanSocketExists {
		t.Fatalf("PodmanSocketExists = false after the socket file was created")
	}
	if cfg.Mode != "auto" {
		t.Errorf("Mode = %q, want %q", cfg.Mode, "auto")
	}
}

func TestLogPosture(t *testing.T) {
	cases := []struct {
		name        string
		result      PostureResult
		wantLevel   slog.Level
		wantContain string
	}{
		{
			name:        "docker posture logs the trust implication at warn",
			result:      PostureResult{Posture: PostureDocker, SocketPath: "/var/run/docker.sock"},
			wantLevel:   slog.LevelWarn,
			wantContain: "approximately root",
		},
		{
			name:        "manual posture logs at info",
			result:      PostureResult{Posture: PostureManual},
			wantLevel:   slog.LevelInfo,
			wantContain: "will not write",
		},
		{
			name:        "rootless podman logs at info",
			result:      PostureResult{Posture: PostureRootlessPodman, SocketPath: "/run/user/1000/podman/podman.sock"},
			wantLevel:   slog.LevelInfo,
			wantContain: "posture resolved",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

			LogPosture(logger, tc.result)

			out := buf.String()
			if !strings.Contains(out, tc.wantContain) {
				t.Errorf("log output = %q, want substring %q", out, tc.wantContain)
			}
			if !strings.Contains(out, "level="+tc.wantLevel.String()) {
				t.Errorf("log output = %q, want level %q", out, tc.wantLevel)
			}
		})
	}
}

func touchFile(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	return f.Close()
}
