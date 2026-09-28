// Package imagearchive inspects OCI and Docker-legacy image archives
// (`docker save` / `podman save` output, gzip-compressed or plain tar) to
// recover the one fact a plugin bundle's signing step — and a host's
// install-time verification — both need answered before they trust the
// archive: the image config digest (the classic image ID) and any
// repository tags baked into the archive itself.
//
// Every digest Inspect/InspectBytes returns is recomputed from the bytes it
// names, never trusted from a JSON field: an OCI-layout manifest blob is
// hashed and checked against index.json's own descriptor digest before it is
// parsed, and the config blob it names is hashed and checked against that
// digest before ConfigDigest is returned. A Docker-legacy archive has no
// declared digest to trust in the first place — manifest.json only names a
// path, so its config digest has always been a hash of that path's contents.
//
// This lives in plugin-sdk, not the CLI's own cmd package, so the host loader
// can import the exact same computation at install time (#1032) instead of
// re-deriving it. The CLI's package-time check and the host's install-time
// check must never become two implementations that could quietly drift apart.
package imagearchive
