package resources

import "testing"

// The precedence chain, and the reason for it: the manifest value is the
// author's estimate; the override is the person who will be paged.
func TestResolve_Precedence(t *testing.T) {
	tests := []struct {
		name        string
		manifest    Limits
		override    Limits
		wantMemory  int64
		wantCPU     int64
		wantMemFrom Source
		wantCPUFrom Source
	}{
		{
			name:        "nobody specified anything",
			wantMemory:  DefaultMemoryBytes,
			wantCPU:     DefaultCPUMillicores,
			wantMemFrom: SourceDefault,
			wantCPUFrom: SourceDefault,
		},
		{
			name:        "the manifest declared both",
			manifest:    Limits{MemoryBytes: 512 << 20, CPUMillicores: 1000},
			wantMemory:  512 << 20,
			wantCPU:     1000,
			wantMemFrom: SourceManifest,
			wantCPUFrom: SourceManifest,
		},
		{
			name:        "an admin override beats the manifest",
			manifest:    Limits{MemoryBytes: 512 << 20, CPUMillicores: 1000},
			override:    Limits{MemoryBytes: 128 << 20, CPUMillicores: 250},
			wantMemory:  128 << 20,
			wantCPU:     250,
			wantMemFrom: SourceOverride,
			wantCPUFrom: SourceOverride,
		},
		{
			// Per FIELD, not per layer. An admin who caps memory without an
			// opinion about CPU should not silently discard the author's CPU
			// figure and fall all the way back to the default.
			name:        "a partial override keeps the manifest's other value",
			manifest:    Limits{MemoryBytes: 512 << 20, CPUMillicores: 1000},
			override:    Limits{MemoryBytes: 128 << 20},
			wantMemory:  128 << 20,
			wantCPU:     1000,
			wantMemFrom: SourceOverride,
			wantCPUFrom: SourceManifest,
		},
		{
			name:        "an override with no manifest still beats the default",
			override:    Limits{CPUMillicores: 2000},
			wantMemory:  DefaultMemoryBytes,
			wantCPU:     2000,
			wantMemFrom: SourceDefault,
			wantCPUFrom: SourceOverride,
		},
		{
			name:        "a manifest declaring only memory leaves CPU at the default",
			manifest:    Limits{MemoryBytes: 64 << 20},
			wantMemory:  64 << 20,
			wantCPU:     DefaultCPUMillicores,
			wantMemFrom: SourceManifest,
			wantCPUFrom: SourceDefault,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Resolve(tc.manifest, tc.override)
			if got.MemoryBytes != tc.wantMemory {
				t.Errorf("memory = %d, want %d", got.MemoryBytes, tc.wantMemory)
			}
			if got.CPUMillicores != tc.wantCPU {
				t.Errorf("cpu = %d, want %d", got.CPUMillicores, tc.wantCPU)
			}
			// The source is on the record because "why is this plugin capped
			// at 128 MiB" has three possible answers.
			if got.MemorySource != tc.wantMemFrom {
				t.Errorf("memory source = %q, want %q", got.MemorySource, tc.wantMemFrom)
			}
			if got.CPUSource != tc.wantCPUFrom {
				t.Errorf("cpu source = %q, want %q", got.CPUSource, tc.wantCPUFrom)
			}
		})
	}
}

// A default of "no limit" would be the wrong choice: an unlimited container on
// a homelab host is one plugin away from an OOM that takes Gleipnir with it.
func TestResolve_DefaultsAreRealLimits(t *testing.T) {
	got := Resolve(Limits{}, Limits{})
	if got.MemoryBytes <= 0 || got.CPUMillicores <= 0 {
		t.Fatalf("defaults = %+v, want real caps rather than unlimited", got)
	}
}

func TestEffective_NanoCPUs(t *testing.T) {
	tests := []struct {
		millicores int64
		want       int64
	}{
		{1000, 1_000_000_000}, // one core
		{500, 500_000_000},
		{2500, 2_500_000_000},
	}
	for _, tc := range tests {
		got := Effective{CPUMillicores: tc.millicores}.NanoCPUs()
		if got != tc.want {
			t.Errorf("%d millicores = %d nanoCPUs, want %d", tc.millicores, got, tc.want)
		}
	}
}

// A value the runtime would reject is an error rather than something to clamp:
// silently raising a typo'd limit to something workable would make the consent
// screen say one thing and the container do another.
func TestFromManifestMiB(t *testing.T) {
	tests := []struct {
		name       string
		memoryMiB  int
		millicores int
		wantErr    bool
		wantBytes  int64
	}{
		{name: "unspecified", wantBytes: 0},
		{name: "ordinary", memoryMiB: 256, millicores: 500, wantBytes: 256 << 20},
		{name: "cpu only", millicores: 250},
		{name: "below what the runtime can enforce", memoryMiB: 1, wantErr: true},
		{name: "negative memory", memoryMiB: -1, wantErr: true},
		{name: "negative cpu", millicores: -1, wantErr: true},
		{name: "implausibly large", memoryMiB: 1 << 21, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := FromManifestMiB(tc.memoryMiB, tc.millicores)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("FromManifestMiB(%d, %d) accepted an unusable value", tc.memoryMiB, tc.millicores)
				}
				return
			}
			if err != nil {
				t.Fatalf("FromManifestMiB: %v", err)
			}
			if got.MemoryBytes != tc.wantBytes {
				t.Errorf("memory = %d, want %d", got.MemoryBytes, tc.wantBytes)
			}
			if got.CPUMillicores != int64(tc.millicores) {
				t.Errorf("cpu = %d, want %d", got.CPUMillicores, tc.millicores)
			}
		})
	}
}

func TestLimits_Specified(t *testing.T) {
	if (Limits{}).Specified() {
		t.Error("the zero value reports as specified")
	}
	if !(Limits{MemoryBytes: 1}).Specified() {
		t.Error("a memory-only value reports as unspecified")
	}
	if !(Limits{CPUMillicores: 1}).Specified() {
		t.Error("a cpu-only value reports as unspecified")
	}
}
