package container

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	dockercontainer "github.com/docker/docker/api/types/container"
	dockerfilters "github.com/docker/docker/api/types/filters"
	dockermount "github.com/docker/docker/api/types/mount"
	dockernetwork "github.com/docker/docker/api/types/network"
	dockerclient "github.com/docker/docker/client"
)

// DockerRuntime is the production Runtime backed by the official Docker SDK.
// It talks to whatever socket DetectPosture resolved — Podman's
// Docker-compatible socket needs no special-casing anywhere in this file, by
// design; the wire protocol is what the SDK understands regardless of which
// daemon speaks it.
type DockerRuntime struct {
	cli *dockerclient.Client
}

// NewDockerRuntime dials the container-runtime socket at socketPath. Dialing
// a Unix socket does not itself require the daemon to be reachable yet — the
// SDK's client construction only prepares the HTTP transport; the first real
// call surfaces a connection error if the socket is unresponsive.
func NewDockerRuntime(socketPath string) (*DockerRuntime, error) {
	cli, err := dockerclient.NewClientWithOpts(
		dockerclient.WithHost("unix://"+socketPath),
		dockerclient.WithAPIVersionNegotiation(),
	)
	if err != nil {
		return nil, fmt.Errorf("container: dial runtime socket %s: %w", socketPath, err)
	}
	return &DockerRuntime{cli: cli}, nil
}

func (r *DockerRuntime) Create(ctx context.Context, opts CreateOptions) (ContainerID, error) {
	if err := ValidateCreate(opts); err != nil {
		return "", err
	}

	cfg, hostCfg, netCfg := toDockerCreateArgs(opts)
	resp, err := r.cli.ContainerCreate(ctx, cfg, hostCfg, netCfg, nil, opts.Name)
	if err != nil {
		return "", fmt.Errorf("container: create %s: %w", opts.Name, err)
	}
	return ContainerID(resp.ID), nil
}

func (r *DockerRuntime) Start(ctx context.Context, id ContainerID) error {
	if err := r.cli.ContainerStart(ctx, string(id), dockercontainer.StartOptions{}); err != nil {
		return fmt.Errorf("container: start %s: %w", id, err)
	}
	return nil
}

func (r *DockerRuntime) Stop(ctx context.Context, id ContainerID, timeout time.Duration) error {
	seconds := int(timeout.Seconds())
	if err := r.cli.ContainerStop(ctx, string(id), dockercontainer.StopOptions{Timeout: &seconds}); err != nil {
		return fmt.Errorf("container: stop %s: %w", id, err)
	}
	return nil
}

func (r *DockerRuntime) Remove(ctx context.Context, id ContainerID, force bool) error {
	err := r.cli.ContainerRemove(ctx, string(id), dockercontainer.RemoveOptions{Force: force, RemoveVolumes: false})
	if err != nil {
		return fmt.Errorf("container: remove %s: %w", id, err)
	}
	return nil
}

func (r *DockerRuntime) Inspect(ctx context.Context, id ContainerID) (ContainerInfo, error) {
	resp, err := r.cli.ContainerInspect(ctx, string(id))
	if err != nil {
		return ContainerInfo{}, fmt.Errorf("container: inspect %s: %w", id, err)
	}
	return fromInspectResponse(resp), nil
}

func (r *DockerRuntime) Stats(ctx context.Context, id ContainerID) (ContainerStats, error) {
	reader, err := r.cli.ContainerStatsOneShot(ctx, string(id))
	if err != nil {
		return ContainerStats{}, fmt.Errorf("container: stats %s: %w", id, err)
	}
	defer reader.Body.Close()

	stats, err := decodeStats(reader.Body)
	if err != nil {
		return ContainerStats{}, fmt.Errorf("container: decode stats for %s: %w", id, err)
	}
	return stats, nil
}

