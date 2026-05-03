//go:build ignore

// Program generate regenerates the interop fixture files in this directory
// using our own signing library. Run with:
//
//	go run generate.go
package main

import (
	"fmt"
	"os"

	"github.com/felag-engineering/gleipnir/plugin-sdk/signing"
)

func main() {
	payload := []byte("gleipnir plugin signing interop test payload v1")

	pk, sk, err := signing.GenerateKeypair(nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "generate keypair: %v\n", err)
		os.Exit(1)
	}

	trustedComment := "timestamp:1234567890\tname:test-plugin\tversion:0.1.0"
	sig, err := signing.Sign(sk.SecretKey, sk.KeyID, payload, trustedComment)
	if err != nil {
		fmt.Fprintf(os.Stderr, "sign: %v\n", err)
		os.Exit(1)
	}

	pubData := signing.MarshalPublicKey(pk, "generated test fixture public key")
	sigData := signing.MarshalSignature(sig, "generated test fixture signature")

	if err := os.WriteFile("upstream_payload.bin", payload, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "write payload: %v\n", err)
		os.Exit(1)
	}
	if err := os.WriteFile("upstream.pub", pubData, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "write pub: %v\n", err)
		os.Exit(1)
	}
	if err := os.WriteFile("upstream.minisig", sigData, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "write minisig: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("fixtures written: upstream_payload.bin, upstream.pub, upstream.minisig")
}
