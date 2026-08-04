package container

import (
	"strings"
	"testing"
	"time"

	dockercontainer "github.com/docker/docker/api/types/container"
)

// TestToDockerCreateArgs exercises the pure CreateOptions -> SDK-args
// translation without a socket. It is the only part of DockerRuntime that
// this package tests directly — real-daemon coverage is a later, CI-gated
// issue (see internal/plugin/container's package doc).
func TestToDockerCreateArgs(t *testing.T) {
	opts := CreateOptions{
		Name:    "plugin-abc123",
		Image:   "registry.example.com/plugin@sha256:deadbeef",
		Env:     []string{"FOO=bar"},
		Command: []string{"serve"},
		Labels:  map[string]string{"gleipnir.instance_id": "abc123"},
		Network: "gleipnir-plugin-abc123",
		Volume:  VolumeMount{Name: "plugin-abc123-data", MountPath: "/data", ReadOnly: true},
		Resources: Resources{
			MemoryBytes: 128 * 1024 * 1024,
			NanoCPUs:    500_000_000,
		},
	}

	cfg, hostCfg, netCfg := toDockerCreateArgs(opts)

	if cfg.Image != opts.Image {
		t.Errorf("Config.Image = %q, want %q", cfg.Image, opts.Image)
	}
	if len(cfg.Env) != 1 || cfg.Env[0] != "FOO=bar" {
		t.Errorf("Config.Env = %v, want [FOO=bar]", cfg.Env)
	}
	if cfg.Labels["gleipnir.instance_id"] != "abc123" {
		t.Errorf("Config.Labels missing gleipnir.instance_id")
	}

	if hostCfg.NetworkMode != dockercontainer.NetworkMode(opts.Network) {
		t.Errorf("HostConfig.NetworkMode = %q, want %q", hostCfg.NetworkMode, opts.Network)
	}
	if hostCfg.Memory != opts.Resources.MemoryBytes {
		t.Errorf("HostConfig.Memory = %d, want %d", hostCfg.Memory, opts.Resources.MemoryBytes)
	}
	if hostCfg.NanoCPUs != opts.Resources.NanoCPUs {
		t.Errorf("HostConfig.NanoCPUs = %d, want %d", hostCfg.NanoCPUs, opts.Resources.NanoCPUs)
	}
	if len(hostCfg.Mounts) != 1 {
		t.Fatalf("HostConfig.Mounts = %v, want exactly one mount", hostCfg.Mounts)
	}
	mount := hostCfg.Mounts[0]
	if mount.Source != opts.Volume.Name || mount.Target != opts.Volume.MountPath || !mount.ReadOnly {
		t.Errorf("Mounts[0] = %+v, want source=%s target=%s readOnly=true", mount, opts.Volume.Name, opts.Volume.MountPath)
	}

	if _, ok := netCfg.EndpointsConfig[opts.Network]; !ok {
		t.Errorf("NetworkingConfig.EndpointsConfig missing entry for %q", opts.Network)
	}
}

func TestToDockerCreateArgs_NoVolumeMeansNoMounts(t *testing.T) {
	opts := CreateOptions{Name: "x", Image: "img", Network: "net"}
	_, hostCfg, _ := toDockerCreateArgs(opts)
	if len(hostCfg.Mounts) != 0 {
		t.Errorf("Mounts = %v, want none when Volume is unset", hostCfg.Mounts)
	}
}

func TestFromSummary(t *testing.T) {
	created := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	summary := dockercontainer.Summary{
		ID:      "abc123",
		Names:   []string{"/plugin-abc123"},
		Image:   "img@sha256:deadbeef",
		Labels:  map[string]string{"k": "v"},
		State:   string(ContainerStateRunning),
		Created: created.Unix(),
	}

	info := fromSummary(summary)

	if info.ID != ContainerID("abc123") {
		t.Errorf("ID = %q, want abc123", info.ID)
	}
	if info.Name != "plugin-abc123" {
		t.Errorf("Name = %q, want plugin-abc123 (leading slash trimmed)", info.Name)
	}
	if info.State != ContainerStateRunning {
		t.Errorf("State = %q, want %q", info.State, ContainerStateRunning)
	}
	if !info.CreatedAt.Equal(created) {
		t.Errorf("CreatedAt = %v, want %v", info.CreatedAt, created)
	}
}

