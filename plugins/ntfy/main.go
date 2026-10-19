// Command ntfy is a notify-only io.gleipnir/channel server that publishes
// Gleipnir notifications to an ntfy topic. It is a v2 plugin: a containerized
// MCP server on streamable HTTP, with no gRPC or go-plugin involvement.
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
	"github.com/felag-engineering/gleipnir/plugin-sdk/manifestv2"
	"github.com/felag-engineering/gleipnir/plugin-sdk/mcpserver"
)

const (
	defaultListenAddr = ":8080"
	shutdownTimeout   = 10 * time.Second
	serverVersion     = "0.2.0"
)

// declaration is what the server serves in server/discover. manifest.yaml
// attests the same assurance; manifest_test.go pins the two together.
//
// Weak: an ntfy topic is a shared space that cannot establish which Gleipnir
// user acted. That is moot for a notify-only channel, but the declaration must
// still say it.
var declaration = channelext.Declaration{
	Version:    channelext.ExtensionVersion,
	Assurance:  manifestv2.AssuranceWeak,
	Deliveries: []string{channelext.DeliveryShared},
}

// newServer composes the MCP server. No channelext.WithRequest option is
// passed, so channel/request is answered with method-not-found: this channel
// can notify but has no way to take an answer back.
func newServer(n *notifier) (*mcpserver.Server, error) {
	ext, err := channelext.New(declaration, n.Notify)
	if err != nil {
		return nil, fmt.Errorf("build channel extension: %w", err)
	}
	srv := mcpserver.NewServer("ntfy", serverVersion)
	if err := srv.Mount(ext); err != nil {
		return nil, fmt.Errorf("mount channel: %w", err)
	}
	return srv, nil
}

func main() {
	if err := run(); err != nil {
		log.Fatalf("ntfy: %v", err)
	}
}

func run() error {
	host, err := hostclient.New()
	if err != nil {
		return fmt.Errorf("host client: %w", err)
	}
	srv, err := newServer(newNotifier(host, nil))
	if err != nil {
		return err
	}

	addr := os.Getenv("LISTEN_ADDR")
	if addr == "" {
		addr = defaultListenAddr
	}
	httpSrv := &http.Server{
		Addr:              addr,
		Handler:           srv,
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	serveErr := make(chan error, 1)
	go func() { serveErr <- httpSrv.ListenAndServe() }()
	log.Printf("ntfy listening on %s", addr)

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
