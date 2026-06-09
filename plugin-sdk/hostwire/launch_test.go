package hostwire_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"

	handshakev1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/handshake/v1"
	toolv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/tool/v1"
	triggerv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/trigger/v1"
	"github.com/felag-engineering/gleipnir/plugin-sdk/hostwire"
	"github.com/felag-engineering/gleipnir/plugin-sdk/internal/fakehost"
)

// TestLaunch_BootstrapAndNegotiate builds the runfixture binary and runs a
// full Launch → Bootstrap.Bind → Handshake.Negotiate round trip. This verifies
// that the hostwire wiring matches the plugin's expectations end-to-end.
//
// Skipped unless GOOS == linux and `go` is on PATH.
func TestLaunch_BootstrapAndNegotiate(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("launch_test requires linux")
	}
	goPath, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go binary not on PATH; skipping launch test")
	}

	// Build the runfixture binary.
	dir := t.TempDir()
	fixtureBin := filepath.Join(dir, "runfixture")
	fixturePkg := "github.com/felag-engineering/gleipnir/plugin-sdk/cmd/gleipnir-plugin/cmd/internal/runfixture"

	// Locate the plugin-sdk module root by walking up from this file.
	sdkRoot := findSDKRoot(t)

	buildCmd := exec.Command(goPath, "build", "-tags", "runfixture", "-o", fixtureBin, fixturePkg)
	buildCmd.Dir = sdkRoot
	buildOut, buildErr := buildCmd.CombinedOutput()
	if buildErr != nil {
		t.Fatalf("build runfixture: %v\n%s", buildErr, buildOut)
	}

	host := fakehost.New(fakehost.Options{})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client, teardown, err := hostwire.Launch(ctx, fixtureBin, host, hostwire.Options{
		Stderr: os.Stderr,
	})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	defer teardown()

	// Negotiate should succeed and return TOOL + TRIGGER capabilities.
	resp, err := client.Handshake.Negotiate(ctx, &handshakev1.NegotiateRequest{
		HostVersion: "0.0.0-test",
	})
	if err != nil {
		t.Fatalf("Negotiate: %v", err)
	}
	if !resp.GetOk() {
		t.Fatalf("Negotiate returned ok=false: %s", resp.GetErrorDetail())
	}

	caps := resp.GetActualCapabilities()
	hasTool, hasTrigger := false, false
	for _, c := range caps {
		switch c {
		case handshakev1.ServiceCapability_SERVICE_CAPABILITY_TOOL:
			hasTool = true
		case handshakev1.ServiceCapability_SERVICE_CAPABILITY_TRIGGER:
			hasTrigger = true
		}
	}
	if !hasTool {
		t.Error("expected TOOL capability in Negotiate response")
	}
	if !hasTrigger {
		t.Error("expected TRIGGER capability in Negotiate response")
	}
}

// TestLaunch_ServerInterceptorsAreInvokedInOrder verifies that interceptors
// passed via Options.ServerInterceptors are wired into the broker gRPC server
// and invoked in slice order when the plugin makes a Host RPC.
//
// The runfixture's Trigger.Start implementation dials HostService and calls
// EmitEvent — that Host RPC is what exercises the interceptor chain.
//
// Skipped unless GOOS == linux and `go` is on PATH (same guard as the parent test).
func TestLaunch_ServerInterceptorsAreInvokedInOrder(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("launch_test requires linux")
	}
	goPath, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go binary not on PATH; skipping launch test")
	}

	// Build the runfixture binary (same as TestLaunch_BootstrapAndNegotiate).
	dir := t.TempDir()
	fixtureBin := filepath.Join(dir, "runfixture")
	fixturePkg := "github.com/felag-engineering/gleipnir/plugin-sdk/cmd/gleipnir-plugin/cmd/internal/runfixture"

	sdkRoot := findSDKRoot(t)

	buildCmd := exec.Command(goPath, "build", "-tags", "runfixture", "-o", fixtureBin, fixturePkg)
	buildCmd.Dir = sdkRoot
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("build runfixture: %v\n%s", err, out)
	}

	// Two interceptors that record the order in which they run.
	var mu sync.Mutex
	var order []int

	makeInterceptor := func(n int) grpc.UnaryServerInterceptor {
		return func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
			mu.Lock()
			order = append(order, n)
			mu.Unlock()
			return handler(ctx, req)
		}
	}

	host := fakehost.New(fakehost.Options{})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client, teardown, err := hostwire.Launch(ctx, fixtureBin, host, hostwire.Options{
		Stderr: os.Stderr,
		ServerInterceptors: []grpc.UnaryServerInterceptor{
			makeInterceptor(1),
			makeInterceptor(2),
		},
	})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	defer teardown()

	// Negotiate first to confirm the plugin is live.
	resp, err := client.Handshake.Negotiate(ctx, &handshakev1.NegotiateRequest{
		HostVersion: "0.0.0-test",
	})
	if err != nil {
		t.Fatalf("Negotiate: %v", err)
	}
	if !resp.GetOk() {
		t.Fatalf("Negotiate returned ok=false: %s", resp.GetErrorDetail())
	}

	// Trigger.Start causes the plugin to call EmitEvent on the host, which goes
	// through the broker gRPC server where our interceptors are installed.
	stream, err := client.Trigger.Start(ctx, &triggerv1.StartRequest{})
	if err != nil {
		t.Fatalf("Trigger.Start: %v", err)
	}
	// Drain the stream.
	for {
		if _, err := stream.Recv(); err != nil {
			break
		}
	}

	mu.Lock()
	gotOrder := append([]int(nil), order...)
	mu.Unlock()

	if len(gotOrder) == 0 {
		t.Fatal("no interceptors were invoked; expected at least one Host RPC from the fixture")
	}
	// The first observed RPC must see interceptors in slice order (1 before 2).
	if len(gotOrder) >= 2 {
		// Find first occurrence of each interceptor for a single RPC call.
		// For a single call: order should start with 1, 2, ...
		if gotOrder[0] != 1 || gotOrder[1] != 2 {
			t.Errorf("interceptors invoked in wrong order: got %v, want [1, 2, ...]", gotOrder)
		}
	} else {
		if gotOrder[0] != 1 {
			t.Errorf("first interceptor should be 1, got %d", gotOrder[0])
		}
	}
}

