//go:build unix

package serve_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	channelv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/channel/v1"
	hostv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/host/v1"
	toolv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/tool/v1"
	"github.com/felag-engineering/gleipnir/plugin-sdk/hostwire"
	"github.com/felag-engineering/gleipnir/plugin-sdk/manifest"
	sdkproto "github.com/felag-engineering/gleipnir/plugin-sdk/proto"
	"github.com/felag-engineering/gleipnir/plugin-sdk/serve"
)

// TestMain intercepts the test binary when run with GLEIPNIR_SERVE_TEST_FIXTURE
// set so it acts as a plugin subprocess instead of running the test suite. This
// is the standard Go re-exec pattern for subprocess tests.
func TestMain(m *testing.M) {
	switch os.Getenv("GLEIPNIR_SERVE_TEST_FIXTURE") {
	case "channel-only":
		// Registers a ChannelService that returns Ok=true on every Notify.
		// Does NOT call the host, so no token verification happens here.
		serve.Serve(
			serve.WithChannelService(func(_ hostv1.HostServiceClient) channelv1.ChannelServiceServer {
				return &notifyOkService{}
			}),
		)
		os.Exit(0)

	case "channel-calls-host":
		// Registers a ChannelService that calls host.GetInstanceConfig on every
		// Notify, so the host server records incoming metadata including the token.
		serve.Serve(
			serve.WithManifest(fixtureManifest),
			serve.WithChannelService(func(host hostv1.HostServiceClient) channelv1.ChannelServiceServer {
				return &notifyWithHostCallService{host: host}
			}),
		)
		os.Exit(0)

	case "channel-pid":
		// Writes "pid=<N>\n" to stderr so the test can read the PID and send
		// SIGTERM directly to the subprocess, then serves a basic ChannelService.
		fmt.Fprintf(os.Stderr, "pid=%d\n", os.Getpid())
		serve.Serve(
			serve.WithChannelService(func(_ hostv1.HostServiceClient) channelv1.ChannelServiceServer {
				return &notifyOkService{}
			}),
		)
		os.Exit(0)

	case "emit-manifest":
		// serve.Serve detects --emit-manifest in os.Args and writes JSON to stdout.
		// This mode is only reached when --emit-manifest is NOT the first arg;
		// TestEmitManifest passes --emit-manifest explicitly to the subprocess.
		serve.Serve(
			serve.WithManifest(fixtureManifest),
		)
		os.Exit(0)
	}

	os.Exit(m.Run())
}

// ── fixture manifest ─────────────────────────────────────────────────────────

var fixtureManifest = manifest.Manifest{
	SchemaVersion: "v1",
	Name:          "serve-test-fixture",
	Version:       "1.2.3",
	Services:      manifest.Services{Channel: "v1"},
	Auth:          manifest.AuthDecl{Mode: "instance_credentials", Strategy: "none"},
}

// ── plugin-side service stubs ─────────────────────────────────────────────────

// notifyOkService returns Ok=true for every Notify call.
type notifyOkService struct {
	channelv1.UnimplementedChannelServiceServer
}

func (s *notifyOkService) Notify(_ context.Context, _ *channelv1.NotifyRequest) (*channelv1.NotifyResponse, error) {
	return &channelv1.NotifyResponse{Ok: true}, nil
}

// notifyWithHostCallService calls host.GetInstanceConfig on every Notify so the
// token interceptor fires and the host server can record the metadata.
type notifyWithHostCallService struct {
	channelv1.UnimplementedChannelServiceServer
	host hostv1.HostServiceClient
}

func (s *notifyWithHostCallService) Notify(ctx context.Context, _ *channelv1.NotifyRequest) (*channelv1.NotifyResponse, error) {
	// WithCallContext propagates the host-injected call ID on the outgoing RPC,
	// which is the normal plugin practice per spec §8.5.
	hostCtx := serve.WithCallContext(ctx)
	_, _ = s.host.GetInstanceConfig(hostCtx, &hostv1.GetInstanceConfigRequest{})
	return &channelv1.NotifyResponse{Ok: true}, nil
}

// ── host-side server ─────────────────────────────────────────────────────────

// recordingHostServer implements hostwire.HostServer and records the incoming
// metadata from every GetInstanceConfig call so tests can inspect the token.
type recordingHostServer struct {
	hostv1.UnimplementedHostServiceServer

	mu       sync.Mutex
	received []metadata.MD
}

