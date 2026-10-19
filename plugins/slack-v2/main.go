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

	"github.com/felag-engineering/gleipnir/plugin-sdk/channelext"
	"github.com/felag-engineering/gleipnir/plugin-sdk/hostclient"
	"github.com/felag-engineering/gleipnir/plugin-sdk/mcpserver"
)

const (
	defaultListenAddr = ":8080"
	shutdownTimeout   = 10 * time.Second

	// listenAddrEnvVar overrides the address the MCP endpoint listens on. In
	// the container the manifest's transport port (8080) is what the host
	// dials, so the default stays put; the override exists for the in-process
	// e2e, which runs the binary beside other listeners on one host.
	listenAddrEnvVar = "LISTEN_ADDR"

	// slackAPIURLEnvVar overrides the Slack Web API base URL, so the plugin
	// can be pointed at a fake Slack. Unset means the real API.
	slackAPIURLEnvVar = "SLACK_API_URL"

	// dataDirEnvVar names the instance volume holding the durable task store
	// and the posted-message records, so an open request survives a restart.
	dataDirEnvVar  = "DATA_DIR"
	defaultDataDir = "/data"
)

// plugin holds what the tool handlers share.
type plugin struct {
	slack  *slackProvider
	health *healthReporter
	refs   *messageRefStore
	tasks  channelext.TaskStore
	events *eventSource
}

// hostAPI is everything the plugin needs from the host endpoint;
// *hostclient.Client satisfies it.
type hostAPI interface {
	credentialSource
	healthSetter
}

// declaredProfiles is the capability profiles manifest.yaml declares; a test
// keeps the two in step. The socket hub reports health only for declared
// profiles: human_channel and event_source are reported against when the
// connection is lost.
var declaredProfiles = []string{hostclient.ProfileToolProvider, hostclient.ProfileEventSource, hostclient.ProfileHumanChannel, hostclient.ProfileIdentityProvider}

// newServer registers every tool on a fresh MCP server.
func newServer(host hostAPI, apiURL, dataDir string) (*mcpserver.Server, *plugin, error) {
	p := &plugin{
		slack:  newSlackProvider(host, apiURL, nil),
		health: newHealthReporter(host),
	}
	srv := mcpserver.NewServer("slack-v2", pluginVersion)
	for _, tool := range p.allTools() {
		if err := srv.RegisterTool(tool); err != nil {
			return nil, nil, fmt.Errorf("register tool: %w", err)
		}
	}
	ext, err := p.newChannelExtension(dataDir)
	if err != nil {
		return nil, nil, fmt.Errorf("build channel extension: %w", err)
	}
	p.tasks = ext.Tasks()
	if err := srv.Mount(ext); err != nil {
		return nil, nil, fmt.Errorf("mount channel extension: %w", err)
	}
	src, err := newEventSource(dataDir, p.slack)
	if err != nil {
		return nil, nil, err
	}
	p.events = src
	if err := srv.Mount(src.handler); err != nil {
		return nil, nil, errors.Join(fmt.Errorf("mount events extension: %w", err), src.close())
	}
	if err := srv.MountStreaming("events/listen", src.listenHandler()); err != nil {
		return nil, nil, errors.Join(fmt.Errorf("mount events/listen: %w", err), src.close())
	}
	return srv, p, nil
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
	dataDir := os.Getenv(dataDirEnvVar)
	if dataDir == "" {
		dataDir = defaultDataDir
	}
	srv, p, err := newServer(host, os.Getenv(slackAPIURLEnvVar), dataDir)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Handlers register on the hub before it runs; with none registered the hub
	// is inert and no connection is opened.
	hub := newSocketHub(newClientRunnerFactory(host, p.slack), host, declaredProfiles)
	clicks := newClickHandler(p, host)
	if err := hub.Handle(EnvelopeInteractive, routeInteractive(clicks.handle, p.events)); err != nil {
		return fmt.Errorf("register interactive handler: %w", err)
	}
	if err := hub.Handle(EnvelopeEventsAPI, p.events.handleEventsAPI); err != nil {
		return fmt.Errorf("register events handler: %w", err)
	}
	if err := hub.Handle(EnvelopeSlashCommands, p.events.handleSlashCommand); err != nil {
		return fmt.Errorf("register slash command handler: %w", err)
	}
	hubDone := make(chan struct{})
	go func() {
		defer close(hubDone)
		runSocketHub(ctx, hub, host)
	}()

	listenAddr := os.Getenv(listenAddrEnvVar)
	if listenAddr == "" {
		listenAddr = defaultListenAddr
	}
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
	clicks.wait()
	p.events.closeStreams()
	defer func() {
		if err := p.events.close(); err != nil {
			log.Printf("slack-v2: close event store: %v", err)
		}
	}()
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
