package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"

	"github.com/felag-engineering/gleipnir/plugin-sdk/imagearchive"
)

// hostExtractionCapBytes mirrors maxTarballBytes in
// internal/plugin/loader/extract.go:23 — the cumulative uncompressed bytes
// the host will extract from one plugin bundle. The SDK cannot import
// internal/*, so this is a hand-kept copy rather than a shared constant: if
// the host's cap ever moves, this comment is the tripwire to update it here
// too.
const hostExtractionCapBytes = 100 << 20 // 100 MiB

// resolveImageArchivePath returns a path to an OCI/Docker image archive on
// disk, either the caller-supplied one or one just produced by shelling out
// to docker/podman save. The returned cleanup removes any temp file this
// function created; it is a no-op for a caller-supplied archive, which is not
// ours to delete.
func resolveImageArchivePath(cmd *cobra.Command, imageArchive, imageRef string) (path string, cleanup func(), err error) {
	if imageArchive != "" {
		return imageArchive, func() {}, nil
	}

	if err := validateImageRef(imageRef); err != nil {
		return "", nil, err
	}

	tmp, err := os.CreateTemp("", "gleipnir-plugin-image-*.tar")
	if err != nil {
		return "", nil, fmt.Errorf("create temp file for image save: %w", err)
	}
	tmpPath := tmp.Name()
	tmp.Close()
	cleanup = func() { os.Remove(tmpPath) }

	cli, err := containerCLI()
	if err != nil {
		cleanup()
		return "", nil, err
	}

	// "--" tells both docker and podman to stop parsing flags, so imageRef is
	// always taken as the positional argument it is — belt-and-suspenders
	// alongside validateImageRef's leading-dash rejection, not a substitute
	// for it.
	save := exec.Command(cli, "save", "-o", tmpPath, "--", imageRef)
	save.Stdout = cmd.OutOrStdout()
	save.Stderr = cmd.ErrOrStderr()
	if err := save.Run(); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("%s save %s: %w", cli, imageRef, err)
	}
	return tmpPath, cleanup, nil
}

// validateImageRef rejects an image reference starting with '-'. Such a value
// shells out to docker/podman as an argument this CLI does not otherwise
// inspect, and a string shaped like a flag being passed to a subprocess is
// exactly the kind of ambiguity worth refusing outright rather than trusting
// "--" (see resolveImageArchivePath) to be enough on its own.
func validateImageRef(ref string) error {
	if strings.HasPrefix(ref, "-") {
		return fmt.Errorf("--image %q looks like a flag, not an image reference (starts with '-')", ref)
	}
	return nil
}

// containerCLI finds a container CLI to shell out to, preferring docker.
func containerCLI() (string, error) {
	for _, name := range []string{"docker", "podman"} {
		if _, err := exec.LookPath(name); err == nil {
			return name, nil
		}
	}
	return "", fmt.Errorf("neither docker nor podman found on PATH; use --image-archive to supply a pre-saved archive instead")
}

// inspectImageArchive computes archiveData's image config digest and embedded
// tags. Callers must pass the exact bytes they intend to sign: computing the
// digest from a path and separately re-reading that path to sign would leave
// a check-then-sign gap if the file changed in between the two reads.
func inspectImageArchive(archiveData []byte) (imagearchive.Info, error) {
	info, err := imagearchive.InspectBytes(archiveData)
	if err != nil {
		return imagearchive.Info{}, fmt.Errorf("inspect image archive: %w", err)
	}
	return info, nil
}

// warnOnUnexpectedTags prints a warning for every archive tag that is not the
// manifest's own repository. This is advisory, not a rejection — policing
// tags against a manifest at the point that matters (install time) is #1032's
// job — but an author who packaged the wrong image by mistake should see it
// before they sign and ship it.
//
// A "bare" tag — one with no '/', so no repository information of its own,
// such as the "latest" that org.opencontainers.image.ref.name frequently
// carries — is not warned about when some OTHER, fully-qualified tag in the
// same set already confirms the manifest's repository (typically
// io.containerd.image.name, which Docker 25+/containerd populate with the
// full reference). A bare tag carries no evidence either way once a
// qualified one has already confirmed the repository; warning about it
// anyway would just be noise. Absent that confirmation, a bare tag is
// compared like any other and can still trigger a warning.
func warnOnUnexpectedTags(cmd *cobra.Command, tags []string, repository string) {
	confirmed := false
	for _, tag := range tags {
		if strings.Contains(tag, "/") && repositoryOf(tag) == repository {
			confirmed = true
			break
		}
	}

	for _, tag := range tags {
		if repositoryOf(tag) == repository {
			continue
		}
		if confirmed && !strings.Contains(tag, "/") {
			continue
		}
		fmt.Fprintf(cmd.ErrOrStderr(), "WARNING: image archive is tagged %q, which does not match manifest repository %q\n", tag, repository)
	}
}

// repositoryOf strips the ":tag" suffix off a "repo[:tag]" reference. It
// looks for the colon only after the last '/', since a registry host may
// itself carry a port ("registry.example.com:5000/acme/plugin:1.0.0") — a
// naive first-colon split would mistake the port for the tag separator.
func repositoryOf(ref string) string {
	prefix, rest := "", ref
	if slash := strings.LastIndex(ref, "/"); slash >= 0 {
		prefix, rest = ref[:slash+1], ref[slash+1:]
	}
	if colon := strings.LastIndex(rest, ":"); colon >= 0 {
		rest = rest[:colon]
	}
	return prefix + rest
}

// warnIfBundleExceedsHostExtractionCap warns when the bundle's total
// uncompressed entry size would exceed hostExtractionCapBytes — the same
// cumulative-bytes check internal/plugin/loader/extract.go applies while
// unpacking a dropped-in bundle. Packaging still proceeds: the CLI has no
// authority to change the host's cap, only to tell an author their bundle is
// bound to be rejected at install time before they sign and ship it.
func warnIfBundleExceedsHostExtractionCap(cmd *cobra.Command, entries []bundleTarEntry) {
	var total int64
	for _, e := range entries {
		total += int64(len(e.data))
	}
	if total > hostExtractionCapBytes {
		fmt.Fprintf(cmd.ErrOrStderr(), "WARNING: bundle contents total %d bytes, over the host's %d byte extraction cap; this bundle will be rejected at install time\n",
			total, hostExtractionCapBytes)
	}
}
