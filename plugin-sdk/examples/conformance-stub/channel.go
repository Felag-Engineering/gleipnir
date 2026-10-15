package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/felag-engineering/gleipnir/plugin-sdk/channelext"
	"github.com/felag-engineering/gleipnir/plugin-sdk/hostclient"
)

// actorAuthorizer is the one host call the auto-resolver needs;
// *hostclient.Client satisfies it.
type actorAuthorizer interface {
	AuthorizeActor(ctx context.Context, req hostclient.AuthorizeActorRequest) (*hostclient.AuthorizeActorResponse, error)
}

// rejectOptionIDs are the option ids the reject mode recognises as a refusal.
// The channel contract gives option ids no meaning of their own, so a refusal
// can only be recognised by convention.
var rejectOptionIDs = map[string]bool{"reject": true, "deny": true, "decline": true}

// autoResolver plays the part of a human answering a channel/request, so a
// conformance run can exercise the whole request lifecycle without one.
//
// Every completion is preceded by a successful host/authorize_actor for the
// configured actor, as a real channel would before letting a click settle
// anything. An unauthorized actor, or a failed authorization call, leaves the
// task working: that is the contract's "the request stays open" outcome.
type autoResolver struct {
	mode    string
	delay   time.Duration
	actorID string

	host  actorAuthorizer
	tasks channelext.TaskStore

	// ctx is owned by the process lifecycle, not by any request: the delayed
	// work must outlive the channel/request call that started it.
	ctx context.Context
	// wait blocks for d or until ctx ends. Swapped in tests so they never
	// sleep on a wall clock.
	wait func(ctx context.Context, d time.Duration) error

	wg      sync.WaitGroup
	mu      sync.Mutex
	pending map[string]context.CancelFunc
}

func newAutoResolver(ctx context.Context, cfg Config, host actorAuthorizer, tasks channelext.TaskStore) *autoResolver {
	return &autoResolver{
		mode:    cfg.AutoResolve,
		delay:   cfg.AutoResolveDelay,
		actorID: cfg.ActorID,
		host:    host,
		tasks:   tasks,
		ctx:     ctx,
		wait:    sleepCtx,
		pending: map[string]context.CancelFunc{},
	}
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Request is the channelext.RequestFunc. It returns immediately; the answer
// arrives later through the task store.
func (a *autoResolver) Request(_ context.Context, taskID string, req channelext.Request) error {
	if a.mode == autoResolveOff {
		return nil
	}
	ctx, cancel := context.WithCancel(a.ctx)
	a.mu.Lock()
	a.pending[taskID] = cancel
	a.mu.Unlock()

	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		defer a.forget(taskID)
		a.resolve(ctx, taskID, req)
	}()
	return nil
}

// Cancel is the channelext.CancelFunc: it stops a resolution still waiting out
// its delay. A resolution already past that point is stopped by the task
// store instead, which refuses to complete a terminal task.
func (a *autoResolver) Cancel(_ context.Context, taskID, _ string) {
	a.mu.Lock()
	cancel := a.pending[taskID]
	a.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (a *autoResolver) forget(taskID string) {
	a.mu.Lock()
	cancel := a.pending[taskID]
	delete(a.pending, taskID)
	a.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// Wait blocks until every in-flight resolution has finished. Call it after
// cancelling the context given to newAutoResolver.
func (a *autoResolver) Wait() { a.wg.Wait() }

func (a *autoResolver) resolve(ctx context.Context, taskID string, req channelext.Request) {
	resolution, ok := resolutionFor(a.mode, req)
	if !ok {
		log.Printf("conformance-stub: task %s: mode %q has no answer for this request; leaving it working", taskID, a.mode)
		return
	}

	if err := a.wait(ctx, a.delay); err != nil {
		return
	}

	auth, err := a.host.AuthorizeActor(ctx, hostclient.AuthorizeActorRequest{
		RequestID:       taskID,
		ActorExternalID: a.actorID,
	})
	if err != nil {
		if ctx.Err() == nil {
			log.Printf("conformance-stub: task %s: authorize_actor failed; leaving it working: %v", taskID, err)
		}
		return
	}
	if !auth.Authorized {
		log.Printf("conformance-stub: task %s: actor %q is not authorized; leaving it working", taskID, a.actorID)
		return
	}

	resolution.ActorExternalID = a.actorID
	if _, err := a.tasks.Complete(ctx, taskID, resolution); err != nil {
		if errors.Is(err, channelext.ErrTaskTerminal) {
			log.Printf("conformance-stub: task %s: already terminal, not completing: %v", taskID, err)
			return
		}
		log.Printf("conformance-stub: task %s: complete: %v", taskID, err)
	}
}

// resolutionFor picks the answer a mode gives. ok is false when the mode has
// no honest answer, in which case the task is left working rather than
// answered with something the request did not offer.
//
//   - first_option: the first option, or an empty content object for a
//     schema-only request.
//   - reject: the contract defines no rejecting resolution, only option ids and
//     content. The answer is therefore the option whose id is conventionally a
//     refusal (reject, deny, decline); with no such option, or with a
//     schema-only request, there is none.
func resolutionFor(mode string, req channelext.Request) (channelext.Resolution, bool) {
	switch mode {
	case autoResolveFirstOption:
		if len(req.Options) > 0 {
			return channelext.Resolution{OptionID: req.Options[0].ID}, true
		}
		return channelext.Resolution{Content: json.RawMessage(`{}`)}, true
	case autoResolveReject:
		for _, o := range req.Options {
			if rejectOptionIDs[strings.ToLower(o.ID)] {
				return channelext.Resolution{OptionID: o.ID}, true
			}
		}
	}
	return channelext.Resolution{}, false
}

// logNotify is the stub's channel/notify: it accepts and logs. A real channel
// would deliver the message; the stub has nowhere to deliver it.
func logNotify(_ context.Context, n channelext.Notification) error {
	log.Printf("conformance-stub: notify %s/%s: %s", n.Target.Delivery, n.Target.Address, n.Message)
	return nil
}
