package loader

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/felag-engineering/gleipnir/internal/plugin/container"
	"github.com/felag-engineering/gleipnir/plugin-sdk/imagearchive"
	"github.com/felag-engineering/gleipnir/plugin-sdk/manifestv2"
)

// ErrImageArchiveInvalid reports that a bundle's image archive failed the
// host's pre-load inspection.
//
// The signature proves only that the author signed these bytes. `docker load`
// acts on whatever the archive says about itself: it can bring in extra
// images and point tags such as postgres:16 at attacker content on the
// operator's daemon, and the post-load digest check would still pass because
// the pinned image is among them. So the archive is read and judged before the
// daemon ever sees it.
var ErrImageArchiveInvalid = errors.New("image archive failed inspection")

// inspectImageArchive judges the bundle's image archive without touching the
// container runtime. It reuses the SDK's parser, which is the same code the
// author's packaging step ran, so the two cannot disagree about what an
// archive contains. That parser already refuses: more than one image, an
// index.json that disagrees with manifest.json, manifest or config blobs whose
// bytes do not hash to their declared digest, duplicate entries, non-canonical
// or escaping entry names, non-regular entries, and oversized entries or
// decompressed streams.
//
// On top of that this enforces the two host-side rules the parser leaves to
// its caller: the computed config digest must be the manifest's pin, and every
// tag baked into the archive must name the manifest's own repository.
//
// The returned Info carries the digests the verified archive hashes to. The
// post-load check uses them to recognize the loaded image under either image
// store (see imageMatchesArchive); they come from the archive itself, so
// accepting them never accepts a digest the archive does not prove.
func inspectImageArchive(archivePath string, pkg manifestv2.Package) (imagearchive.Info, error) {
	f, err := os.Open(archivePath)
	if err != nil {
		return imagearchive.Info{}, fmt.Errorf("open image archive: %w", err)
	}
	defer f.Close()

	st, err := f.Stat()
	if err != nil {
		return imagearchive.Info{}, fmt.Errorf("stat image archive: %w", err)
	}

	info, err := imagearchive.Inspect(f, st.Size())
	if err != nil {
		return imagearchive.Info{}, fmt.Errorf("%w: %w", ErrImageArchiveInvalid, err)
	}

	if pin := pkg.Digest(); info.ConfigDigest != pin {
		return imagearchive.Info{}, &imageDigestMismatchError{Expected: pin, Observed: info.ConfigDigest}
	}

	if err := checkArchiveTags(info.Tags, pkg.Repository()); err != nil {
		return imagearchive.Info{}, fmt.Errorf("%w: %w", ErrImageArchiveInvalid, err)
	}
	return info, nil
}

// imageMatchesArchive reports whether the runtime's inspected image is the one
// the verified archive describes. The pin is the config digest, which is the
// image ID under the classic Docker image store. With the containerd image
// store the daemon reports the manifest digest as the ID instead, so that is
// accepted too, as is a RepoDigest equal to the pinned reference. Every
// accepted value comes from the archive or the pin; an arbitrary digest the
// daemon happens to report is never accepted.
func imageMatchesArchive(inspected container.ImageInfo, verified imagearchive.Info, pinnedReference string) bool {
	if inspected.ID == verified.ConfigDigest {
		return true
	}
	if verified.ManifestDigest != "" && inspected.ID == verified.ManifestDigest {
		return true
	}
	return slices.Contains(inspected.RepoDigests, pinnedReference)
}

// checkArchiveTags requires every embedded tag to name repository. Tags on the
// manifest's own repository are harmless, since the operator's daemon gains
// nothing it was not already going to hold under that name; any other
// repository is a foreign tag the load would apply to the host daemon.
//
// A bare name ("latest", as org.opencontainers.image.ref.name often carries)
// names no repository, so it is accepted only when it is exactly the tag part
// of a qualified own-repository tag in the same archive. That is the shape
// Docker 25+ emits ("repo:latest" beside "latest"). Anything looser lets the
// consumer decide what a bare name means, and "postgres" means postgres.
func checkArchiveTags(tags []string, repository string) error {
	confirmedTags := make(map[string]bool)
	for _, tag := range tags {
		if strings.Contains(tag, "/") && tagRepository(tag) == repository {
			confirmedTags[strings.TrimPrefix(tag[len(repository):], ":")] = true
		}
	}

	for _, tag := range tags {
		if tagRepository(tag) == repository {
			continue
		}
		if !strings.ContainsAny(tag, "/:") && confirmedTags[tag] {
			continue
		}
		return fmt.Errorf("archive is tagged %q, which is not the manifest repository %q", tag, repository)
	}
	return nil
}

// tagRepository strips a trailing ":tag" from a reference. The colon is looked
// for only after the last '/', since a registry host may carry a port.
func tagRepository(ref string) string {
	slash := strings.LastIndex(ref, "/")
	if colon := strings.LastIndex(ref, ":"); colon > slash {
		return ref[:colon]
	}
	return ref
}