func (r *DockerRuntime) Logs(ctx context.Context, id ContainerID, opts LogOptions) (io.ReadCloser, error) {
	logOpts := dockercontainer.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Timestamps: opts.Timestamps,
		Follow:     opts.Follow,
		Tail:       opts.Tail,
	}
	if !opts.Since.IsZero() {
		logOpts.Since = opts.Since.Format(time.RFC3339Nano)
	}

	rc, err := r.cli.ContainerLogs(ctx, string(id), logOpts)
	if err != nil {
		return nil, fmt.Errorf("container: logs %s: %w", id, err)
	}
	return rc, nil
}

func (r *DockerRuntime) ListByLabel(ctx context.Context, key, value string) ([]ContainerInfo, error) {
	f := dockerfilters.NewArgs()
	f.Add("label", key+"="+value)

	list, err := r.cli.ContainerList(ctx, dockercontainer.ListOptions{All: true, Filters: f})
	if err != nil {
		return nil, fmt.Errorf("container: list by label %s=%s: %w", key, value, err)
	}

	out := make([]ContainerInfo, 0, len(list))
	for _, c := range list {
		out = append(out, fromSummary(c))
	}
	return out, nil
}

func (r *DockerRuntime) CreateNetwork(ctx context.Context, opts NetworkOptions) (NetworkID, error) {
	if err := ValidateCreateNetwork(opts); err != nil {
		return "", err
	}

	createOpts := dockernetwork.CreateOptions{
		Internal: opts.Internal,
		Labels:   opts.Labels,
	}
	if opts.Subnet != "" {
		createOpts.IPAM = &dockernetwork.IPAM{
			Config: []dockernetwork.IPAMConfig{{Subnet: opts.Subnet}},
		}
	}

	resp, err := r.cli.NetworkCreate(ctx, opts.Name, createOpts)
	if err != nil {
		return "", fmt.Errorf("container: create network %s: %w", opts.Name, err)
	}
	return NetworkID(resp.ID), nil
}

func (r *DockerRuntime) RemoveNetwork(ctx context.Context, id NetworkID) error {
	if err := r.cli.NetworkRemove(ctx, string(id)); err != nil {
		return fmt.Errorf("container: remove network %s: %w", id, err)
	}
	return nil
}

func (r *DockerRuntime) ListNetworksByLabel(ctx context.Context, key, value string) ([]NetworkInfo, error) {
	f := dockerfilters.NewArgs()
	f.Add("label", key+"="+value)

	list, err := r.cli.NetworkList(ctx, dockernetwork.ListOptions{Filters: f})
	if err != nil {
		return nil, fmt.Errorf("container: list networks by label %s=%s: %w", key, value, err)
	}

	out := make([]NetworkInfo, 0, len(list))
	for _, n := range list {
		out = append(out, NetworkInfo{
			ID:       NetworkID(n.ID),
			Name:     n.Name,
			Labels:   n.Labels,
			Internal: n.Internal,
		})
	}
	return out, nil
}

func (r *DockerRuntime) Close() error {
	if err := r.cli.Close(); err != nil {
		return fmt.Errorf("container: close runtime client: %w", err)
	}
	return nil
}

// toDockerCreateArgs translates our typed CreateOptions into the three
// argument structs the Docker SDK's ContainerCreate expects. It is a pure
// function so tests can assert on the translation without a socket.
//
// opts must have already passed ValidateCreate — this function does not
// re-check self-constraint, it only maps the (by-then-validated) fields that
// self-constraint permits: Mounts/Privileged/CapAdd are never read here
// because a validated CreateOptions never carries hostile values in them.
func toDockerCreateArgs(opts CreateOptions) (*dockercontainer.Config, *dockercontainer.HostConfig, *dockernetwork.NetworkingConfig) {
	cfg := &dockercontainer.Config{
		Image:  opts.Image,
		Env:    opts.Env,
		Cmd:    opts.Command,
		Labels: opts.Labels,
	}

	hostCfg := &dockercontainer.HostConfig{
		NetworkMode: dockercontainer.NetworkMode(opts.Network),
		Resources: dockercontainer.Resources{
			Memory:   opts.Resources.MemoryBytes,
			NanoCPUs: opts.Resources.NanoCPUs,
		},
	}
	if opts.Volume.Name != "" {
		hostCfg.Mounts = []dockermount.Mount{{
			Type:     dockermount.TypeVolume,
			Source:   opts.Volume.Name,
			Target:   opts.Volume.MountPath,
			ReadOnly: opts.Volume.ReadOnly,
		}}
	}

	netCfg := &dockernetwork.NetworkingConfig{
		EndpointsConfig: map[string]*dockernetwork.EndpointSettings{
			opts.Network: {},
		},
	}

	return cfg, hostCfg, netCfg
}