func TestFromInspectResponse_NilBaseDoesNotPanic(t *testing.T) {
	info := fromInspectResponse(dockercontainer.InspectResponse{})
	if info.ID != "" || info.Name != "" {
		t.Errorf("fromInspectResponse(zero value) = %+v, want zero ContainerInfo", info)
	}
}

func TestFromInspectResponse_FullyPopulated(t *testing.T) {
	created := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	resp := dockercontainer.InspectResponse{
		ContainerJSONBase: &dockercontainer.ContainerJSONBase{
			ID:      "abc123",
			Name:    "/plugin-abc123",
			Created: created.Format(time.RFC3339Nano),
			State: &dockercontainer.State{
				Status: dockercontainer.ContainerState(ContainerStateRunning),
				Health: &dockercontainer.Health{Status: dockercontainer.Healthy},
			},
		},
		Config: &dockercontainer.Config{
			Image:  "img@sha256:deadbeef",
			Labels: map[string]string{"k": "v"},
		},
	}

	info := fromInspectResponse(resp)

	if info.ID != "abc123" {
		t.Errorf("ID = %q, want abc123", info.ID)
	}
	if info.Name != "plugin-abc123" {
		t.Errorf("Name = %q, want plugin-abc123", info.Name)
	}
	if info.State != ContainerStateRunning {
		t.Errorf("State = %q, want %q", info.State, ContainerStateRunning)
	}
	if info.Health != string(dockercontainer.Healthy) {
		t.Errorf("Health = %q, want %q", info.Health, dockercontainer.Healthy)
	}
	if !info.CreatedAt.Equal(created) {
		t.Errorf("CreatedAt = %v, want %v", info.CreatedAt, created)
	}
	if info.Image != "img@sha256:deadbeef" {
		t.Errorf("Image = %q, want img@sha256:deadbeef", info.Image)
	}
	if info.Labels["k"] != "v" {
		t.Errorf("Labels = %v, want k=v", info.Labels)
	}
}

func TestDecodeStats(t *testing.T) {
	body := strings.NewReader(`{
		"cpu_stats": {"cpu_usage": {"total_usage": 200}, "system_cpu_usage": 2000, "online_cpus": 2},
		"precpu_stats": {"cpu_usage": {"total_usage": 100}, "system_cpu_usage": 1000},
		"memory_stats": {"usage": 1048576, "limit": 2097152}
	}`)

	stats, err := decodeStats(body)
	if err != nil {
		t.Fatalf("decodeStats() error = %v", err)
	}
	if stats.CPUPercent != 20 {
		t.Errorf("CPUPercent = %v, want 20", stats.CPUPercent)
	}
	if stats.MemoryUsageBytes != 1048576 {
		t.Errorf("MemoryUsageBytes = %v, want 1048576", stats.MemoryUsageBytes)
	}
	if stats.MemoryLimitBytes != 2097152 {
		t.Errorf("MemoryLimitBytes = %v, want 2097152", stats.MemoryLimitBytes)
	}
}

func TestDecodeStats_MalformedBody(t *testing.T) {
	if _, err := decodeStats(strings.NewReader("not json")); err == nil {
		t.Fatal("decodeStats() with malformed body = nil error, want error")
	}
}

func TestCPUPercent(t *testing.T) {
	cases := []struct {
		name string
		resp dockercontainer.StatsResponse
		want float64
	}{
		{
			name: "zero system delta yields zero percent",
			resp: dockercontainer.StatsResponse{},
			want: 0,
		},
		{
			name: "typical delta computes nonzero percent",
			resp: dockercontainer.StatsResponse{
				CPUStats: dockercontainer.CPUStats{
					CPUUsage:    dockercontainer.CPUUsage{TotalUsage: 200},
					SystemUsage: 2000,
					OnlineCPUs:  2,
				},
				PreCPUStats: dockercontainer.CPUStats{
					CPUUsage:    dockercontainer.CPUUsage{TotalUsage: 100},
					SystemUsage: 1000,
				},
			},
			// (200-100)/(2000-1000) * 2 * 100 = 20
			want: 20,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := cpuPercent(tc.resp)
			if got != tc.want {
				t.Errorf("cpuPercent() = %v, want %v", got, tc.want)
			}
		})
	}
}
