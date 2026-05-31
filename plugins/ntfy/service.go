package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	hostv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/host/v1"
	"github.com/felag-engineering/gleipnir/plugin-sdk/channel"
	"github.com/felag-engineering/gleipnir/plugin-sdk/pluginerr"
	"github.com/felag-engineering/gleipnir/plugin-sdk/serve"
)

// ChannelService implements channel.Service with Notify only.
// It POSTs to <server_url>/<topic> with an optional API key auth header.
type ChannelService struct {
	host       hostv1.HostServiceClient
	httpClient *http.Client
}

// NewChannelService creates a ChannelService that uses hostClient for host RPCs
// and httpClient for outbound HTTP calls to the ntfy server.
func NewChannelService(hostClient hostv1.HostServiceClient, httpClient *http.Client) *ChannelService {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &ChannelService{host: hostClient, httpClient: httpClient}
}

// instanceConfig is the per-install configuration shape stored by the host.
type instanceConfig struct {
	ServerURL      string `json:"server_url"`
	DefaultTopic   string `json:"default_topic"`
	AuthHeaderName string `json:"auth_header_name"`
}

// channelConfig is the per-audience-entry configuration shape.
type channelConfig struct {
	Topic string `json:"topic"`
}

// credentials is the shape of the API key credential stored by the host.
// The host stores it as {"api_key":"<token>"}.
type credentials struct {
	APIKey string `json:"api_key"`
}

// notifyPayload extracts title and body from the loosely-typed payload JSON.
// Unknown fields are tolerated per the forward-compatibility convention.
type notifyPayload struct {
	Title string `json:"title"`
	Body  string `json:"body"`
	// message is a common alias for body in ntfy event payloads.
	Message string `json:"message"`
}

// Notify delivers a fire-and-forget notification to ntfy.
// Plugins MUST honor ctx.Done(): when the host cancels the call (run cancelled
// or operator cancellation), every blocking I/O must use this ctx.
func (s *ChannelService) Notify(ctx context.Context, n channel.Notification) error {
	// Propagate the host-injected call ID to all outgoing host RPCs so the host
	// can correlate them back to this run and step. See serve.WithCallContext
	// and plugin-system-spec.md §8.5.
	hostCtx := serve.WithCallContext(ctx)

	// 1. Fetch instance config (server URL, default topic, auth header name).
	cfgResp, err := s.host.GetInstanceConfig(hostCtx, &hostv1.GetInstanceConfigRequest{})
	if err != nil {
		return pluginerr.Internal(fmt.Sprintf("GetInstanceConfig: %v", err))
	}
	var cfg instanceConfig
	if err := json.Unmarshal([]byte(cfgResp.GetConfigJson()), &cfg); err != nil {
		return pluginerr.Internal(fmt.Sprintf("parse instance config: %v", err))
	}
	if cfg.AuthHeaderName == "" {
		cfg.AuthHeaderName = "Authorization"
	}

	// 2. Fetch credentials (API key is optional — ntfy supports unauthenticated topics).
	credResp, err := s.host.GetCredentials(hostCtx, &hostv1.GetCredentialsRequest{})
	if err != nil {
		return pluginerr.Internal(fmt.Sprintf("GetCredentials: %v", err))
	}
	var creds credentials
	// Tolerate empty credentials JSON (no key configured).
	if raw := credResp.GetCredentialsJson(); raw != "" && raw != "{}" {
		if err := json.Unmarshal([]byte(raw), &creds); err != nil {
			return pluginerr.Internal(fmt.Sprintf("parse credentials: %v", err))
		}
	}

	// 3. Resolve topic: per-audience entry config overrides instance default.
	var chanCfg channelConfig
	if raw := n.ChannelConfig; len(raw) > 0 {
		_ = json.Unmarshal(raw, &chanCfg) // tolerate missing/malformed
	}
	topic := chanCfg.Topic
	if topic == "" {
		topic = cfg.DefaultTopic
	}
	if topic == "" {
		return pluginerr.InvalidArg("no topic: set default_topic in instance config or topic in channel config")
	}

	// 4. Extract title and body from the notification payload.
	var payload notifyPayload
	if raw := n.Payload; len(raw) > 0 {
		_ = json.Unmarshal(raw, &payload) // tolerate unknown schemas
	}
	body := payload.Body
	if body == "" {
		body = payload.Message
	}

	// 5. POST to <server_url>/<topic>.
	url := strings.TrimRight(cfg.ServerURL, "/") + "/" + topic
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		return pluginerr.Internal(fmt.Sprintf("build request: %v", err))
	}
	if payload.Title != "" {
		httpReq.Header.Set("Title", payload.Title)
	}
	if creds.APIKey != "" {
		httpReq.Header.Set(cfg.AuthHeaderName, "Bearer "+creds.APIKey)
	}

	httpResp, err := s.httpClient.Do(httpReq)
	if err != nil {
		return pluginerr.Internal(fmt.Sprintf("POST %s: %v", url, err))
	}
	defer httpResp.Body.Close()
	_, _ = io.Copy(io.Discard, httpResp.Body)

	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		return pluginerr.Internal(fmt.Sprintf("ntfy returned HTTP %d", httpResp.StatusCode))
	}

	return nil
}

// Request is not supported by ntfy — it is a Notify-only channel plugin.
// Returning pluginerr.Unimplemented produces an application-level
// RequestResponse{Acked: false, Error: {UNIMPLEMENTED}} envelope rather than
// a gRPC status error. In normal operation the host never routes Request to
// this plugin because the manifest declares Notify-only channel_capabilities.
func (s *ChannelService) Request(_ context.Context, _ channel.FeedbackRequest) error {
	return pluginerr.Unimplemented("ntfy supports Notify only; Request is not implemented")
}
