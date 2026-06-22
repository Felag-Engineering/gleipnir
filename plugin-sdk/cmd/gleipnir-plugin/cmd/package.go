package cmd

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/felag-engineering/gleipnir/plugin-sdk/manifest"
	"github.com/felag-engineering/gleipnir/plugin-sdk/signing"
)

// NewPackageCmd returns the cobra.Command for the `package` subcommand.
func NewPackageCmd() *cobra.Command {
	var flagBinary, flagManifest, flagKey, flagPubkey, flagOutDir, flagSBOM string
	var flagKeyStdin, flagUnsigned bool

	defaultKey := defaultKeyPath()

	cmd := &cobra.Command{
		Use:   "package",
		Short: "Build and sign a plugin release tarball",
		Long: `Build a signed plugin release tarball per spec §14.5.

The tarball contains:
  <name>-<version>/
    <manifest.Name>            (mode 0755, the binary)
    manifest.yaml             (mode 0644)
    <manifest.Name>.minisig   (mode 0644)
    signing.pub               (mode 0644)
    sbom.cyclonedx.json       (mode 0644, optional)

The binary and .minisig filenames both derive from manifest.Name — the host
locates the binary at <bundle>/<manifest.Name> to hash and verify it, so the
binary basename on disk is irrelevant.

Signed payload is sha256(binary) || sha256(manifest) per spec §5.2.

Use --unsigned only when the host has GLEIPNIR_ALLOW_UNSIGNED_PLUGINS=true set.
Unsigned bundles carry no .minisig or signing.pub.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPackage(cmd, flagBinary, flagManifest, flagKey, flagKeyStdin, flagPubkey, flagOutDir, flagSBOM, flagUnsigned)
		},
	}

	cmd.Flags().StringVar(&flagBinary, "binary", "", "path to plugin binary (required)")
	cmd.Flags().StringVar(&flagManifest, "manifest", "manifest.yaml", "path to manifest.yaml")
	cmd.Flags().StringVar(&flagKey, "key", defaultKey, "path to .key file")
	cmd.Flags().BoolVar(&flagKeyStdin, "key-stdin", false, "read .key from stdin (CI)")
	cmd.Flags().StringVar(&flagPubkey, "pubkey", "", "path to .pub file (default: sibling of .key)")
	cmd.Flags().StringVar(&flagOutDir, "out-dir", "dist", "output directory for tarball")
	cmd.Flags().StringVar(&flagSBOM, "sbom", "", "optional CycloneDX SBOM JSON path")
	cmd.Flags().BoolVar(&flagUnsigned, "unsigned", false, "produce unsigned bundle (requires GLEIPNIR_ALLOW_UNSIGNED_PLUGINS=true on host)")

	_ = cmd.MarkFlagRequired("binary")

	return cmd
}

func runPackage(cmd *cobra.Command, binary, manifestPath, flagKey string, flagKeyStdin bool, pubkeyPath, outDir, sbomPath string, unsigned bool) error {
	stdin := cmd.InOrStdin()

	// Load and canonicalise manifest.
	var manifestData []byte
	var err error
	if _, err = os.Stat(manifestPath); err == nil {
		manifestData, err = loadCanonicalManifest(manifestPath)
		if err != nil {
			return fmt.Errorf("package: %w", err)
		}
	} else {
		// Derive manifest from binary --emit-manifest.
		raw, err := runBinary(binary, []string{"--emit-manifest"})
		if err != nil {
			return fmt.Errorf("package: invoke binary for manifest: %w", err)
		}
		manifestData, err = manifest.Canonicalize(raw)
		if err != nil {
			return fmt.Errorf("package: canonicalise manifest from binary: %w", err)
		}
	}

	m, err := parseManifestFromBytes(manifestData)
	if err != nil {
		return fmt.Errorf("package: parse manifest: %w", err)
	}
	if m.Name == "" || m.Version == "" {
		return fmt.Errorf("package: manifest must have name and version set")
	}
	if err := validateBundleNameComponent("name", m.Name); err != nil {
		return fmt.Errorf("package: %w", err)
	}
	if err := validateBundleNameComponent("version", m.Version); err != nil {
		return fmt.Errorf("package: %w", err)
	}

	binaryData, err := os.ReadFile(binary)
	if err != nil {
		return fmt.Errorf("package: read binary: %w", err)
	}

	// Signing or unsigned.
	var sigData, pubData []byte
	if !unsigned {
		raw, keyID, defaultPubPath, err := loadSecretKey(flagKey, flagKeyStdin, stdin)
		if err != nil {
			return fmt.Errorf("package: %w", err)
		}

		trustedComment := fmt.Sprintf("timestamp:%d\tname:%s\tversion:%s",
			time.Now().Unix(), m.Name, m.Version)
		payload := signing.PluginPayload(binaryData, manifestData)
		sig, err := signing.Sign(raw, keyID, payload, trustedComment)
		if err != nil {
			return fmt.Errorf("package: sign: %w", err)
		}
		sigData = signing.MarshalSignature(sig, fmt.Sprintf("signature for %s %s", m.Name, m.Version))

		resolvedPubPath := pubkeyPath
		if resolvedPubPath == "" {
			resolvedPubPath = defaultPubPath
		}
		if resolvedPubPath != "" {
			pubData, err = os.ReadFile(resolvedPubPath)
			if err != nil {
				return fmt.Errorf("package: read public key %s: %w", resolvedPubPath, err)
			}
		}
		if len(pubData) == 0 {
			return fmt.Errorf("package: no public key available; use --pubkey to specify one")
		}
	} else {
		fmt.Fprintln(cmd.ErrOrStderr(), "WARNING: building unsigned bundle — only loads on hosts with GLEIPNIR_ALLOW_UNSIGNED_PLUGINS=true")
	}

	// Optional SBOM.
	var sbomData []byte
	if sbomPath != "" {
		sbomData, err = os.ReadFile(sbomPath)
		if err != nil {
			return fmt.Errorf("package: read sbom: %w", err)
		}
	}

	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return fmt.Errorf("package: create output dir: %w", err)
	}

	tarName := fmt.Sprintf("%s-%s.tar.gz", m.Name, m.Version)
	tarPath := filepath.Join(outDir, tarName)

	if err := writeTarball(tarPath, binaryData, manifestData, sigData, pubData, sbomData, m.Name, m.Version); err != nil {
		return fmt.Errorf("package: write tarball: %w", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "wrote bundle: %s\n", tarPath)
	return nil
}

// validateBundleNameComponent rejects values that could escape a tar path:
// those containing '/', '\', or starting with '.'.
func validateBundleNameComponent(field, value string) error {
	if strings.ContainsAny(value, `/\`) || strings.HasPrefix(value, ".") {
		return fmt.Errorf("manifest %s contains path separator or starts with '.': %q", field, value)
	}
	return nil
}

// writeTarball writes the plugin bundle tarball per spec §14.5. The binary is
// stored under the manifest name (not the source binary's basename) because the
// host locates it at <bundle>/<manifest.Name> to hash and verify the signature.
func writeTarball(tarPath string, binaryData, manifestData, sigData, pubData, sbomData []byte, name, version string) error {
	f, err := os.Create(tarPath)
	if err != nil {
		return fmt.Errorf("create tarball: %w", err)
	}

	gw := gzip.NewWriter(f)
	tw := tar.NewWriter(gw)

	mtime := sourceDateEpoch()
	prefix := fmt.Sprintf("%s-%s", name, version)

	// Build entries, then sort by name for determinism.
	type entry struct {
		name string
		mode int64
		data []byte
	}

	entries := []entry{
		{prefix + "/" + name, 0o755, binaryData},
		{prefix + "/manifest.yaml", 0o644, manifestData},
	}
	if sigData != nil {
		// .minisig filename derives from manifest.Name per spec §14.5.
		entries = append(entries, entry{prefix + "/" + name + ".minisig", 0o644, sigData})
	}
	if pubData != nil {
		entries = append(entries, entry{prefix + "/signing.pub", 0o644, pubData})
	}
	if sbomData != nil {
		entries = append(entries, entry{prefix + "/sbom.cyclonedx.json", 0o644, sbomData})
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].name < entries[j].name
	})

	for _, e := range entries {
		hdr := &tar.Header{
			Name:     e.name,
			Mode:     e.mode,
			Size:     int64(len(e.data)),
			ModTime:  mtime,
			Typeflag: tar.TypeReg,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return fmt.Errorf("write tar header for %s: %w", e.name, err)
		}
		if _, err := tw.Write(e.data); err != nil {
			return fmt.Errorf("write tar entry %s: %w", e.name, err)
		}
	}

	if err := tw.Close(); err != nil {
		return fmt.Errorf("flush tar: %w", err)
	}
	if err := gw.Close(); err != nil {
		return fmt.Errorf("flush gzip: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close output: %w", err)
	}
	return nil
}

// sourceDateEpoch returns the mtime for tarball entries. Honors the
// SOURCE_DATE_EPOCH environment variable (standard for reproducible builds);
// falls back to the current time.
func sourceDateEpoch() time.Time {
	if v := os.Getenv("SOURCE_DATE_EPOCH"); v != "" {
		if sec, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil {
			return time.Unix(sec, 0).UTC()
		}
	}
	return time.Now().UTC()
}