// TestLaunch_SubprocessEnvDoesNotLeakSecrets is a regression test for the
// vulnerability where os.Environ() was passed wholesale to plugin subprocesses,
// leaking GLEIPNIR_ENCRYPTION_KEY and other host secrets.
//
// The test injects a sentinel encryption key into the test process's environment,
// launches the runfixture plugin, calls its env_dump tool (which returns
// os.Environ() from inside the subprocess), and asserts the key is absent.
//
// Skipped unless GOOS == linux and `go` is on PATH.
func TestLaunch_SubprocessEnvDoesNotLeakSecrets(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("launch_test requires linux")
	}
	goPath, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go binary not on PATH; skipping launch test")
	}

	// Inject secrets into the host process environment. safeEnv must filter
	// these out so the subprocess never sees them.
	t.Setenv("GLEIPNIR_ENCRYPTION_KEY", "deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef")
	t.Setenv("GLEIPNIR_DB_PATH", "/data/secret.db")

	dir := t.TempDir()
	fixtureBin := filepath.Join(dir, "runfixture")
	fixturePkg := "github.com/felag-engineering/gleipnir/plugin-sdk/cmd/gleipnir-plugin/cmd/internal/runfixture"

	sdkRoot := findSDKRoot(t)

	buildCmd := exec.Command(goPath, "build", "-tags", "runfixture", "-o", fixtureBin, fixturePkg)
	buildCmd.Dir = sdkRoot
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("build runfixture: %v\n%s", err, out)
	}

	host := fakehost.New(fakehost.Options{})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client, teardown, err := hostwire.Launch(ctx, fixtureBin, host, hostwire.Options{
		Stderr: os.Stderr,
	})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	defer teardown()

	// Call the env_dump tool, which returns the subprocess's os.Environ() as a
	// JSON array of KEY=VALUE strings.
	resp, err := client.Tool.Call(ctx, &toolv1.CallRequest{
		ToolName:  "env_dump",
		InputJson: "{}",
	})
	if err != nil {
		t.Fatalf("Tool.Call(env_dump): %v", err)
	}

	var subEnv []string
	if err := json.Unmarshal([]byte(resp.GetOutputJson()), &subEnv); err != nil {
		t.Fatalf("parse env_dump output: %v", err)
	}

	// Assert that no host secrets leaked into the subprocess environment.
	leakChecks := []string{
		"GLEIPNIR_ENCRYPTION_KEY",
		"GLEIPNIR_DB_PATH",
	}
	for _, entry := range subEnv {
		for _, secret := range leakChecks {
			if strings.HasPrefix(entry, secret+"=") {
				t.Errorf("subprocess received secret env var %s — host encryption key leaked to plugin subprocess", secret)
			}
		}
	}

	// Assert that fundamental system vars were passed through (PATH at minimum).
	hasPath := false
	for _, entry := range subEnv {
		if strings.HasPrefix(entry, "PATH=") {
			hasPath = true
		}
	}
	if !hasPath {
		t.Error("subprocess did not receive PATH; safeEnv allowlist too restrictive")
	}
}

// findSDKRoot walks up from the test binary's directory to find the plugin-sdk
// module root (a directory containing go.mod with the plugin-sdk module path).
func findSDKRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		modPath := filepath.Join(dir, "go.mod")
		if data, err := os.ReadFile(modPath); err == nil {
			// Check it's the plugin-sdk go.mod, not the root one.
			if strings.Contains(string(data), "module github.com/felag-engineering/gleipnir/plugin-sdk") {
				return dir
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("could not find plugin-sdk root from %s", dir)
		}
		dir = parent
	}
}
