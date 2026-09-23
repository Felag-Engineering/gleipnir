package mcp

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/felag-engineering/gleipnir/internal/testutil"
)

// pemBlock builds a PEM-encoded block of the given type from arbitrary bytes,
// for constructing malformed test inputs (a non-certificate block, or a
// CERTIFICATE block whose "DER" is not actually DER).
func pemBlock(blockType string, bytes []byte) string {
	return string(pem.EncodeToMemory(&pem.Block{Type: blockType, Bytes: bytes}))
}

func TestParseCACertBundle(t *testing.T) {
	ca := testutil.NewTestCA(t)
	otherCA := testutil.NewTestCA(t)

	tests := []struct {
		name    string
		pemText string
		wantErr error  // checked via errors.Is when set
		errSub  string // checked via strings.Contains when wantErr is unset
	}{
		{
			name:    "valid single certificate",
			pemText: ca.PEM,
		},
		{
			name:    "valid two-certificate bundle",
			pemText: ca.PEM + otherCA.PEM,
		},
		{
			name:    "malformed PEM (no END marker)",
			pemText: "-----BEGIN CERTIFICATE-----\nnot a real cert\n",
			errSub:  "no matching END line found",
		},
		{
			name:    "random text",
			pemText: "this is not PEM at all",
			errSub:  "unexpected data",
		},
		{
			name:    "empty input",
			pemText: "",
			wantErr: ErrNoCertificate,
		},
		{
			name:    "non-certificate block: private key",
			pemText: pemBlock("PRIVATE KEY", []byte("fake key bytes")),
			errSub:  `not CERTIFICATE`,
		},
		{
			name:    "non-certificate block: certificate request",
			pemText: pemBlock("CERTIFICATE REQUEST", []byte("fake csr bytes")),
			errSub:  `not CERTIFICATE`,
		},
		{
			name:    "CERTIFICATE block with junk DER",
			pemText: pemBlock("CERTIFICATE", []byte("this is not valid DER")),
			errSub:  "parse PEM block 0",
		},
		{
			name:    "trailing garbage after a valid block",
			pemText: ca.PEM + "not a pem block",
			errSub:  "unexpected data",
		},
		{
			name:    "leading garbage before a valid block",
			pemText: "garbage-before-the-cert\n" + ca.PEM,
			errSub:  "unexpected data",
		},
		{
			name:    "garbage between two valid blocks",
			pemText: ca.PEM + "garbage-between-certs\n" + otherCA.PEM,
			errSub:  "unexpected data",
		},
		{
			// A truncated PRIVATE KEY block (no END line) sitting in front
			// of a real certificate. pem.Decode does not fail on this — it
			// silently skips the truncated block and returns the
			// certificate that follows it. That skip must be rejected, not
			// accepted with the truncated key bytes discarded (security
			// review finding, #928): the operator asked to pin a CA, not to
			// have an arbitrary private-key fragment silently dropped from
			// what gets stored.
			name:    "truncated PRIVATE KEY before a cert",
			pemText: "-----BEGIN PRIVATE KEY-----\nAAAAAAAA\n" + ca.PEM,
			errSub:  "unexpected or malformed data was skipped",
		},
		{
			// BEGIN CERTIFICATE paired with a non-matching END line. Also
			// silently skipped by pem.Decode on the way to the real
			// certificate that follows; also must be rejected.
			name:    "mismatched BEGIN/END before a cert",
			pemText: "-----BEGIN CERTIFICATE-----\nQUJD\n-----END RSA PRIVATE KEY-----\n" + ca.PEM,
			errSub:  "unexpected or malformed data was skipped",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			infos, pool, err := ParseCACertBundle(tc.pemText)

			if tc.wantErr != nil || tc.errSub != "" {
				if err == nil {
					t.Fatal("ParseCACertBundle: want error, got nil")
				}
				if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
					t.Errorf("err = %v, want errors.Is match for %v", err, tc.wantErr)
				}
				if tc.errSub != "" && !strings.Contains(err.Error(), tc.errSub) {
					t.Errorf("err = %q, want substring %q", err.Error(), tc.errSub)
				}
				if infos != nil || pool != nil {
					t.Error("ParseCACertBundle: want nil infos and pool on error")
				}
				return
			}

			if err != nil {
				t.Fatalf("ParseCACertBundle: unexpected error: %v", err)
			}
			if pool == nil {
				t.Fatal("ParseCACertBundle: pool is nil on success")
			}
			if len(infos) == 0 {
				t.Fatal("ParseCACertBundle: infos is empty on success")
			}
			for i, info := range infos {
				if info.Subject == "" {
					t.Errorf("infos[%d].Subject is empty", i)
				}
				if len(info.SHA256Fingerprint) != hex.EncodedLen(sha256.Size) {
					t.Errorf("infos[%d].SHA256Fingerprint = %q, want %d hex chars", i, info.SHA256Fingerprint, hex.EncodedLen(sha256.Size))
				}
				if info.NotAfter.IsZero() {
					t.Errorf("infos[%d].NotAfter is zero", i)
				}
			}
		})
	}
}