func (h *recordingHostServer) Register(s *grpc.Server) {
	hostv1.RegisterHostServiceServer(s, h)
}

func (h *recordingHostServer) GetInstanceConfig(ctx context.Context, _ *hostv1.GetInstanceConfigRequest) (*hostv1.GetInstanceConfigResponse, error) {
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		h.mu.Lock()
		h.received = append(h.received, md.Copy())
		h.mu.Unlock()
	}
	return &hostv1.GetInstanceConfigResponse{ConfigJson: `{}`}, nil
}

func (h *recordingHostServer) incomingMDs() []metadata.MD {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]metadata.MD, len(h.received))
	copy(out, h.received)
	return out
}

// noopHostServer registers no services. Used when the test does not need the
// plugin to call back.
type noopHostServer struct{}

func (noopHostServer) Register(_ *grpc.Server) {}

// ── launch helper ─────────────────────────────────────────────────────────────

// launchFixture re-execs the test binary in the given fixture mode using
// hostwire.Launch. token, if non-empty, is set as GLEIPNIR_INSTANCE_TOKEN so
// the SDK's TokenInterceptorFromEnv picks it up.
func launchFixture(t *testing.T, fixtureMode string, host hostwire.HostServer, token string) (*hostwire.Client, func(), error) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	env := []string{"GLEIPNIR_SERVE_TEST_FIXTURE=" + fixtureMode}
	if token != "" {
		env = append(env, serve.InstanceTokenEnvVar+"="+token)
	}

	if host == nil {
		host = noopHostServer{}
	}

	return hostwire.Launch(ctx, os.Args[0], host, hostwire.Options{
		StartupTimeout: 20 * time.Second,
		Env:            env,
	})
}

// ── tests ─────────────────────────────────────────────────────────────────────

// TestServe_RegistersChannelService verifies that Serve registers the user's
// ChannelService factory and that Notify returns Ok=true.
func TestServe_RegistersChannelService(t *testing.T) {
	t.Parallel()

	client, teardown, err := launchFixture(t, "channel-only", nil, "")
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	defer teardown()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	resp, err := client.Channel.Notify(ctx, &channelv1.NotifyRequest{})
	if err != nil {
		t.Fatalf("Notify RPC: %v", err)
	}
	if !resp.GetOk() {
		t.Errorf("Notify: want Ok=true, got Ok=false; error=%v", resp.GetError())
	}
}

