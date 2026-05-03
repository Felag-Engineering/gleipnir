//go:build tools

// Package tools pins the exact versions of protobuf code generators used to
// produce stubs in plugin-sdk/gen/. Running `go mod tidy` with this file
// present keeps the generator binaries anchored in go.sum even though nothing
// in the main module imports them at runtime.
//
// To regenerate stubs: `make proto` (runs `buf generate` with buf.gen.yaml).
// buf uses BSR remote plugins (see buf.gen.yaml) and does not invoke the local
// binaries below directly; these imports exist solely for version pinning.
package tools

import (
	_ "google.golang.org/grpc/cmd/protoc-gen-go-grpc"
)
