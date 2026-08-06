package resources

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/felag-engineering/gleipnir/internal/plugin/container"
)

// errRequired keeps the constructor messages uniform.
func errRequired(field string) error {
	return fmt.Errorf("resources: %s is required", field)
}

// OOMReporter records that a container was killed for exceeding its memory
// cap. Both destinations matter and neither substitutes for the other: the
// health fault narrows routing away from an instance that cannot serve, and the
// audit event is what an operator reads afterwards to find out why.
type OOMReporter interface {
	// CapabilityFault marks the instance unhealthy with a detail naming the
	// cause (#814's per-capability health).
	CapabilityFault(ctx context.Context, instanceID, detail string)

	// Audited records the operator-visible event.
	Audited(ctx context.Context, instanceID string, limitBytes int64)
}

// OOMDetail is the health detail an OOM produces.
//
// It names the limit because the fix is almost always "raise it", and a detail
// saying only "out of memory" sends an operator to look at the plugin's code
// when the answer is a number in their own configuration.
func OOMDetail(limitBytes int64) string {
	if limitBytes <= 0 {
		return "container was killed for exceeding its memory limit"
	}
	return fmt.Sprintf("container was killed for exceeding its %d MiB memory limit", limitBytes>>20)
}

// ObserveExit inspects a container's terminal state and reports an OOM kill.
//
// It reports only on the OOM flag, not on "exited with a non-zero code": a
// plugin that crashes on its own is a different fault with a different fix, and
// conflating the two would send an operator to raise a memory limit that was
// never the problem.
func ObserveExit(ctx context.Context, reporter OOMReporter, instanceID string, info container.ContainerInfo, limitBytes int64) bool {
	if !info.OOMKilled {
		return false
	}

	detail := OOMDetail(limitBytes)
	slog.WarnContext(ctx, "plugin container was OOM-killed",
		"instance_id", instanceID, "container_id", string(info.ID), "limit_bytes", limitBytes)
	oomKills.Inc()

	if reporter != nil {
		reporter.CapabilityFault(ctx, instanceID, detail)
		reporter.Audited(ctx, instanceID, limitBytes)
	}
	return true
}
