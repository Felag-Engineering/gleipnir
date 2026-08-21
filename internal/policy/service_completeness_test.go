package policy

import (
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/settings"
)

// TestRequireCompleteCoversEveryCollaborator is the guard that gives
// RequireComplete its value.
//
// RequireComplete is an explicit list of nil checks, which is readable but
// forgettable — and forgetting is the entire failure mode it exists to prevent
// (#788, #870). So this test walks Service by reflection and fails if any field
// is neither reported as missing by a zero-valued Service nor recorded in
// collaboratorsExemptFromCompleteness with a reason.
//
// Adding a collaborator to Service therefore forces a decision about it: check
// it, or write down why it does not need checking. Neither can be skipped.
func TestRequireCompleteCoversEveryCollaborator(t *testing.T) {
	var zero Service
	reported := make(map[string]bool)
	for _, name := range zero.missingCollaborators() {
		reported[name] = true
	}

	st := reflect.TypeOf(Service{})
	var uncovered []string
	for i := 0; i < st.NumField(); i++ {
		name := st.Field(i).Name
		if reported[name] {
			continue
		}
		if reason, ok := collaboratorsExemptFromCompleteness[name]; ok {
			if strings.TrimSpace(reason) == "" {
				t.Errorf("field %q is exempt from the completeness check with an empty reason; state why a nil value is safe", name)
			}
			continue
		}
		uncovered = append(uncovered, name)
	}

	if len(uncovered) > 0 {
		sort.Strings(uncovered)
		t.Fatalf("policy.Service fields not covered by RequireComplete: %s\n"+
			"Add a nil check to missingCollaborators, or add the field to "+
			"collaboratorsExemptFromCompleteness with the reason a nil value is safe.",
			strings.Join(uncovered, ", "))
	}
}

// TestExemptCollaboratorsAreRealFields keeps the exempt map from rotting into a
// list of names that no longer exist — an exemption for a deleted field reads
// as coverage while covering nothing.
func TestExemptCollaboratorsAreRealFields(t *testing.T) {
	st := reflect.TypeOf(Service{})
	for name := range collaboratorsExemptFromCompleteness {
		if _, ok := st.FieldByName(name); !ok {
			t.Errorf("collaboratorsExemptFromCompleteness names %q, which is not a field of Service", name)
		}
	}
}

func TestRequireComplete(t *testing.T) {
	t.Run("a zero service names every missing collaborator", func(t *testing.T) {
		var s Service
		err := s.RequireComplete()
		if err == nil {
			t.Fatal("RequireComplete() = nil for a zero Service; want an error")
		}
		// The message is what an operator sees when the process refuses to
		// start, so it has to name the fields rather than just report a count.
		for _, want := range []string{"store", "lookup", "modelValidator", "optionsValidator", "settings", "subscribedValidator"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error does not name %q: %v", want, err)
			}
		}
	})

	t.Run("a fully wired service is complete", func(t *testing.T) {
		// Only presence matters here, so the cheapest non-nil value of each
		// type is the honest one — a real collaborator would suggest this
		// asserts behaviour, which it does not.
		s := &Service{
			store:               &db.Store{},
			lookup:              &stubLookup{},
			modelValidator:      &stubModelValidator{},
			optionsValidator:    &stubOptionsValidator{},
			settings:            &settings.Service{},
			subscribedValidator: NewSubscribedBindingValidator(&fakeResolver{}, nil),
		}
		if err := s.RequireComplete(); err != nil {
			t.Fatalf("RequireComplete() = %v, want nil", err)
		}
	})

	t.Run("the encrypter is exempt, so its absence does not block startup", func(t *testing.T) {
		// Pinning the one deliberate exemption: a deployment with no
		// GLEIPNIR_ENCRYPTION_KEY must still start.
		if _, ok := collaboratorsExemptFromCompleteness["encrypter"]; !ok {
			t.Fatal("encrypter is no longer exempt; a keyless deployment would now fail to start")
		}
	})
}
