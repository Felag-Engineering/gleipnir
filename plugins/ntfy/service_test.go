package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	channelv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/channel/v1"
	hostv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/host/v1"
	plugintest "github.com/felag-engineering/gleipnir/plugin-sdk/testing"
	"github.com/felag-engineering/gleipnir/plugin-sdk/channel"
)

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

	h := plugintest.NewChannelHarness(t, func(hc hostv1.HostServiceClient) channel.Service {
		var httpClient *http.Client
		if backend != nil {
			httpClient = backend.Client()
		}
		return NewChannelService(hc, httpClient)
	},
		plugintest.WithInstanceConfigJSON(`{"server_url":"`+backend.URL+`","default_topic":"alerts"}`),
		plugintest.WithCredentialsJSON(`{"api_key":"k-test"}`),
	)

	resp, err := h.Client.Notify(context.Background(), &channelv1.NotifyRequest{
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

	h := plugintest.NewChannelHarness(t, func(hc hostv1.HostServiceClient) channel.Service {
		var httpClient *http.Client
		if backend != nil {
			httpClient = backend.Client()
		}
		return NewChannelService(hc, httpClient)
	},
		plugintest.WithInstanceConfigJSON(`{"server_url":"`+backend.URL+`","default_topic":"alerts"}`),
		plugintest.WithCredentialsJSON(`{}`),
	)

	resp, err := h.Client.Notify(context.Background(), &channelv1.NotifyRequest{
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

	h := plugintest.NewChannelHarness(t, func(hc hostv1.HostServiceClient) channel.Service {
		var httpClient *http.Client
		if backend != nil {
			httpClient = backend.Client()
		}
		return NewChannelService(hc, httpClient)
	},
		// No default_topic in instance config; no topic in channel config.
		plugintest.WithInstanceConfigJSON(`{"server_url":"`+backend.URL+`"}`),
		plugintest.WithCredentialsJSON(`{}`),
	)

	resp, err := h.Client.Notify(context.Background(), &channelv1.NotifyRequest{
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

	h := plugintest.NewChannelHarness(t, func(hc hostv1.HostServiceClient) channel.Service {
		var httpClient *http.Client
		if backend != nil {
			httpClient = backend.Client()
		}
		return NewChannelService(hc, httpClient)
	},
		plugintest.WithInstanceConfigJSON(`{"server_url":"`+backend.URL+`","default_topic":"alerts"}`),
		plugintest.WithCredentialsJSON(`{}`),
	)

	resp, err := h.Client.Notify(context.Background(), &channelv1.NotifyRequest{
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

	h := plugintest.NewChannelHarness(t, func(hc hostv1.HostServiceClient) channel.Service {
		var httpClient *http.Client
		if backend != nil {
			httpClient = backend.Client()
		}
		return NewChannelService(hc, httpClient)
	},
		plugintest.WithInstanceConfigJSON(`{"server_url":"`+backend.URL+`","default_topic":"alerts"}`),
		// Empty credentials — no api_key.
		plugintest.WithCredentialsJSON(`{}`),
	)

	resp, err := h.Client.Notify(context.Background(), &channelv1.NotifyRequest{
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
