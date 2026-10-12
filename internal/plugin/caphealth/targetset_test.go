package caphealth

import (
	"context"
	"testing"
)

func TestTargetSet_ReplaceAndCopy(t *testing.T) {
	var s TargetSet
	got, err := s.ProbeTargets(context.Background())
	if err != nil || len(got) != 0 {
		t.Fatalf("empty set = %v, %v; want none", got, err)
	}

	in := []Target{{InstanceID: "a", ContainerID: "c1"}}
	s.SetTargets(in)
	in[0].InstanceID = "mutated"

	got, _ = s.ProbeTargets(context.Background())
	if len(got) != 1 || got[0].InstanceID != "a" {
		t.Fatalf("set aliased caller slice: %+v", got)
	}
	got[0].InstanceID = "mutated"
	again, _ := s.ProbeTargets(context.Background())
	if again[0].InstanceID != "a" {
		t.Fatalf("returned slice aliased internal state: %+v", again)
	}

	s.SetTargets(nil)
	got, _ = s.ProbeTargets(context.Background())
	if len(got) != 0 {
		t.Fatalf("after clearing: %+v", got)
	}
}
