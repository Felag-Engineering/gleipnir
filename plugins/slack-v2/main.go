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

// declaredProfiles is the capability profiles manifest.yaml declares; a test
// keeps the two in step. The socket hub reports health only for declared
// profiles, so adding human_channel or event_source here (with the manifest)
// is all it takes for connection loss to be reported against them.
var declaredProfiles = []string{hostclient.ProfileToolProvider, hostclient.ProfileHumanChannel}

// newServer registers every tool on a fresh MCP server.
func newServer(host hostAPI, apiURL string) (*mcpserver.Server, error) {
	p := &plugin{
		slack:  newSlackProvider(host, apiURL, nil),
		health: newHealthReporter(host),
	}
	srv := mcpserver.NewServer("slack-v2", pluginVersion)
	for _, tool := range p.allTools() {
		if err := srv.RegisterTool(tool); err != nil {
			return nil, fmt.Errorf("register tool: %w", err)
		}
	}
	// channel/request (#992) is not mounted yet: it is refused as an unknown
	// method rather than accepted and never answered.
	ext, err := p.newChannelExtension()
	if err != nil {
		return nil, fmt.Errorf("build channel extension: %w", err)
	}
	if err := srv.Mount(ext); err != nil {
		return nil, fmt.Errorf("mount channel extension: %w", err)
	}
	return srv, nil
}

func (p *plugin) allTools() []mcpserver.Tool {
	return append(p.readTools(), p.writeTools()...)
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

	// Handlers register on the hub here as the channel and event features
	// land; until one does the hub is inert and no connection is opened.
	hub := newSocketHub(newClientRunnerFactory(host, newSlackProvider(host, os.Getenv(slackAPIURLEnvVar), nil)), host, declaredProfiles)
	hubDone := make(chan struct{})
	go func() {
		defer close(hubDone)
		runSocketHub(ctx, hub, host)
	}()

	httpSrv := &http.Server{Addr: listenAddr, Handler: srv, ReadHeaderTimeout: 10 * time.Second}
	serveErr := make(chan error, 1)
	go func() { serveErr <- httpSrv.ListenAndServe() }()
	log.Printf("slack-v2 listening on %s", listenAddr)

	var serveFailure error
	select {
	case serveFailure = <-serveErr:
		stop()
	case <-ctx.Done():
	}
	<-hubDone
	if serveFailure != nil {
		return serveFailure
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("shutdown: %w", err)
	}
	return nil
}
