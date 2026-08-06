// Package resources turns declared and operator-set resource limits into the
// enforced cgroup caps a container is created with, and reports what the
// running containers actually consume (ADR-056 spec §7; issue #815).
//
// The shift this package embodies: v1.1 SAMPLED RSS from /proc and alerted on
// it, which is observation. A cgroup cap is enforcement — the kernel refuses
// the allocation rather than Gleipnir noticing afterwards that it happened.
// The sampler's reporting role is served here by container stats, so the
// numbers an operator reads keep coming from one place after the cutover.
package resources

import "fmt"

// Defaults applied when neither the manifest nor an admin says otherwise.
//
// A default of "no limit" would be the wrong choice: an unlimited container on
// a homelab host is one plugin away from an OOM that takes Gleipnir with it,
// and the whole point of the shift from sampling to caps is that the failure
// lands on the plugin rather than the host. These are generous enough that a
// well-behaved plugin never notices and tight enough that a runaway one dies
// first.
const (
	DefaultMemoryBytes    int64 = 256 << 20 // 256 MiB
	DefaultCPUMillicores  int64 = 500       // half a core
	nanoCPUsPerMillicore  int64 = 1_000_000
	minEnforceableMemory  int64 = 6 << 20 // 6 MiB — below this the runtime refuses
	maxPlausibleMemoryMiB int64 = 1 << 20 // 1 TiB, expressed in MiB
)

// Limits is one resource envelope. A zero field means "not specified at this
// layer" — which is different from "no limit", and the distinction is what
// makes the precedence chain expressible.
type Limits struct {
	MemoryBytes   int64
	CPUMillicores int64
}

// Specified reports whether this layer said anything at all.
func (l Limits) Specified() bool {
	return l.MemoryBytes > 0 || l.CPUMillicores > 0
}

// Source names which layer produced an effective value. Recorded on the audit
// event, because "why is this plugin capped at 128 MiB" has three possible
// answers and an operator should not have to guess which one applied.
type Source string

const (
	// SourceDefault — nobody specified, the host default applied.
	SourceDefault Source = "default"

	// SourceManifest — the plugin author declared it and an admin consented at
	// review time.
	SourceManifest Source = "manifest"

	// SourceOverride — an admin set it on this instance, overriding whatever
	// the manifest asked for.
	SourceOverride Source = "admin_override"
)

// Effective is the resolved envelope plus where each value came from.
type Effective struct {
	MemoryBytes   int64
	CPUMillicores int64

	MemorySource Source
	CPUSource    Source
}

// NanoCPUs renders the CPU cap in the units the runtime API speaks
// (1e9 == one core).
func (e Effective) NanoCPUs() int64 { return e.CPUMillicores * nanoCPUsPerMillicore }

// Resolve applies the precedence chain: admin override, then manifest, then
// host default.
//
// The override wins because an operator running the host has better
// information than a plugin author who has never seen it — the manifest value
// is the author's estimate, and the override is the person who will be paged.
//
// Resolution is PER FIELD rather than per layer. An admin who caps memory
// without an opinion about CPU should not silently discard the author's CPU
// figure and fall all the way back to the default; each number is a separate
// judgement.
func Resolve(manifest, override Limits) Effective {
	out := Effective{
		MemoryBytes:   DefaultMemoryBytes,
		CPUMillicores: DefaultCPUMillicores,
		MemorySource:  SourceDefault,
		CPUSource:     SourceDefault,
	}

	if manifest.MemoryBytes > 0 {
		out.MemoryBytes = manifest.MemoryBytes
		out.MemorySource = SourceManifest
	}
	if manifest.CPUMillicores > 0 {
		out.CPUMillicores = manifest.CPUMillicores
		out.CPUSource = SourceManifest
	}

	if override.MemoryBytes > 0 {
		out.MemoryBytes = override.MemoryBytes
		out.MemorySource = SourceOverride
	}
	if override.CPUMillicores > 0 {
		out.CPUMillicores = override.CPUMillicores
		out.CPUSource = SourceOverride
	}
	return out
}

// FromManifestMiB converts the manifest's units (MiB, millicores) into Limits.
//
// A value the runtime would reject is an error rather than something to clamp:
// a manifest asking for 1 MiB is a manifest with a typo, and silently raising
// it to something workable would make the consent screen say one thing and the
// container do another.
func FromManifestMiB(memoryMiB, cpuMillicores int) (Limits, error) {
	var out Limits

	if memoryMiB < 0 || cpuMillicores < 0 {
		return Limits{}, fmt.Errorf("resources: limits must not be negative (memory_mb=%d, cpu_millicores=%d)", memoryMiB, cpuMillicores)
	}
	if memoryMiB > 0 {
		if int64(memoryMiB) > maxPlausibleMemoryMiB {
			return Limits{}, fmt.Errorf("resources: memory_mb=%d exceeds the plausible ceiling", memoryMiB)
		}
		bytes := int64(memoryMiB) << 20
		if bytes < minEnforceableMemory {
			return Limits{}, fmt.Errorf("resources: memory_mb=%d is below the %d MiB the runtime can enforce",
				memoryMiB, minEnforceableMemory>>20)
		}
		out.MemoryBytes = bytes
	}
	out.CPUMillicores = int64(cpuMillicores)
	return out, nil
}
