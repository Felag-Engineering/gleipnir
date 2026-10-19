package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"

	"github.com/felag-engineering/gleipnir/plugin-sdk/channelext"
	"github.com/felag-engineering/gleipnir/plugin-sdk/hostclient"
	"github.com/slack-go/slack"
	"github.com/slack-go/slack/socketmode"
)

// Ephemeral replies. They are plugin-authored text only: nothing from the
// request or the clicker's profile is echoed back.
const (
	msgNotAuthorized  = "You are not authorized to answer this request."
	msgAlreadyDone    = "This request has already been resolved."
	msgCannotVerify   = "Could not verify that you may answer this request, so nothing was recorded. Please try again."
	msgCannotRecord   = "Could not record your answer. Please try again."
	msgRequestUnknown = "This request is no longer open."
)

// actorAuthorizer is the one host call a click needs; *hostclient.Client
// satisfies it.
type actorAuthorizer interface {
	AuthorizeActor(ctx context.Context, req hostclient.AuthorizeActorRequest) (*hostclient.AuthorizeActorResponse, error)
}

// clickHandler turns a button click into a task completion, but only after the
// host has said the clicker may settle that request.
type clickHandler struct {
	plugin *plugin
	host   actorAuthorizer
	tasks  channelext.TaskStore

	wg sync.WaitGroup
}

func newClickHandler(p *plugin, host actorAuthorizer) *clickHandler {
	return &clickHandler{plugin: p, host: host, tasks: p.tasks}
}

// wait blocks until every click accepted so far has been fully processed.
func (h *clickHandler) wait() { h.wg.Wait() }

// handle is the socketHandler for interactive envelopes.
//
// Slack needs the ack within 3 seconds and the hub acks only after a handler
// returns, while processing a click makes a host call, a disk write and up to
// two Slack calls. So the handler decodes synchronously (cheap, and the only
// part that can reject a payload that is not ours) and does the work on its own
// goroutine, acking at once. Redelivery on a missed ack is the alternative, and
// would only turn a slow click into a duplicate one. The work runs on the hub's
// context, so shutdown cancels it; the click is then simply lost and the human
// can press the button again.
func (h *clickHandler) handle(ctx context.Context, evt socketmode.Event) (any, error) {
	callback, ok := evt.Data.(slack.InteractionCallback)
	if !ok {
		return nil, fmt.Errorf("interactive envelope carried %T, not an interaction callback", evt.Data)
	}
	if callback.Type != slack.InteractionTypeBlockActions {
		return nil, nil
	}
	for _, action := range callback.ActionCallback.BlockActions {
		taskID, optionID, ok := parseActionID(action.ActionID)
		if !ok {
			continue
		}
		h.wg.Add(1)
		go func() {
			defer h.wg.Done()
			h.process(ctx, callback, taskID, optionID)
		}()
	}
	return nil, nil
}

func (h *clickHandler) process(ctx context.Context, cb slack.InteractionCallback, taskID, optionID string) {
	actor := actorExternalID(cb.User)
	if actor == "" {
		// Nobody to authorize and nobody to reply to.
		log.Printf("slack-v2: task %s: click carried no user id; ignored", taskID)
		return
	}
	reply := func(text string) { h.ephemeral(ctx, cb.Channel.ID, actor, text) }

	ref, err := h.plugin.refs.load(taskID)
	if err != nil {
		log.Printf("slack-v2: task %s: click on a message with no record: %v", taskID, err)
		reply(msgRequestUnknown)
		return
	}
	if !ref.hasOption(optionID) {
		log.Printf("slack-v2: task %s: click named option %q the request never offered", taskID, optionID)
		reply(msgRequestUnknown)
		return
	}

	auth, err := h.host.AuthorizeActor(ctx, hostclient.AuthorizeActorRequest{
		RequestID:       taskID,
		ActorExternalID: actor,
	})
	if err != nil {
		// Fail closed: a click the host could not vouch for settles nothing.
		log.Printf("slack-v2: task %s: authorize_actor: %v", taskID, err)
		reply(msgCannotVerify)
		return
	}
	if auth == nil || !auth.Authorized {
		// Not an error: the task stays working for someone who may answer.
		reply(msgNotAuthorized)
		return
	}

	_, err = h.tasks.Complete(ctx, taskID, channelext.Resolution{OptionID: optionID, ActorExternalID: actor})
	switch {
	case err == nil:
	case errors.Is(err, channelext.ErrTaskTerminal):
		reply(msgAlreadyDone)
		return
	case errors.Is(err, channelext.ErrTaskNotFound):
		reply(msgRequestUnknown)
		return
	default:
		log.Printf("slack-v2: task %s: complete: %v", taskID, err)
		reply(msgCannotRecord)
		return
	}

	if err := h.plugin.markResolved(ctx, taskID, resolvedNote(ref, optionID, actor)); err != nil {
		// The answer is recorded; only the cosmetic update failed.
		log.Printf("slack-v2: task %s: resolved summary: %v", taskID, err)
	}
}

// resolvedNote says what was chosen and by whom. The label is server-supplied
// and is escaped; the actor is a Slack user ID, so a mention is safe, but only
// when it is shaped like one.
func resolvedNote(ref messageRef, optionID, actor string) string {
	note := ":white_check_mark: Resolved: *" + escapeMrkdwn(ref.label(optionID)) + "*"
	if userIDPattern.MatchString(actor) {
		note += " by <@" + actor + ">"
	}
	return note
}

func (h *clickHandler) ephemeral(ctx context.Context, channel, user, text string) {
	sc, err := h.plugin.slack.client(ctx)
	if err != nil {
		log.Printf("slack-v2: ephemeral reply: %v", err)
		return
	}
	if _, err := sc.PostEphemeralContext(ctx, channel, user, slack.MsgOptionText(text, false)); err != nil {
		h.plugin.reportChannelFailure(ctx, "post_ephemeral", err)
		log.Printf("slack-v2: ephemeral reply: %v", err)
	}
}
