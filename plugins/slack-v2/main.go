// Command slack-v2 is the Slack plugin rebuilt as a containerized MCP server.
// The v1 plugin (plugins/slack) stays live until the cutover; see README.md.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/felag-engineering/gleipnir/plugin-sdk/hostclient"
	"github.com/felag-engineering/gleipnir/plugin-sdk/mcpserver"
)

const (
	listenAddr      = ":8080"
	shutdownTimeout = 10 * time.Second

	// slackAPIURLEnvVar overrides the Slack Web API base URL, so the plugin
	// can be pointed at a fake Slack. Unset means the real API.
	slackAPIURLEnvVar = "SLACK_API_URL"
)

// plugin holds what the tool handlers share.
type plugin struct {
	slack  *slackProvider
	health *healthReporter
}

// hostAPI is everything the plugin needs from the host endpoint;
// *hostclient.Client satisfies it.
type hostAPI interface {
	credentialSource
	healthSetter
}

// newServer registers every tool on a fresh MCP server.
func newServer(host hostAPI, apiURL string) (*mcpserver.Server, error) {
	p := &plugin{
		slack:  newSlackProvider(host, apiURL, nil),
		health: newHealthReporter(host),
	}
	srv := mcpserver.NewServer("slack-v2", pluginVersion)
	for _, tool := range p.readTools() {
		if err := srv.RegisterTool(tool); err != nil {
			return nil, fmt.Errorf("register tool: %w", err)
		}
	}
	return srv, nil
}

func main() {
	if err := run(); err != nil {
		log.Fatalf("slack-v2: %v", err)
	}
}

func run() error {
	host, err := hostclient.New()
	if err != nil {
		return fmt.Errorf("host client: %w", err)
	}
	srv, err := newServer(host, os.Getenv(slackAPIURLEnvVar))
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	httpSrv := &http.Server{Addr: listenAddr, Handler: srv, ReadHeaderTimeout: 10 * time.Second}
	serveErr := make(chan error, 1)
	go func() { serveErr <- httpSrv.ListenAndServe() }()
	log.Printf("slack-v2 listening on %s", listenAddr)

	select {
	case err := <-serveErr:
		return err
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("shutdown: %w", err)
	}
	return nil
}
