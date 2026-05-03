//go:build integration

package signing

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestUpstreamCLIVerifiesOurSignature generates a keypair and signature using
// our library then shells out to the upstream `minisign` binary to verify.
// This closes the us→upstream half of AC#7.
//
// Requires `minisign` on PATH. Run with:
//
//	go test -tags integration ./plugin-sdk/signing/...
func TestUpstreamCLIVerifiesOurSignature(t *testing.T) {
	if _, err := exec.LookPath("minisign"); err != nil {
		t.Skip("minisign binary not found on PATH; skipping integration test")
	}

	pk, sk, err := GenerateKeypair(nil)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	payload := []byte("integration test payload for upstream verification")
	comment := "timestamp:9876543210\tname:int-test\tversion:1.0.0"

	sig, err := Sign(sk.SecretKey, sk.KeyID, payload, comment)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	dir := t.TempDir()
	payloadPath := filepath.Join(dir, "payload.bin")
	pubPath := filepath.Join(dir, "signing.pub")
	// upstream minisign expects the .minisig file to be at <payload>.minisig
	sigPath := filepath.Join(dir, "payload.bin.minisig")

	if err := os.WriteFile(payloadPath, payload, 0o644); err != nil {
		t.Fatalf("write payload: %v", err)
	}
	if err := os.WriteFile(pubPath, MarshalPublicKey(pk, "integration test key"), 0o644); err != nil {
		t.Fatalf("write pub: %v", err)
	}
	if err := os.WriteFile(sigPath, MarshalSignature(sig, "integration test sig"), 0o644); err != nil {
		t.Fatalf("write sig: %v", err)
	}

	cmd := exec.Command("minisign", "-V", "-p", pubPath, "-m", payloadPath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Errorf("upstream minisign -V failed: %v\noutput: %s", err, out)
	}
}
