# Graceful Shutdown Sequence

On SIGINT/SIGTERM, `main.go` cancels the root context, quiesces plugin trigger ingress, then drains every background loop and in-flight run concurrently under a single `GLEIPNIR_DRAIN_TIMEOUT` budget (default 5m) before stopping the plugin runtime and the HTTP server.

```mermaid
sequenceDiagram
    participant SIG as SIGINT / SIGTERM
    participant MAIN as main.go
    participant RT as plugin runtime
    participant BG as Background loops<br/>(poller · cron · scheduler ·<br/>approval / feedback / plugin-request scanners)
    participant RM as RunManager
    participant AGENTS as Active agents
    participant HTTP as HTTP server

    SIG ->> MAIN: Signal received
    MAIN ->> MAIN: cancel() root context<br/>(signals loops; does NOT cancel runs)
    MAIN ->> RT: quiesceTriggers()<br/>clear EmitEvent sink + Supervisor.StopAll()
    MAIN ->> RM: CancelAll() — cancel each agent context
    RM ->> AGENTS: contexts cancelled

    Note over MAIN,RM: Concurrent drain, bounded by GLEIPNIR_DRAIN_TIMEOUT (5m)
    par drain in one goroutine
        BG -->> MAIN: poller / cron / scheduler / scanners Wait()
    and
        AGENTS -->> RM: goroutines exit
        RM -->> MAIN: runManager.Wait()
    end
    Note over MAIN: select { runsDrained | After(DrainTimeout) }

    MAIN ->> RT: shutdown()<br/>Supervisor.StopAll → join bg goroutines (OAuth + dedup sweeper, 5s)<br/>→ stop subprocesses → close dispatch pool
    MAIN ->> HTTP: Shutdown(shutdownTimeout)
    HTTP -->> MAIN: Connections drained
    MAIN ->> MAIN: httpWG.Wait() → Exit 0
```

Notes:

- **`cancel()` does not stop in-flight runs.** Run contexts derive from `context.Background()` (see `launcher.go`); `RunManager.CancelAll()` is what cancels them (#500).
- **Triggers are quiesced before the run drain.** Both ingresses — the per-instance `TriggerService.Start` streams and the substrate-initiated `hostsvc.EmitEvent` RPC — are closed so no late `RunLauncher.Launch` can register a run after `runManager.Wait()` returns (add-after-Wait hazard, #500).
- **One joined drain.** The poller, cron runner, scheduler (in-flight `fire()`), the approval/feedback/plugin-request timeout scanners (in-flight `resolveTimeout()`), and the run manager are all waited in one goroutine raced against `GLEIPNIR_DRAIN_TIMEOUT` (#487).
- **Plugin runtime stops after the drain.** `rt.shutdown()` joins the OAuth nonce janitor, OAuth refresh scanner, and dedup sweeper (`bgWG`, bounded 5s) before tearing down subprocesses and the dispatch pool.
