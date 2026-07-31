package policy

import (
	"context"
	"errors"
	"fmt"

	"github.com/felag-engineering/gleipnir/internal/model"
	"github.com/felag-engineering/gleipnir/internal/plugin/configvalidate"
)

// InstanceManifestResolver resolves a plugin instance name to its instance ID.
// The caller then passes the ID to Snapshotter.ForInstanceID to load the manifest.
// Keeping the interface minimal (name→ID only) makes it easy to fake in tests.
type InstanceManifestResolver interface {
	ResolveInstanceByName(ctx context.Context, name string) (instanceID string, err error)
}

// SubscribedBindingValidator performs I/O-touching validation for subscribed
// triggers: it resolves the source instance, checks that the plugin provides a
// TriggerService, verifies the event_kind is declared, and validates the binding
// block against the manifest's binding_schema.
//
// Validation is BLOCKING — a non-existent plugin reference can never produce a
// run, so we reject at save time (422) rather than warning.
type SubscribedBindingValidator struct {
	resolver InstanceManifestResolver
	snap     *configvalidate.Snapshotter
}

// NewSubscribedBindingValidator returns a SubscribedBindingValidator backed by
// the given resolver and snapshotter.
func NewSubscribedBindingValidator(resolver InstanceManifestResolver, snap *configvalidate.Snapshotter) *SubscribedBindingValidator {
	return &SubscribedBindingValidator{resolver: resolver, snap: snap}
}

// Validate checks the subscribed-trigger fields in t. Returns nil when t is not
// a subscribed trigger (fast-path skip) or when all checks pass. Returns one or
// more Issues on failure.
func (v *SubscribedBindingValidator) Validate(ctx context.Context, t model.TriggerConfig) []Issue {
	if t.Type != model.TriggerTypeSubscribed {
		return nil
	}

	var issues []Issue
	add := func(field, format string, args ...any) {
		issues = append(issues, Issue{Field: field, Message: fmt.Sprintf(format, args...)})
	}

	// Step 1: resolve the named instance to an ID.
	instanceID, err := v.resolver.ResolveInstanceByName(ctx, t.Source)
	if err != nil {
		add("trigger.source", "no plugin instance named %q", t.Source)
		return issues
	}

	// Step 2: fetch the manifest for that instance.
	m, err := v.snap.ForInstanceID(ctx, instanceID)
	if err != nil {
		add("trigger.source", "could not load manifest for instance %q: %v", t.Source, err)
		return issues
	}

	// Step 3: verify the plugin provides a TriggerService.
	if m.Services.Trigger == "" || len(m.EventKinds) == 0 {
		add("trigger.source", "plugin instance %q does not provide a TriggerService", t.Source)
		return issues
	}

	// Step 4: compile the binding validator for the named event kind.
	bv, err := configvalidate.ForTriggerBinding(m, t.EventKind)
	if err != nil {
		if errors.Is(err, configvalidate.ErrEventKindNotFound) {
			add("trigger.event_kind", "event_kind %q not declared by plugin instance %q", t.EventKind, t.Source)
		} else {
			add("trigger.event_kind", "could not compile binding schema for event_kind %q: %v", t.EventKind, err)
		}
		return issues
	}

	// Step 5: validate the binding block against the schema (nil binding is
	// treated as an empty map — the schema decides whether that is valid).
	// The nil check must be on the concrete map, BEFORE boxing into any: a
	// nil map wrapped in an interface is non-nil, so the previous
	// `any(t.Binding) == nil` check never fired (staticcheck SA4023). The
	// jsonschema validator happens to treat a nil map as an empty object, so
	// behavior was unchanged — but the normalization the comment promises now
	// actually runs rather than relying on that library quirk.
	var binding any = t.Binding
	if t.Binding == nil {
		binding = map[string]any{}
	}
	fieldErrs, err := bv.Validate(binding)
	if err != nil {
		add("trigger.binding", "binding validation error: %v", err)
		return issues
	}
	for _, fe := range fieldErrs {
		field := "trigger.binding"
		if fe.Field != "" {
			field = "trigger.binding." + fe.Field
		}
		issues = append(issues, Issue{Field: field, Message: fe.Message})
	}

	return issues
}
