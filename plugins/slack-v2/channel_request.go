package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"unicode/utf8"

	"github.com/felag-engineering/gleipnir/plugin-sdk/channelext"
	"github.com/slack-go/slack"
)

const (
	// Slack's documented limits for the pieces a request message is built from.
	maxActionIDLen    = 255
	maxButtonLabelLen = 75
	maxSectionTextLen = 3000
	maxButtons        = 25

	kindPermission = "permission"
)

// The channel contract gives option ids no meaning, so these conventional ids
// are used only to pick a button colour, never to decide an outcome.
var (
	approveOptionIDs = map[string]bool{"approve": true, "allow": true, "accept": true}
	refuseOptionIDs  = map[string]bool{"reject": true, "deny": true, "decline": true}
)

// messageRef is what the plugin must remember about the Slack message it
// posted for a task, to retract or summarize it later. channelext.Task has no
// metadata field, so it is kept in a sidecar file keyed by task ID, in the same
// volume as the task store and written with the same temp-file-and-rename
// discipline.
type messageRef struct {
	Channel string      `json:"channel"`
	TS      string      `json:"ts"`
	Message string      `json:"message"`
	Kind    string      `json:"kind,omitempty"`
	Options []refOption `json:"options"`
}

type refOption struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// label returns the display label of an option, or its id when unknown.
func (r messageRef) label(optionID string) string {
	for _, o := range r.Options {
		if o.ID == optionID {
			return o.Label
		}
	}
	return optionID
}

func (r messageRef) hasOption(optionID string) bool {
	for _, o := range r.Options {
		if o.ID == optionID {
			return true
		}
	}
	return false
}

type messageRefStore struct{ dir string }

func newMessageRefStore(dir string) (*messageRefStore, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("message ref store: create %s: %w", dir, err)
	}
	return &messageRefStore{dir: dir}, nil
}

var errRefNotFound = errors.New("no message recorded for task")

// path only accepts the 32-hex shape channelext task IDs have, so a hostile ID
// can never name a file outside dir.
func (s *messageRefStore) path(taskID string) (string, error) {
	if len(taskID) != 32 {
		return "", fmt.Errorf("task id %q is malformed", taskID)
	}
	for _, c := range taskID {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return "", fmt.Errorf("task id %q is malformed", taskID)
		}
	}
	return filepath.Join(s.dir, taskID+".json"), nil
}

