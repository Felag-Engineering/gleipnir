// Command conformance-stub is a configurable stub plugin for exercising a
// host's conformance checks: one container image that can present as any
// combination of the tool_provider, event_source and human_channel profiles,
// chosen at startup by STUB_PROFILES. See README.md for the full
// configuration table.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/felag-engineering/gleipnir/plugin-sdk/channelext"
	"github.com/felag-engineering/gleipnir/plugin-sdk/events"
	"github.com/felag-engineering/gleipnir/plugin-sdk/hostclient"
	"github.com/felag-engineering/gleipnir/plugin-sdk/mcpserver"
)

const (
	listenAddr      = ":8080"
	eventRetention  = 24 * time.Hour
	shutdownTimeout = 10 * time.Second
)

// eventKinds is declared once; manifest.go derives the manifests' event_kinds
// from it, and events.NewHandler serves it.
var eventKinds = []events.Kind{
	{
		Kind:     "conformance.tick",
		Guidance: "Fires on a fixed schedule from the stub's own configuration; carries a label to bind on.",
		BindingSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"label": map[string]any{"type": "string", "description": "Label carried by the tick."},
			},
		},
		Operators: map[string][]string{"label": {"equals", "contains"}},
	},
}

var echoTool = mcpserver.Tool{
	Name:        "conformance.echo",
	Description: "Returns the message it is given.",
	InputSchema: json.RawMessage(`{"type":"object","properties":{"message":{"type":"string"}},"required":["message"]}`),
	Handler: func(_ context.Context, args json.RawMessage) (any, error) {
		var in struct {
			Message string `json:"message"`
		}
		if err := json.Unmarshal(args, &in); err != nil {
			return nil, fmt.Errorf("decode arguments: %w", err)
		}
		return map[string]string{"message": in.Message}, nil
	},
}

// stub is the composed server plus the pieces that need an orderly shutdown.
type stub struct {
	Handler http.Handler
	Events  *events.Handler // nil unless event_source is enabled
	Channel *autoResolver   // nil unless human_channel is enabled

	// EmitStart is closed when tick emission may begin: at once, or when the
	// first events/listen stream opens (STUB_EMIT_START=first_listener).
	EmitStart <-chan struct{}

	closers []func() error
}

// Close releases the stores. Cancel the context given to newStub first.
func (s *stub) Close() error {
	if s.Channel != nil {
		s.Channel.Wait()
	}
	var errs []error
	for _, c := range s.closers {
		errs = append(errs, c())
	}
	return errors.Join(errs...)
}

// newStub composes the server for cfg. host is only used by the channel
// auto-resolver and may be nil when cfg.AutoResolve is off. ctx owns any
// background work and must outlive the returned stub's use.
func newStub(ctx context.Context, cfg Config, host actorAuthorizer) (*stub, error) {
	srv := mcpserver.NewServer("conformance-stub", "0.1.0")
	s := &stub{Handler: srv}

	if cfg.Profiles.ToolProvider {
		if err := srv.RegisterTool(echoTool); err != nil {
			return nil, err
		}
	}

	if cfg.Profiles.EventSource {
		store, err := events.NewFileStore(filepath.Join(cfg.DataDir, "events"), eventRetention)
		if err != nil {
			return nil, fmt.Errorf("open event store: %w", err)
		}
		s.closers = append(s.closers, store.Close)
		buffer, err := events.NewBufferWithStore(ctx, store)
		if err != nil {
			return nil, fmt.Errorf("open event buffer: %w", err)
		}
		s.Events = events.NewHandler("conformance-stub", eventKinds, events.WithBuffer(buffer))
		if err := srv.Mount(s.Events); err != nil {
			return nil, fmt.Errorf("mount events: %w", err)
		}
		listen := s.Events.ListenHandler()
		switch cfg.EmitStart {
		case emitStartFirstListener:
			gate := newListenGate()
			listen = gate.Wrap(listen)
			s.EmitStart = gate.Started()
		default:
			immediate := make(chan struct{})
			close(immediate)
			s.EmitStart = immediate
		}
		if err := srv.MountStreaming("events/listen", listen); err != nil {
			return nil, fmt.Errorf("mount events/listen: %w", err)
		}
	}

	if cfg.Profiles.HumanChannel {
		tasks, err := channelext.NewFileTaskStore(filepath.Join(cfg.DataDir, "tasks"))
		if err != nil {
			return nil, fmt.Errorf("open task store: %w", err)
		}
		s.Channel = newAutoResolver(ctx, cfg, host, tasks)
		decl := channelext.Declaration{
			Version:    channelext.ExtensionVersion,
			Assurance:  cfg.Assurance,
			Deliveries: []string{channelext.DeliveryDirect, channelext.DeliveryShared},
		}
		ext, err := channelext.New(decl, logNotify,
			channelext.WithRequest(s.Channel.Request),
			channelext.WithTaskStore(tasks),
			channelext.WithOnCancel(s.Channel.Cancel),
		)
		if err != nil {
			return nil, fmt.Errorf("build channel extension: %w", err)
		}
		if err := srv.Mount(ext); err != nil {
			return nil, fmt.Errorf("mount channel: %w", err)
		}
	}
	return s, nil
}

func main() {
	if err := run(); err != nil {
		log.Fatalf("conformance-stub: %v", err)
	}
}

func run() error {
	cfg, err := ParseConfig(os.LookupEnv)
	if err != nil {
		return fmt.Errorf("invalid configuration:\n%w", err)
	}

	var host actorAuthorizer
	if cfg.AutoResolve != autoResolveOff {
		client, err := hostclient.New()
		if err != nil {
			return fmt.Errorf("host client (needed for STUB_AUTO_RESOLVE=%s): %w", cfg.AutoResolve, err)
		}
		host = client
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	s, err := newStub(ctx, cfg, host)
	if err != nil {
		return err
	}

	if s.Events != nil {
		go emitTicks(ctx, s.Events, cfg.EmitCount, cfg.EmitInterval, newFileProgress(cfg.DataDir), s.EmitStart)
	}

	httpSrv := &http.Server{
		Addr:    listenAddr,
		Handler: s.Handler,
		// No WriteTimeout: events/listen streams for as long as the host stays.
		ReadHeaderTimeout: 10 * time.Second,
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- httpSrv.ListenAndServe() }()
	log.Printf("conformance-stub listening on %s, profiles %v", listenAddr, cfg.Profiles.Names())

	select {
	case err := <-serveErr:
		stop()
		_ = s.Close()
		return err
	case <-ctx.Done():
	}

	// Close the event streams first: Shutdown waits on open connections, and
	// an events/listen stream never ends on its own.
	if s.Events != nil {
		s.Events.Close("shutting down")
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	return s.Close()
}