// fromInspectResponse maps a Docker SDK InspectResponse onto our typed
// ContainerInfo.
func fromInspectResponse(resp dockercontainer.InspectResponse) ContainerInfo {
	var info ContainerInfo

	// ContainerJSONBase is an embedded pointer; a response missing it (should
	// not happen against a real daemon, but this package must not panic on a
	// malformed response) leaves info at its zero value.
	if base := resp.ContainerJSONBase; base != nil {
		info.ID = ContainerID(base.ID)
		info.Name = trimLeadingSlash(base.Name)
		if base.State != nil {
			info.State = ContainerState(base.State.Status)
			if base.State.Health != nil {
				info.Health = string(base.State.Health.Status)
			}
		}
		if base.Created != "" {
			if t, err := time.Parse(time.RFC3339Nano, base.Created); err == nil {
				info.CreatedAt = t
			}
		}
	}
	if resp.Config != nil {
		info.Image = resp.Config.Image
		info.Labels = resp.Config.Labels
	}
	return info
}

// fromSummary maps one entry of a Docker SDK container-list response onto
// our typed ContainerInfo.
func fromSummary(c dockercontainer.Summary) ContainerInfo {
	name := ""
	if len(c.Names) > 0 {
		// Docker prefixes list names with "/"; trim it for parity with the
		// unprefixed name Inspect and Create return.
		name = trimLeadingSlash(c.Names[0])
	}
	return ContainerInfo{
		ID:        ContainerID(c.ID),
		Name:      name,
		Image:     c.Image,
		Labels:    c.Labels,
		State:     ContainerState(c.State),
		CreatedAt: time.Unix(c.Created, 0).UTC(),
	}
}

func trimLeadingSlash(s string) string {
	if len(s) > 0 && s[0] == '/' {
		return s[1:]
	}
	return s
}

// decodeStats reads a one-shot stats response body and computes the derived
// CPU percentage using the same delta formula the Docker CLI uses
// (cpu_stats vs precpu_stats), since the API reports cumulative counters, not
// a percentage.
func decodeStats(body io.Reader) (ContainerStats, error) {
	var resp dockercontainer.StatsResponse
	if err := json.NewDecoder(body).Decode(&resp); err != nil {
		return ContainerStats{}, fmt.Errorf("decode stats response: %w", err)
	}

	return ContainerStats{
		CPUPercent:       cpuPercent(resp),
		MemoryUsageBytes: resp.MemoryStats.Usage,
		MemoryLimitBytes: resp.MemoryStats.Limit,
	}, nil
}

// cpuPercent computes the CPU usage percentage from one stats sample using
// the delta between the current and previous reading, exactly as `docker
// stats` does. When online_cpus is unset (e.g. some Podman versions) it
// falls back to the length of the per-core usage slice.
func cpuPercent(resp dockercontainer.StatsResponse) float64 {
	cpuDelta := float64(resp.CPUStats.CPUUsage.TotalUsage) - float64(resp.PreCPUStats.CPUUsage.TotalUsage)
	systemDelta := float64(resp.CPUStats.SystemUsage) - float64(resp.PreCPUStats.SystemUsage)
	if systemDelta <= 0 || cpuDelta <= 0 {
		return 0
	}

	onlineCPUs := float64(resp.CPUStats.OnlineCPUs)
	if onlineCPUs == 0 {
		onlineCPUs = float64(len(resp.CPUStats.CPUUsage.PercpuUsage))
	}
	if onlineCPUs == 0 {
		onlineCPUs = 1
	}

	return (cpuDelta / systemDelta) * onlineCPUs * 100.0
}
