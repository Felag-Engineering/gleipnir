package cmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestValidateImageRef(t *testing.T) {
	tests := []struct {
		name    string
		ref     string
		wantErr bool
	}{
		{name: "ordinary ref", ref: "ghcr.io/acme/plugin:1.0.0"},
		{name: "ordinary ref with digest", ref: "ghcr.io/acme/plugin@sha256:" + strings.Repeat("a", 64)},
		{name: "leading dash looks like a short flag", ref: "-rf", wantErr: true},
		{name: "leading dash looks like a long flag", ref: "--rm", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateImageRef(tt.ref)
			if tt.wantErr && err == nil {
				t.Fatalf("validateImageRef(%q): expected an error, got nil", tt.ref)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("validateImageRef(%q): unexpected error: %v", tt.ref, err)
			}
		})
	}
}

func TestRepositoryOf(t *testing.T) {
	tests := []struct {
		name string
		ref  string
		want string
	}{
		{name: "repo and tag", ref: "acme/plugin:1.0.0", want: "acme/plugin"},
		{name: "no tag", ref: "acme/plugin", want: "acme/plugin"},
		{
			// A registry host carrying a port must not be mistaken for a tag
			// separator — the colon that matters is the one after the last '/'.
			name: "registry host with a port",
			ref:  "registry.example.com:5000/acme/plugin:1.0.0",
			want: "registry.example.com:5000/acme/plugin",
		},
		{
			name: "registry host with a port, no tag",
			ref:  "registry.example.com:5000/acme/plugin",
			want: "registry.example.com:5000/acme/plugin",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := repositoryOf(tt.ref); got != tt.want {
				t.Errorf("repositoryOf(%q) = %q, want %q", tt.ref, got, tt.want)
			}
		})
	}
}

func TestWarnOnUnexpectedTags(t *testing.T) {
	fakeCmd := &cobra.Command{}
	fakeCmd.SetOut(&bytes.Buffer{})
	var errOut bytes.Buffer
	fakeCmd.SetErr(&errOut)

	warnOnUnexpectedTags(fakeCmd, []string{"acme/plugin:1.0.0", "acme/other:latest"}, "acme/plugin")

	got := errOut.String()
	if strings.Contains(got, "acme/plugin:1.0.0") {
		t.Errorf("warned about a tag matching the manifest repository: %q", got)
	}
	if !strings.Contains(got, "acme/other:latest") {
		t.Errorf("expected a warning naming the mismatched tag, got: %q", got)
	}
	if !strings.Contains(got, "acme/plugin") {
		t.Errorf("expected the warning to name the manifest repository, got: %q", got)
	}
}

// TestWarnOnUnexpectedTags_BareRefNameSuppressedByContainerdImageName proves a
// bare org.opencontainers.image.ref.name value like "latest" — which carries
// no repository information at all — is not warned about once a
// fully-qualified tag (typically io.containerd.image.name) in the same set
// already confirms the manifest's repository.
func TestWarnOnUnexpectedTags_BareRefNameSuppressedByContainerdImageName(t *testing.T) {
	fakeCmd := &cobra.Command{}
	fakeCmd.SetOut(&bytes.Buffer{})
	var errOut bytes.Buffer
	fakeCmd.SetErr(&errOut)

	warnOnUnexpectedTags(fakeCmd, []string{"ghcr.io/acme/plugin:1.0.0", "latest"}, "ghcr.io/acme/plugin")

	if got := errOut.String(); got != "" {
		t.Errorf("expected no warning (containerd image name confirms the repository), got: %q", got)
	}
}

// TestWarnOnUnexpectedTags_BareTagWarnedWithoutConfirmation proves the
// suppression above does not go the other way: a bare tag with no
// corroborating qualified tag is still compared and can still warn.
func TestWarnOnUnexpectedTags_BareTagWarnedWithoutConfirmation(t *testing.T) {
	fakeCmd := &cobra.Command{}
	fakeCmd.SetOut(&bytes.Buffer{})
	var errOut bytes.Buffer
	fakeCmd.SetErr(&errOut)

	warnOnUnexpectedTags(fakeCmd, []string{"latest"}, "ghcr.io/acme/plugin")

	if !strings.Contains(errOut.String(), "latest") {
		t.Errorf("expected a warning for an unconfirmed bare tag, got: %q", errOut.String())
	}
}

func TestWarnIfBundleExceedsHostExtractionCap(t *testing.T) {
	fakeCmd := &cobra.Command{}
	fakeCmd.SetOut(&bytes.Buffer{})
	var errOut bytes.Buffer
	fakeCmd.SetErr(&errOut)

	// A small cap stands in for the real one: materializing 1 GiB to cross it
	// would test nothing extra.
	const testCap = 4096

	small := []bundleTarEntry{{"image.tar", 0o644, make([]byte, 1024)}}
	warnIfBundleExceedsHostExtractionCap(fakeCmd, small, testCap)
	if errOut.Len() != 0 {
		t.Errorf("expected no warning for a small bundle, got: %q", errOut.String())
	}

	large := []bundleTarEntry{{"image.tar", 0o644, make([]byte, testCap+1)}}
	warnIfBundleExceedsHostExtractionCap(fakeCmd, large, testCap)
	if !strings.Contains(errOut.String(), "extraction cap") {
		t.Errorf("expected an extraction-cap warning for an oversized bundle, got: %q", errOut.String())
	}
}
