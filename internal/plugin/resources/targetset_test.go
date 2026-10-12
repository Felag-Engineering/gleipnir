package resources

import (
	"context"
	"testing"
)

func TestTargetSet_ReplaceAndCopy(t *testing.T) {
	var s TargetSet
	in := []Target{{InstanceID: "a", ContainerID: "c1"}}
	s.SetTargets(in)
	in[0].InstanceID = "mutated"

	got, err := s.StatsTargets(context.Background())
	if err != nil || len(got) != 1 || got[0].InstanceID != "a" {
		t.Fatalf("StatsTargets = %+v, %v; want the original copy", got, err)
	}

	s.SetTargets(nil)
	got, _ = s.StatsTargets(context.Background())
	if len(got) != 0 {
		t.Fatalf("after clearing: %+v", got)
	}
}