func (s *messageRefStore) save(taskID string, ref messageRef) error {
	path, err := s.path(taskID)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(ref)
	if err != nil {
		return fmt.Errorf("encode message ref: %w", err)
	}
	tmp, err := os.CreateTemp(s.dir, ".ref-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("write message ref: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("fsync message ref: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("close message ref: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("rename message ref: %w", err)
	}
	return nil
}

func (s *messageRefStore) load(taskID string) (messageRef, error) {
	path, err := s.path(taskID)
	if err != nil {
		return messageRef{}, err
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return messageRef{}, fmt.Errorf("task %q: %w", taskID, errRefNotFound)
	}
	if err != nil {
		return messageRef{}, fmt.Errorf("read message ref: %w", err)
	}
	var ref messageRef
	if err := json.Unmarshal(raw, &ref); err != nil {
		return messageRef{}, fmt.Errorf("decode message ref: %w", err)
	}
	return ref, nil
}

// truncateRunes shortens s to at most max runes, ending in an ellipsis.
func truncateRunes(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	runes := []rune(s)
	return string(runes[:max-1]) + "…"
}

// request is the channelext.RequestFunc: it posts the question as one button
// per option and records where, then returns. The answer arrives later as a
// click (channel_click.go) completing the task.
func (p *plugin) request(ctx context.Context, taskID string, req channelext.Request) error {
	if len(req.Options) == 0 {
		// Form asks in Slack are post-alpha. Failing the call lets the
		// audience fall through to the next channel (in-app can render a form).
		return errors.New("slack-v2 cannot render a form (requestedSchema) request; only pick-one options are supported")
	}
	if err := validateOptions(taskID, req.Options); err != nil {
		return err
	}
	channel, err := p.resolveTarget(ctx, req.Target)
	if err != nil {
		return err
	}
	sc, err := p.slack.client(ctx)
	if err != nil {
		return err
	}

	postedChannel, ts, err := sc.PostMessageContext(ctx, channel,
		slack.MsgOptionText(truncateRunes(escapeMrkdwn(req.Message), maxSectionTextLen), false),
		slack.MsgOptionBlocks(requestBlocks(taskID, req)...),
		slack.MsgOptionParse(false),
		slack.MsgOptionLinkNames(false),
		slack.MsgOptionDisableLinkUnfurl(),
		slack.MsgOptionDisableMediaUnfurl(),
	)
	if err != nil {
		p.reportChannelFailure(ctx, "post_message", err)
		return fmt.Errorf("post request: %w", err)
	}

	ref := messageRef{Channel: postedChannel, TS: ts, Message: req.Message, Kind: req.Kind}
	for _, o := range req.Options {
		ref.Options = append(ref.Options, refOption{ID: o.ID, Label: o.Label})
	}
	if err := p.refs.save(taskID, ref); err != nil {
		return fmt.Errorf("record posted message: %w", err)
	}
	return nil
}

// validateOptions refuses options Slack would reject, before anything is posted.
func validateOptions(taskID string, options []channelext.RequestOption) error {
	if len(options) > maxButtons {
		return fmt.Errorf("%d options exceeds Slack's limit of %d buttons", len(options), maxButtons)
	}
	for _, o := range options {
		if n := len(actionIDFor(taskID, o.ID)); n > maxActionIDLen {
			return fmt.Errorf("option id %q is too long for a Slack action id (%d > %d bytes)", o.ID, n, maxActionIDLen)
		}
	}
	return nil
}

// requestBlocks lays out the question and its buttons. The message is
// untrusted text and is escaped exactly as channel/notify escapes it.
func requestBlocks(taskID string, req channelext.Request) []slack.Block {
	text := escapeMrkdwn(req.Message)
	if req.Kind == kindPermission {
		text = ":lock: *Permission requested*\n" + text
	}
	buttons := make([]slack.BlockElement, 0, len(req.Options))
	for _, o := range req.Options {
		label := o.Label
		if label == "" {
			label = o.ID
		}
		btn := slack.NewButtonBlockElement(
			actionIDFor(taskID, o.ID),
			o.ID,
			slack.NewTextBlockObject(slack.PlainTextType, truncateRunes(label, maxButtonLabelLen), false, false),
		)
		// Colour is only a hint, and only for a permission ask, where the
		// stakes make the distinction worth drawing.
		if req.Kind == kindPermission {
			switch {
			case approveOptionIDs[o.ID]:
				btn.Style = slack.StylePrimary
			case refuseOptionIDs[o.ID]:
				btn.Style = slack.StyleDanger
			}
		}
		buttons = append(buttons, btn)
	}
	return []slack.Block{
		slack.NewSectionBlock(slack.NewTextBlockObject(slack.MarkdownType, truncateRunes(text, maxSectionTextLen), false, false), nil, nil),
		slack.NewActionBlock("", buttons...),
	}
}

// resolvedBlocks is the message after the question is settled: the original
// text, no actions. note is plugin-authored mrkdwn; it must not carry
// untrusted text unescaped.
func resolvedBlocks(ref messageRef, note string) []slack.Block {
	return []slack.Block{
		slack.NewSectionBlock(slack.NewTextBlockObject(slack.MarkdownType, truncateRunes(escapeMrkdwn(ref.Message), maxSectionTextLen), false, false), nil, nil),
		slack.NewContextBlock("", slack.NewTextBlockObject(slack.MarkdownType, note, false, false)),
	}
}

// markResolved replaces the task's message with a resolved summary and no
// buttons. It is idempotent: chat.update to identical content is a no-op, so a
// repeated cancel, or a click racing a cancel, cannot leave a different result.
func (p *plugin) markResolved(ctx context.Context, taskID, note string) error {
	ref, err := p.refs.load(taskID)
	if err != nil {
		return err
	}
	sc, err := p.slack.client(ctx)
	if err != nil {
		return err
	}
	_, _, _, err = sc.UpdateMessageContext(ctx, ref.Channel, ref.TS,
		slack.MsgOptionText(truncateRunes(escapeMrkdwn(ref.Message), maxSectionTextLen)+"\n"+note, false),
		slack.MsgOptionBlocks(resolvedBlocks(ref, note)...),
		slack.MsgOptionParse(false),
	)
	if err != nil {
		p.reportChannelFailure(ctx, "update_message", err)
		return fmt.Errorf("update message: %w", err)
	}
	return nil
}

const cancelledNote = ":no_entry_sign: _This request was closed and no longer needs an answer._"

// onCancel is the channelext.CancelFunc. The task is already cancelled, so a
// failure here has nothing to undo; it is logged.
func (p *plugin) onCancel(ctx context.Context, taskID, _ string) {
	if err := p.markResolved(ctx, taskID, cancelledNote); err != nil {
		log.Printf("slack-v2: task %s: retract request message: %v", taskID, err)
	}
}

// newChannelExtension builds the io.gleipnir/channel extension: notify, and
// request backed by the durable file task store under dataDir.
func (p *plugin) newChannelExtension(dataDir string) (*channelext.Extension, error) {
	tasks, err := channelext.NewFileTaskStore(filepath.Join(dataDir, "tasks"))
	if err != nil {
		return nil, fmt.Errorf("task store: %w", err)
	}
	refs, err := newMessageRefStore(filepath.Join(dataDir, "messages"))
	if err != nil {
		return nil, err
	}
	p.refs = refs
	return channelext.New(channelDeclaration, p.notify,
		channelext.WithRequest(p.request),
		channelext.WithTaskStore(tasks),
		channelext.WithOnCancel(p.onCancel),
	)
}