// TestServe_TokenInterceptorAttached verifies that the SDK wires
// TokenInterceptorFromEnv via broker.DialWithOptions (not the bare broker.Dial
// which silently drops interceptors). Without DialWithOptions the instance token
// set in GLEIPNIR_INSTANCE_TOKEN never reaches the host-side server.
func TestServe_TokenInterceptorAttached(t *testing.T) {
	t.Parallel()

	const testToken = "test-token-interceptor-abc123"

	recorder := &recordingHostServer{}
	client, teardown, err := launchFixture(t, "channel-calls-host", recorder, testToken)
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	defer teardown()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Notify triggers host.GetInstanceConfig inside the fixture service, which
	// is where the outgoing metadata (containing the instance token) is recorded.
	resp, err := client.Channel.Notify(ctx, &channelv1.NotifyRequest{})
	if err != nil {
		t.Fatalf("Notify RPC: %v", err)
	}
	if !resp.GetOk() {
		t.Errorf("Notify: want Ok=true, got Ok=false")
	}

	mds := recorder.incomingMDs()
	if len(mds) == 0 {
		t.Fatal("host server received no GetInstanceConfig calls — fixture did not call back to host")
	}

	// At least one GetInstanceConfig must have carried the instance token.
	found := false
	for _, md := range mds {
		if vals := md.Get(sdkproto.InstanceTokenMetadataKey); len(vals) > 0 && vals[0] == testToken {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("no incoming GetInstanceConfig call carried token %q; metadata seen: %v", testToken, mds)
	}
}

// TestServe_NoCapabilityWithoutFactory verifies that calling a service whose
// factory was not registered returns codes.Unavailable. The "channel-only"
// fixture registers only ChannelService; calling ToolService.Call must fail.
func TestServe_NoCapabilityWithoutFactory(t *testing.T) {
	t.Parallel()

	client, teardown, err := launchFixture(t, "channel-only", nil, "")
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	defer teardown()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err = client.Tool.Call(ctx, &toolv1.CallRequest{})
	if err == nil {
		t.Fatal("Tool.Call with no ToolService registered: expected gRPC error, got nil")
	}
	s, ok := status.FromError(err)
	if !ok {
		t.Fatalf("Tool.Call error is not a gRPC status: %v", err)
	}
	if s.Code() != codes.Unavailable {
		t.Errorf("Tool.Call: want codes.Unavailable, got %s (%q)", s.Code(), s.Message())
	}
}

// TestServe_SigtermDrains verifies that serve.Serve exits cleanly when the
// subprocess receives SIGTERM. The fixture writes its PID to stderr before
// calling Serve; the test reads that PID and sends SIGTERM directly via
// syscall.Kill. This ensures the test actually exercises the SIGTERM handler
// in serve.Serve — if the handler were deleted, the OS default for SIGTERM
// would still kill the process, so the bounded-exit assertion alone is
// insufficient. What we care about here is that the handler path is reached,
// not that it performs any particular cleanup action.
func TestServe_SigtermDrains(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Pipe subprocess stderr so we can scan for the "pid=<N>" line that the
	// "channel-pid" fixture emits before calling serve.Serve. io.Pipe is safe
	// for concurrent reads and writes (go-plugin writes from its copy goroutine
	// while we scan the read end here).
	stderrR, stderrW := io.Pipe()
	pidReady := make(chan int, 1)
	go func() {
		scanner := bufio.NewScanner(stderrR)
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "pid=") {
				pid, err := strconv.Atoi(strings.TrimPrefix(line, "pid="))
				if err == nil {
					select {
					case pidReady <- pid:
					default:
					}
				}
			}
		}
	}()

	exited := make(chan struct{})

	client, teardown, err := hostwire.Launch(ctx, os.Args[0], noopHostServer{}, hostwire.Options{
		StartupTimeout: 20 * time.Second,
		Env:            []string{"GLEIPNIR_SERVE_TEST_FIXTURE=channel-pid"},
		Stderr:         stderrW,
		OnProcessExited: func() {
			stderrW.Close() // unblocks the scanner goroutine so it exits cleanly
			close(exited)
		},
	})
	if err != nil {
		stderrR.Close()
		stderrW.Close()
		t.Fatalf("launch: %v", err)
	}
	_ = client
	defer teardown() // ensures cleanup even if the test fails before SIGTERM

	// Wait for the subprocess to emit its PID line.
	var pid int
	select {
	case pid = <-pidReady:
	case <-time.After(10 * time.Second):
		t.Fatal("subprocess did not write pid= line within 10s")
	}

	start := time.Now()
	// Send SIGTERM directly so the serve.Serve signal handler fires, not
	// hostwire's Kill() path (which closes the broker connection instead).
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		t.Fatalf("Kill(SIGTERM): %v", err)
	}

	select {
	case <-exited:
		t.Logf("subprocess exited within %s after SIGTERM", time.Since(start))
	case <-time.After(10 * time.Second):
		t.Fatal("subprocess did not exit within 10s after SIGTERM")
	}
}

// TestEmitManifest verifies that running the binary with --emit-manifest as
// the first argument writes manifest JSON to stdout that round-trips correctly.
func TestEmitManifest(t *testing.T) {
	t.Parallel()

	// Re-exec this test binary with --emit-manifest. TestMain's "emit-manifest"
	// fixture mode calls serve.Serve(WithManifest(fixtureManifest)); serve.Serve
	// detects os.Args[1]=="--emit-manifest" and calls EmitManifest before the
	// plugin server starts.
	cmd := exec.Command(os.Args[0], "--emit-manifest")
	cmd.Env = append(os.Environ(), "GLEIPNIR_SERVE_TEST_FIXTURE=emit-manifest")

	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("run with --emit-manifest: %v\noutput: %s", err, out)
	}

	var got manifest.Manifest
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unmarshal manifest JSON: %v\nraw output: %s", err, out)
	}

	if got.Name != fixtureManifest.Name {
		t.Errorf("Name: want %q, got %q", fixtureManifest.Name, got.Name)
	}
	if got.Version != fixtureManifest.Version {
		t.Errorf("Version: want %q, got %q", fixtureManifest.Version, got.Version)
	}
	if got.Services.Channel != fixtureManifest.Services.Channel {
		t.Errorf("Services.Channel: want %q, got %q", fixtureManifest.Services.Channel, got.Services.Channel)
	}
}
