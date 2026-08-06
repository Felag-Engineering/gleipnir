package resources

import (
	"github.com/felag-engineering/gleipnir/internal/infra/metrics"
	"github.com/prometheus/client_golang/prometheus"
)

// Container-stats gauges. Labels match the v1.1 RSS gauge's (plugin, instance)
// so a dashboard built against the sampler keeps working across the cutover —
// the number's source changes, the series does not.
var (
	memoryUsage = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "gleipnir_plugin_container_memory_bytes",
		Help: "Plugin container memory usage in bytes, from the runtime's stats API.",
	}, []string{"plugin", "instance"})

	memoryLimit = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "gleipnir_plugin_container_memory_limit_bytes",
		Help: "Plugin container enforced cgroup memory cap in bytes.",
	}, []string{"plugin", "instance"})

	cpuPercent = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "gleipnir_plugin_container_cpu_percent",
		Help: "Plugin container CPU usage as a percentage of one core.",
	}, []string{"plugin", "instance"})

	// An OOM kill is the cap doing its job, but it is also the signal that a
	// limit is wrong. Counted so "how often" is answerable without grepping.
	oomKills = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "gleipnir_plugin_container_oom_kills_total",
		Help: "Plugin containers killed by the kernel for exceeding their memory cap.",
	})
)

func init() {
	metrics.Registry().MustRegister(memoryUsage, memoryLimit, cpuPercent, oomKills)
}
