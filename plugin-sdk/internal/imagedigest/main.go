// Command imagedigest prints the image config digest of an OCI or Docker image
// archive. scripts/build-conformance-stub.sh uses it to stamp the manifest pin
// from the archive itself, using the same parser `gleipnir-plugin package` and
// the host loader use, so the pin cannot disagree with them about the format.
//
// Usage: imagedigest <image-archive>
package main

import (
	"fmt"
	"os"

	"github.com/felag-engineering/gleipnir/plugin-sdk/imagearchive"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: imagedigest <image-archive>")
		os.Exit(2)
	}
	if err := run(os.Args[1]); err != nil {
		fmt.Fprintf(os.Stderr, "imagedigest: %v\n", err)
		os.Exit(1)
	}
}

func run(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open archive: %w", err)
	}
	defer f.Close()

	st, err := f.Stat()
	if err != nil {
		return fmt.Errorf("stat archive: %w", err)
	}
	info, err := imagearchive.Inspect(f, st.Size())
	if err != nil {
		return fmt.Errorf("inspect %s: %w", path, err)
	}
	fmt.Println(info.ConfigDigest)
	return nil
}
