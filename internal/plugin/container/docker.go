package container

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/netip"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	dockercontainer "github.com/moby/moby/api/types/container"
	dockermount "github.com/moby/moby/api/types/mount"
	dockernetwork "github.com/moby/moby/api/types/network"
	dockerclient "github.com/moby/moby/client"
)

// DockerRuntime is the production Runtime backed by Moby's standalone client
// module. It talks to whatever socket DetectPosture resolved — Podman's
// Docker-compatible socket needs no special-casing anywhere in this file, by
// design; the wire protocol is what the client understands regardless of
// which daemon speaks it.
//
// This deliberately depends on github.com/moby/moby/client rather than
// github.com/docker/docker: the latter bundles daemon (dockerd) code into
// the same +incompatible module as the client, so daemon-side CVEs (which a
// client-only consumer like Gleipnir can never be affected by) fail
// govulncheck with no fixed version to upgrade to. Moby split the client out
// into its own module precisely so API consumers aren't dragged into that.
type DockerRuntime struct {
	cli *dockerclient.Client
}

// NewDockerRuntime dials the container-runtime socket at socketPath. Dialing
// a Unix socket does not itself require the daemon to be reachable yet — the
// client's construction only prepares the HTTP transport; the first real
// call surfaces a connection error if the socket is unresponsive. API-version
// negotiation is on by default in this client (unlike the old Docker SDK,
// which needed WithAPIVersionNegotiation() to opt in).
func NewDockerRuntime(socketPath string) (*DockerRuntime, error) {
	cli, err := dockerclient.New(
		dockerclient.WithHost("unix://" + socketPath),
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
	resp, err := r.cli.ContainerCreate(ctx, dockerclient.ContainerCreateOptions{
		Config:           cfg,
		HostConfig:       hostCfg,
		NetworkingConfig: netCfg,
		Name:             opts.Name,
	})
	if err != nil {
		return "", fmt.Errorf("container: create %s: %w", opts.Name, err)
	}
	return ContainerID(resp.ID), nil
}

func (r *DockerRuntime) Start(ctx context.Context, id ContainerID) error {
	if _, err := r.cli.ContainerStart(ctx, string(id), dockerclient.ContainerStartOptions{}); err != nil {
		return fmt.Errorf("container: start %s: %w", id, err)
	}
	return nil
}

func (r *DockerRuntime) Stop(ctx context.Context, id ContainerID, timeout time.Duration) error {
	seconds := int(timeout.Seconds())
	if _, err := r.cli.ContainerStop(ctx, string(id), dockerclient.ContainerStopOptions{Timeout: &seconds}); err != nil {
		return fmt.Errorf("container: stop %s: %w", id, err)
	}
	return nil
}

func (r *DockerRuntime) Remove(ctx context.Context, id ContainerID, force bool) error {
	_, err := r.cli.ContainerRemove(ctx, string(id), dockerclient.ContainerRemoveOptions{Force: force, RemoveVolumes: false})
	if err != nil {
		return fmt.Errorf("container: remove %s: %w", id, err)
	}
	return nil
}

func (r *DockerRuntime) Inspect(ctx context.Context, id ContainerID) (ContainerInfo, error) {
	resp, err := r.cli.ContainerInspect(ctx, string(id), dockerclient.ContainerInspectOptions{})
	if err != nil {
		return ContainerInfo{}, fmt.Errorf("container: inspect %s: %w", id, err)
	}
	return fromInspectResponse(resp.Container), nil
}

func (r *DockerRuntime) Stats(ctx context.Context, id ContainerID) (ContainerStats, error) {
	// Zero-value ContainerStatsOptions (Stream=false, IncludePreviousSample=false)
	// is the one-shot query the old SDK's ContainerStatsOneShot sent.
	result, err := r.cli.ContainerStats(ctx, string(id), dockerclient.ContainerStatsOptions{})
	if err != nil {
		return ContainerStats{}, fmt.Errorf("container: stats %s: %w", id, err)
	}
	defer result.Body.Close()

	stats, err := decodeStats(result.Body)
	if err != nil {
		return ContainerStats{}, fmt.Errorf("container: decode stats for %s: %w", id, err)
	}
	return stats, nil
}

func (r *DockerRuntime) Logs(ctx context.Context, id ContainerID, opts LogOptions) (io.ReadCloser, error) {
	logOpts := dockerclient.ContainerLogsOptions{
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
	f := make(dockerclient.Filters)
	f.Add("label", key+"="+value)

	result, err := r.cli.ContainerList(ctx, dockerclient.ContainerListOptions{All: true, Filters: f})
	if err != nil {
		return nil, fmt.Errorf("container: list by label %s=%s: %w", key, value, err)
	}

	out := make([]ContainerInfo, 0, len(result.Items))
	for _, c := range result.Items {
		out = append(out, fromSummary(c))
	}
	return out, nil
}

func (r *DockerRuntime) CreateNetwork(ctx context.Context, opts NetworkOptions) (NetworkID, error) {
	if err := ValidateCreateNetwork(opts); err != nil {
		return "", err
	}

	createOpts := dockerclient.NetworkCreateOptions{
		Internal: opts.Internal,
		Labels:   opts.Labels,
	}
	if opts.Subnet != "" {
		subnet, err := netip.ParsePrefix(opts.Subnet)
		if err != nil {
			return "", fmt.Errorf("container: create network %s: parse subnet %q: %w", opts.Name, opts.Subnet, err)
		}
		createOpts.IPAM = &dockernetwork.IPAM{
			Config: []dockernetwork.IPAMConfig{{Subnet: subnet}},
		}
	}

	resp, err := r.cli.NetworkCreate(ctx, opts.Name, createOpts)
	if err != nil {
		return "", fmt.Errorf("container: create network %s: %w", opts.Name, err)
	}
	return NetworkID(resp.ID), nil
}

func (r *DockerRuntime) RemoveNetwork(ctx context.Context, id NetworkID) error {
	if _, err := r.cli.NetworkRemove(ctx, string(id), dockerclient.NetworkRemoveOptions{}); err != nil {
		return fmt.Errorf("container: remove network %s: %w", id, err)
	}
	return nil
}

func (r *DockerRuntime) ListNetworksByLabel(ctx context.Context, key, value string) ([]NetworkInfo, error) {
	f := make(dockerclient.Filters)
	f.Add("label", key+"="+value)

	result, err := r.cli.NetworkList(ctx, dockerclient.NetworkListOptions{Filters: f})
	if err != nil {
		return nil, fmt.Errorf("container: list networks by label %s=%s: %w", key, value, err)
	}

	out := make([]NetworkInfo, 0, len(result.Items))
	for _, n := range result.Items {
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

// toDockerCreateArgs translates our typed CreateOptions into the argument
// structs the client's ContainerCreate expects. It is a pure function so
// tests can assert on the translation without a socket.
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

// fromInspectResponse maps a container-inspect response onto our typed
// ContainerInfo. Unlike the old Docker SDK, moby/moby/client's
// InspectResponse carries ID/Name/State/Created directly (no nested
// ContainerJSONBase wrapper to nil-check) — a zero-value response still
// yields a zero-value ContainerInfo without any special-casing.
func fromInspectResponse(resp dockercontainer.InspectResponse) ContainerInfo {
	var info ContainerInfo

	info.ID = ContainerID(resp.ID)
	info.Name = trimLeadingSlash(resp.Name)
	if resp.State != nil {
		info.State = ContainerState(resp.State.Status)
		info.OOMKilled = resp.State.OOMKilled
		info.ExitCode = resp.State.ExitCode
		if resp.State.Health != nil {
			info.Health = string(resp.State.Health.Status)
		}
	}
	if resp.Created != "" {
		if t, err := time.Parse(time.RFC3339Nano, resp.Created); err == nil {
			info.CreatedAt = t
		}
	}
	if resp.Config != nil {
		info.Image = resp.Config.Image
		info.Labels = resp.Config.Labels
	}
	return info
}

// fromSummary maps one entry of a container-list response onto our typed
// ContainerInfo.
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

func (r *DockerRuntime) ImageLoad(ctx context.Context, archive io.Reader) error {
	resp, err := r.cli.ImageLoad(ctx, archive)
	if err != nil {
		return fmt.Errorf("container: load image archive: %w", err)
	}
	defer resp.Close()

	// The daemon performs the load WHILE streaming progress. Returning before
	// the stream is exhausted would report success on a load still in flight,
	// and the caller's very next act is to inspect for the image it expects.
	// The content is discarded on purpose — see ImageRuntime.ImageLoad.
	if _, err := io.Copy(io.Discard, resp); err != nil {
		return fmt.Errorf("container: drain image load stream: %w", err)
	}
	return nil
}

func (r *DockerRuntime) ImageInspect(ctx context.Context, ref string) (ImageInfo, error) {
	res, err := r.cli.ImageInspect(ctx, ref)
	if err != nil {
		if cerrdefs.IsNotFound(err) {
			return ImageInfo{}, fmt.Errorf("%w: %s", ErrImageNotFound, ref)
		}
		return ImageInfo{}, fmt.Errorf("container: inspect image %s: %w", ref, err)
	}
	return ImageInfo{
		ID:          res.ID,
		RepoTags:    res.RepoTags,
		RepoDigests: res.RepoDigests,
		SizeBytes:   res.Size,
	}, nil
}