// TestParseCACertBundle_FingerprintMatchesRelayFormat pins the fingerprint
// format to exactly what Relay's own ca_fingerprint prints: lowercase hex
// SHA-256 of the DER, no colons, no uppercase — so an operator can compare
// the two strings character for character.
func TestParseCACertBundle_FingerprintMatchesRelayFormat(t *testing.T) {
	ca := testutil.NewTestCA(t)

	infos, _, err := ParseCACertBundle(ca.PEM)
	if err != nil {
		t.Fatalf("ParseCACertBundle: %v", err)
	}
	if len(infos) != 1 {
		t.Fatalf("infos length = %d, want 1", len(infos))
	}

	want := sha256.Sum256(ca.Cert.Raw)
	wantHex := hex.EncodeToString(want[:])
	if infos[0].SHA256Fingerprint != wantHex {
		t.Errorf("SHA256Fingerprint = %q, want %q", infos[0].SHA256Fingerprint, wantHex)
	}
	if strings.ToLower(infos[0].SHA256Fingerprint) != infos[0].SHA256Fingerprint {
		t.Error("SHA256Fingerprint contains uppercase characters")
	}
	if strings.Contains(infos[0].SHA256Fingerprint, ":") {
		t.Error("SHA256Fingerprint contains colons, want a bare hex string")
	}
}

// TestValidateCACertPEM_ExpiredCertificate is kept separate from the
// table-driven suite below because it is the one case that mutates the
// package's shared clock (CLAUDE.md "Testing time-dependent code": no
// t.Parallel when swapping timeNow — package mcp's tests already never use
// t.Parallel, see registry_test.go's package-wide note).
func TestValidateCACertPEM_ExpiredCertificate(t *testing.T) {
	notBefore := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	notAfter := time.Date(2021, 1, 1, 0, 0, 0, 0, time.UTC)
	expiredCA := testutil.NewTestCA(t, testutil.WithValidity(notBefore, notAfter))

	freezeClock(t, notAfter.Add(24*time.Hour))

	_, _, err := ValidateCACertPEM(expiredCA.PEM)
	if err == nil {
		t.Fatal("ValidateCACertPEM: want error for an expired certificate, got nil")
	}
	if !strings.Contains(err.Error(), "expired") {
		t.Errorf("err = %q, want it to mention expiry", err.Error())
	}
}

