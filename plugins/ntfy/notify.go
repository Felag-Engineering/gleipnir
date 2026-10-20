package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/felag-engineering/gleipnir/plugin-sdk/channelext"
	"github.com/felag-engineering/gleipnir/plugin-sdk/hostclient"
)

const (
	defaultServerURL = "https://ntfy.sh"
	publishTimeout   = 15 * time.Second
	// errBodyLimit bounds how much of a failed publish's response is read; it
	// is only context for the error, never parsed.
	errBodyLimit = 1024
)

// topicPattern is ntfy's own topic syntax. Checking it here keeps an address
// from smuggling extra path segments or a query string into the publish URL.
var topicPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// instanceConfig is the per-instance configuration, matching the manifest's
// config_schema.
type instanceConfig struct {
	ServerURL    string `json:"server_url"`
	DefaultTopic string `json:"default_topic"`
	APIToken     string `json:"api_token"`
}

// hostAPI is the part of the host endpoint the notifier uses;
// *hostclient.Client satisfies it.
type hostAPI interface {
	GetInstanceConfig(ctx context.Context) (*hostclient.GetInstanceConfigResponse, error)
	Log(ctx context.Context, req hostclient.LogRequest) (*hostclient.LogResponse, error)
}

type notifier struct {
	host hostAPI
	http *http.Client
}

// newNotifier returns a notifier publishing through httpClient, or through a
// client with a bounded timeout when httpClient is nil.
func newNotifier(host hostAPI, httpClient *http.Client) *notifier {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: publishTimeout}
	}
	return &notifier{host: host, http: httpClient}
}

// loadConfig reads the instance config on every notification so an admin's
// change takes effect without restarting the container.
func (n *notifier) loadConfig(ctx context.Context) (instanceConfig, error) {
	resp, err := n.host.GetInstanceConfig(ctx)
	if err != nil {
		return instanceConfig{}, fmt.Errorf("get instance config: %w", err)
	}
	var cfg instanceConfig
	if resp.ConfigJSON != "" {
		if err := json.Unmarshal([]byte(resp.ConfigJSON), &cfg); err != nil {
			return instanceConfig{}, fmt.Errorf("parse instance config: %w", err)
		}
	}
	if cfg.ServerURL == "" {
		cfg.ServerURL = defaultServerURL
	}
	return cfg, nil
}

// publishURL validates cfg and the resolved topic and returns the URL to POST
// to. The topic is the notification's address, falling back to default_topic.
func publishURL(cfg instanceConfig, address string) (string, error) {
	base, err := url.Parse(cfg.ServerURL)
	if err != nil {
		return "", fmt.Errorf("server_url %q is not a valid URL: %w", cfg.ServerURL, err)
	}
	if (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" {
		return "", fmt.Errorf("server_url %q must be an http or https URL with a host", cfg.ServerURL)
	}

	topic := address
	if topic == "" {
		topic = cfg.DefaultTopic
	}
	if topic == "" {
		return "", fmt.Errorf("no topic: the target address is empty and default_topic is not configured")
	}
	if !topicPattern.MatchString(topic) {
		return "", fmt.Errorf("topic %q is not a valid ntfy topic (1-64 letters, digits, '-' or '_')", topic)
	}
	return strings.TrimRight(cfg.ServerURL, "/") + "/" + topic, nil
}

// Notify is the channelext.NotifyFunc: it publishes the message to the ntfy
// topic and reports any failure to the host as a JSON-RPC error.
func (n *notifier) Notify(ctx context.Context, notification channelext.Notification) error {
	err := n.deliver(ctx, notification)
	if err != nil {
		n.log(ctx, hostclient.LogLevelError, "notify failed", map[string]string{"error": err.Error()})
		return err
	}
	n.log(ctx, hostclient.LogLevelInfo, "notification published", nil)
	return nil
}

func (n *notifier) deliver(ctx context.Context, notification channelext.Notification) error {
	cfg, err := n.loadConfig(ctx)
	if err != nil {
		return err
	}
	target, err := publishURL(cfg, notification.Target.Address)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, strings.NewReader(notification.Message))
	if err != nil {
		return fmt.Errorf("build publish request: %w", err)
	}
	req.Header.Set("Content-Type", "text/plain; charset=utf-8")
	if cfg.APIToken != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.APIToken)
	}

	resp, err := n.http.Do(req)
	if err != nil {
		// The *url.Error text carries the URL, which holds the topic but never
		// the token (that travels in a header).
		return fmt.Errorf("publish to ntfy: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, errBodyLimit))
		return fmt.Errorf("ntfy returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(detail)))
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}

// log routes a line through the host's logging pipeline (stdout is not
// collected). A failed log call must not fail or mask the delivery outcome.
func (n *notifier) log(ctx context.Context, level, msg string, attrs map[string]string) {
	_, _ = n.host.Log(ctx, hostclient.LogRequest{Level: level, Msg: msg, Attrs: attrs})
}
