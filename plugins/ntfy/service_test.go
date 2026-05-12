package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	channelv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/channel/v1"
	hostv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/host/v1"
	plugintest "github.com/felag-engineering/gleipnir/plugin-sdk/testing"
)

// setup starts in-process gRPC servers for both the fake host and the channel
// service. ntfyBackend is an optional httptest.Server used as the fake ntfy
// endpoint; pass nil to create a ChannelService with no backend configured.
// Returns the channel client, the fake host, and a cleanup function.
func setup(t *testing.T, ntfyBackend *httptest.Server, hostOpts ...plugintest.Option) (channelv1.ChannelServiceClient, *plugintest.FakeHost, func()) {
	t.Helper()

	host := plugintest.NewFakeHost(hostOpts...)

	// Start host gRPC server.
	hostLis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for host: %v", err)
	}
	hostSrv := grpc.NewServer()
	host.Register(hostSrv)
	go func() { _ = hostSrv.Serve(hostLis) }()

	// Dial the host and build the hostv1 client used by ChannelService.
	hostConn, err := grpc.NewClient(hostLis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial host: %v", err)
	}
	hostClient := hostv1.NewHostServiceClient(hostConn)

	// Build the channel service, using the test HTTP client so requests go to
	// the httptest.Server instead of the real internet.
	var httpClient *http.Client
	if ntfyBackend != nil {
		httpClient = ntfyBackend.Client()
	}
	svc := NewChannelService(hostClient, httpClient)

	// Start channel service gRPC server.
	chanLis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for channel: %v", err)
	}
	chanSrv := grpc.NewServer()
	channelv1.RegisterChannelServiceServer(chanSrv, svc)
	go func() { _ = chanSrv.Serve(chanLis) }()

	// Dial the channel service.
	chanConn, err := grpc.NewClient(chanLis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial channel: %v", err)
	}
	chanClient := channelv1.NewChannelServiceClient(chanConn)

	cleanup := func() {
		chanConn.Close()
		chanSrv.Stop()
		hostConn.Close()
		hostSrv.Stop()
	}
	return chanClient, host, cleanup
}

// TestNotify_Happy verifies a successful notification: correct URL path, Title
// header, Authorization header, and body forwarded to the ntfy server.
func TestNotify_Happy(t *testing.T) {
	var gotPath, gotTitle, gotAuth, gotBody string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotTitle = r.Header.Get("Title")
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	chanClient, _, cleanup := setup(t, backend,
		plugintest.WithInstanceConfigJSON(`{"server_url":"`+backend.URL+`","default_topic":"alerts"}`),
		plugintest.WithCredentialsJSON(`{"api_key":"k-test"}`),
	)
	defer cleanup()

	resp, err := chanClient.Notify(context.Background(), &channelv1.NotifyRequest{
		PayloadJson: `{"title":"Alert","body":"something happened"}`,
	})
	if err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if !resp.GetOk() {
		t.Fatalf("expected ok=true, got error: %v", resp.GetError().GetMessage())
	}

	if gotPath != "/alerts" {
		t.Errorf("path: want /alerts, got %q", gotPath)
	}
	if gotTitle != "Alert" {
		t.Errorf("Title header: want \"Alert\", got %q", gotTitle)
	}
	if gotAuth != "Bearer k-test" {
		t.Errorf("Authorization header: want \"Bearer k-test\", got %q", gotAuth)
	}
	if gotBody != "something happened" {
		t.Errorf("body: want \"something happened\", got %q", gotBody)
	}
}

// TestNotify_PerAudienceTopicOverride verifies that channel_config_json topic
// overrides the default_topic from instance config.
func TestNotify_PerAudienceTopicOverride(t *testing.T) {
	var gotPath string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	chanClient, _, cleanup := setup(t, backend,
		plugintest.WithInstanceConfigJSON(`{"server_url":"`+backend.URL+`","default_topic":"alerts"}`),
		plugintest.WithCredentialsJSON(`{}`),
	)
	defer cleanup()

	resp, err := chanClient.Notify(context.Background(), &channelv1.NotifyRequest{
		ChannelConfigJson: `{"topic":"oncall"}`,
		PayloadJson:       `{"body":"paging oncall"}`,
	})
	if err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if !resp.GetOk() {
		t.Fatalf("expected ok=true, got error: %v", resp.GetError().GetMessage())
	}
	if gotPath != "/oncall" {
		t.Errorf("path: want /oncall, got %q", gotPath)
	}
}

// TestNotify_NoTopicConfigured verifies that missing topic on both instance
// config and channel config returns an INVALID_ARG error without making any
// HTTP request.
func TestNotify_NoTopicConfigured(t *testing.T) {
	var requestCount int
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	chanClient, _, cleanup := setup(t, backend,
		// No default_topic in instance config; no topic in channel config.
		plugintest.WithInstanceConfigJSON(`{"server_url":"`+backend.URL+`"}`),
		plugintest.WithCredentialsJSON(`{}`),
	)
	defer cleanup()

	resp, err := chanClient.Notify(context.Background(), &channelv1.NotifyRequest{
		PayloadJson: `{"body":"test"}`,
	})
	if err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if resp.GetOk() {
		t.Fatal("expected ok=false for missing topic, got ok=true")
	}
	if resp.GetError() == nil {
		t.Fatal("expected error envelope, got nil")
	}
	if requestCount != 0 {
		t.Errorf("expected 0 HTTP requests, got %d", requestCount)
	}
}

// TestNotify_NtfyServerError verifies that a non-2xx response from the ntfy
// server results in ok=false with a populated ErrorEnvelope.
func TestNotify_NtfyServerError(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer backend.Close()

	chanClient, _, cleanup := setup(t, backend,
		plugintest.WithInstanceConfigJSON(`{"server_url":"`+backend.URL+`","default_topic":"alerts"}`),
		plugintest.WithCredentialsJSON(`{}`),
	)
	defer cleanup()

	resp, err := chanClient.Notify(context.Background(), &channelv1.NotifyRequest{
		PayloadJson: `{"body":"test"}`,
	})
	if err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if resp.GetOk() {
		t.Fatal("expected ok=false for 503, got ok=true")
	}
	if resp.GetError() == nil {
		t.Fatal("expected error envelope, got nil")
	}
	if resp.GetError().GetMessage() == "" {
		t.Error("expected non-empty error message")
	}
}

// TestNotify_NoAPIKey verifies that when no API key is configured, no
// Authorization header is sent to the ntfy server.
func TestNotify_NoAPIKey(t *testing.T) {
	var gotAuth string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	chanClient, _, cleanup := setup(t, backend,
		plugintest.WithInstanceConfigJSON(`{"server_url":"`+backend.URL+`","default_topic":"alerts"}`),
		// Empty credentials — no api_key.
		plugintest.WithCredentialsJSON(`{}`),
	)
	defer cleanup()

	resp, err := chanClient.Notify(context.Background(), &channelv1.NotifyRequest{
		PayloadJson: `{"body":"no auth test"}`,
	})
	if err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if !resp.GetOk() {
		t.Fatalf("expected ok=true, got error: %v", resp.GetError().GetMessage())
	}
	if gotAuth != "" {
		t.Errorf("Authorization header: want empty, got %q", gotAuth)
	}
}