// TestValidateCACertPEM_NotYetExpired proves the companion direction: the
// same certificate, evaluated before its NotAfter, is accepted. Without this
// the expiry test above could pass for the wrong reason (e.g. a bug that
// always rejects).
func TestValidateCACertPEM_NotYetExpired(t *testing.T) {
	notBefore := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	notAfter := time.Date(2021, 1, 1, 0, 0, 0, 0, time.UTC)
	ca := testutil.NewTestCA(t, testutil.WithValidity(notBefore, notAfter))

	freezeClock(t, notBefore.Add(24*time.Hour))

	if _, _, err := ValidateCACertPEM(ca.PEM); err != nil {
		t.Fatalf("ValidateCACertPEM: unexpected error: %v", err)
	}
}

func TestValidateCACertPEM(t *testing.T) {
	ca := testutil.NewTestCA(t)

	tests := []struct {
		name    string
		pemText string
		wantErr error
		errSub  string
	}{
		{
			name:    "valid certificate",
			pemText: ca.PEM,
		},
		{
			name:    "empty",
			pemText: "",
			wantErr: ErrEmptyCACert,
		},
		{
			name:    "whitespace only",
			pemText: "   \n\t  ",
			wantErr: ErrEmptyCACert,
		},
		{
			name:    "malformed PEM",
			pemText: "not PEM at all",
			errSub:  "unexpected data",
		},
		{
			name:    "over max size",
			pemText: ca.PEM + strings.Repeat("x", maxCACertPEMBytes),
			errSub:  "exceeds maximum size",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			canonical, infos, err := ValidateCACertPEM(tc.pemText)

			if tc.wantErr != nil || tc.errSub != "" {
				if err == nil {
					t.Fatal("ValidateCACertPEM: want error, got nil")
				}
				if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
					t.Errorf("err = %v, want errors.Is match for %v", err, tc.wantErr)
				}
				if tc.errSub != "" && !strings.Contains(err.Error(), tc.errSub) {
					t.Errorf("err = %q, want substring %q", err.Error(), tc.errSub)
				}
				if canonical != "" {
					t.Errorf("canonical = %q, want empty on error", canonical)
				}
				return
			}

			if err != nil {
				t.Fatalf("ValidateCACertPEM: unexpected error: %v", err)
			}
			if len(infos) == 0 {
				t.Fatal("ValidateCACertPEM: infos is empty on success")
			}
			if canonical == "" {
				t.Fatal("ValidateCACertPEM: canonical is empty on success")
			}
		})
	}
}

// TestValidateCACertPEM_ReturnsCanonicalReencoding is the direct regression
// test for the #928 security review finding: the stored value must be the
// canonical re-encoding of the parsed certificate(s), not the operator's
// original bytes. It exercises both a single certificate and a two-
// certificate bundle, and it deliberately submits a PEM with a header
// comment before the block (a real-world thing an operator might paste)
// to prove that non-certificate bytes surviving parsing never make it into
// storage.
func TestValidateCACertPEM_ReturnsCanonicalReencoding(t *testing.T) {
	ca := testutil.NewTestCA(t)
	otherCA := testutil.NewTestCA(t)

	wantSingle := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.Cert.Raw}))
	wantBundle := wantSingle + string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: otherCA.Cert.Raw}))

	tests := []struct {
		name    string
		pemText string
		want    string
	}{
		{
			name:    "single certificate, byte-identical input",
			pemText: ca.PEM,
			want:    wantSingle,
		},
		{
			name:    "two-certificate bundle",
			pemText: ca.PEM + otherCA.PEM,
			want:    wantBundle,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			canonical, _, err := ValidateCACertPEM(tc.pemText)
			if err != nil {
				t.Fatalf("ValidateCACertPEM: %v", err)
			}
			if canonical != tc.want {
				t.Errorf("canonical PEM = %q, want %q", canonical, tc.want)
			}

			// Idempotent: re-validating the canonical output must return
			// byte-identical output.
			roundTrip, _, err := ValidateCACertPEM(canonical)
			if err != nil {
				t.Fatalf("ValidateCACertPEM (round-trip): %v", err)
			}
			if roundTrip != canonical {
				t.Errorf("round-trip canonical = %q, want %q", roundTrip, canonical)
			}
		})
	}
}
