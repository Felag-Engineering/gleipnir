//go:build dood && substratev2

package dood_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/model"
)

// eventsEmitCount is STUB_EMIT_COUNT in the dood-events scenario bundle
// (scripts/build-conformance-stub.sh).
const eventsEmitCount = 5

// stopAfter is how many runs complete before the container is stopped. The
// scenario emits a tick every 2s, so the stop lands well before the next one.
const stopAfter = 3

// eventsBudget bounds each wait in the test. It covers a container restart
// (the reconciler's first restart backoff is 5s) plus the events supervisor
// reconnecting, with room to spare.
const eventsBudget = 2 * time.Minute

func onTickPolicyYAML(source string) string {
	// queue rather than the default skip: the ticks are seconds apart and a run
	// is quicker than that, but a skip would turn a slow run into a missing
	// launch and the count below would blame the events path for it.
	return fmt.Sprintf(`name: dood-on-tick
trigger:
  type: subscribed
  source: %s
  event_kind: conformance.tick
agent:
  task: acknowledge the tick
  model:
    provider: %s
    name: %s
  concurrency: queue
`, source, policyProvider, policyModel)
}

// TestDooD_EventsRedeliveredAcrossContainerRestart stops a plugin container in
// the middle of its event stream and checks that every event still launches
// exactly one run: none lost, none repeated.
//
// After the stop the reconciler restarts the container, the stub resumes from
// its durable progress, and the events supervisor reconnects with the cursor it
// persisted before the stop. The durable event store on the plugin side serves
// what was published while the host was not listening, so the run count lands
// on exactly the number of events emitted.
func TestDooD_EventsRedeliveredAcrossContainerRestart(t *testing.T) {
	h := newHarness(t)

	pluginID := h.InstallBundle("dood-events")
	instanceID := h.CreateInstance(pluginID, "dood-ev")
	h.InsertPolicy("pol-tick", "dood-on-tick", string(model.TriggerTypeSubscribed), onTickPolicyYAML("dood-ev"))

	h.WaitRunning(instanceID)

	h.WaitRuns("pol-tick", eventsBudget, func(runs []db.ListRunsByPolicyRow) string {
		if n := countStatus(runs, model.RunStatusComplete); n < stopAfter {
			return fmt.Sprintf("%d of %d runs complete", n, stopAfter)
		}
		return ""
	})
	stopped := h.StopPluginContainer(instanceID)
	t.Logf("stopped plugin container %s after %d runs", stopped, stopAfter)

	runs := h.WaitRuns("pol-tick", eventsBudget, func(runs []db.ListRunsByPolicyRow) string {
		if n := countStatus(runs, model.RunStatusComplete); n < eventsEmitCount {
			return fmt.Sprintf("%d of %d runs complete", n, eventsEmitCount)
		}
		return ""
	})

	// The restart must actually have happened, or this is the uninterrupted
	// case and proves nothing about redelivery.
	if info, ok := h.runningContainer(t.Context(), instanceID); !ok {
		t.Errorf("instance %s has no running container after the restart", instanceID)
	} else if info.ID != stopped {
		t.Logf("the restart replaced the container (%s -> %s)", stopped, info.ID)
	}

	if len(runs) != eventsEmitCount {
		t.Errorf("%d runs launched for %d events, want exactly one each: %s",
			len(runs), eventsEmitCount, describeRuns(runs))
	}

	// One run per tick, and each tick exactly once: a count of five could also
	// be four ticks with one delivered twice.
	labels := map[string]int{}
	for _, r := range runs {
		run, err := h.q.GetRun(t.Context(), r.ID)
		if err != nil {
			t.Fatalf("GetRun %s: %v", r.ID, err)
		}
		labels[tickLabel(t, run.TriggerPayload)]++
	}
	for i := 1; i <= eventsEmitCount; i++ {
		label := fmt.Sprintf("tick-%d", i)
		if labels[label] != 1 {
			t.Errorf("%s launched %d runs, want 1 (all labels: %v)", label, labels[label], labels)
		}
	}
}

// tickLabel extracts the tick's label from a run's trigger payload. The payload
// is the event envelope the dispatcher stored, so the label is found by name
// rather than by assuming the envelope's exact shape.
func tickLabel(t *testing.T, payload string) string {
	t.Helper()
	i := strings.Index(payload, `"tick-`)
	if i < 0 {
		t.Fatalf("trigger payload %s carries no tick label", payload)
	}
	rest := payload[i+1:]
	return rest[:strings.Index(rest, `"`)]
}
