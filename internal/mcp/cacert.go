package mcp

import (
	"bytes"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"
	"time"
)

// maxCACertPEMBytes bounds the operator-pasted CA certificate PEM, in line
// with this package's other bounded-untrusted-input constants (e.g.
// maxErrorBodyBytes).
const maxCACertPEMBytes = 64 << 10

// ErrNoCertificate is returned by ParseCACertBundle when pemText contains no
// CERTIFICATE PEM block.
var ErrNoCertificate = errors.New("no CERTIFICATE PEM block found")

// ErrEmptyCACert is returned by ValidateCACertPEM when pemText is empty or
// whitespace-only. The write-path handler treats "" as "clear the pin"
// before ever calling ValidateCACertPEM, so by the time this function sees
// empty input it is always an error, not a valid "no CA" state.
var ErrEmptyCACert = errors.New("ca_cert_pem is empty")

// CACertInfo summarizes one certificate parsed from a stored or pasted CA
// bundle, for surfacing back to the operator so they can confirm they pasted
// the certificate they intended to.
type CACertInfo struct {
	Subject           string
	SHA256Fingerprint string // lowercase hex SHA-256 of the DER, no colons (matches Relay's ca_fingerprint format)
	NotAfter          time.Time
}

// parsedCert pairs a CACertInfo with the raw DER it was parsed from, kept
// private to this file: the DER bytes are needed to build the canonical
// re-encoding (see canonicalPEMFromCerts) but have no reason to leak into
// CACertInfo's public shape, which every other caller in this package only
// ever reads as a summary.
type parsedCert struct {
	info CACertInfo
	raw  []byte
}

// parseCACertBundleCerts is the shared structural parser behind both
// ParseCACertBundle and ValidateCACertPEM. See ParseCACertBundle's doc for
// the anti-garbage-injection rules this enforces; both exported functions
// are thin wrappers that never duplicate this logic.
func parseCACertBundleCerts(pemText string) ([]parsedCert, *x509.CertPool, error) {
	data := []byte(pemText)
	pool := x509.NewCertPool()
	var certs []parsedCert

	for i := 0; ; i++ {
		// Every byte that is not part of a CERTIFICATE block must be
		// whitespace. Trimming left and requiring the exact "-----BEGIN "
		// marker here is what catches leading garbage and garbage between
		// blocks — pem.Decode itself has no way to report "I skipped N
		// bytes to find something that looked like a block", so this
		// package computes it directly (security review finding, #928).
		left := bytes.TrimLeft(data, " \t\r\n")
		if len(left) == 0 {
			break
		}
		if !bytes.HasPrefix(left, []byte("-----BEGIN ")) {
			return nil, nil, fmt.Errorf("PEM block %d: unexpected data — expected only whitespace between CERTIFICATE blocks", i)
		}

		block, rest := pem.Decode(left)
		if block == nil {
			return nil, nil, fmt.Errorf("PEM block %d: malformed — no matching END line found", i)
		}

		// pem.Decode silently skips over an invalid or truncated candidate
		// block to find a LATER valid one anywhere in its input — including
		// one that starts at offset 0, which left's own "-----BEGIN "
		// prefix check above cannot catch on its own. The consumed span
		// (left minus whatever pem.Decode left in rest) must contain
		// EXACTLY one "-----BEGIN " occurrence: the one belonging to the
		// block actually returned. A base64 body can never contain the
		// literal substring "-----BEGIN " (base64's alphabet has no '-'),
		// so a second occurrence can only mean an earlier candidate block
		// was silently skipped — e.g. a truncated PRIVATE KEY block, or a
		// block whose BEGIN and END labels don't match, sitting in front of
		// the certificate that was actually returned.
		consumed := left[:len(left)-len(rest)]
		if bytes.Count(consumed, []byte("-----BEGIN ")) != 1 {
			return nil, nil, fmt.Errorf(
				"PEM block %d: unexpected or malformed data was skipped while looking for a valid block", i)
		}

		if block.Type != "CERTIFICATE" {
			return nil, nil, fmt.Errorf("PEM block %d is %q, not CERTIFICATE", i, block.Type)
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, nil, fmt.Errorf("parse PEM block %d: %w", i, err)
		}

		pool.AddCert(cert)
		fingerprint := sha256.Sum256(cert.Raw)
		certs = append(certs, parsedCert{
			info: CACertInfo{
				Subject:           cert.Subject.String(),
				SHA256Fingerprint: hex.EncodeToString(fingerprint[:]),
				NotAfter:          cert.NotAfter.UTC(),
			},
			raw: append([]byte(nil), cert.Raw...),
		})

		data = rest
	}

	if len(certs) == 0 {
		return nil, nil, ErrNoCertificate
	}
	return certs, pool, nil
}

// canonicalPEMFromCerts re-encodes each parsed certificate's raw DER as a
// standard PEM CERTIFICATE block via pem.EncodeToMemory, concatenated in
// bundle order. This is deliberately NOT the operator's original bytes:
// storing (and later reading back to an auditor) anything other than
// exactly what was parsed would let arbitrary bytes that happened to
// survive parsing — comments, non-standard line wrapping, or any other
// engine-specific PEM quirk — travel to disk and back out unchanged
// (security review finding, #928). The canonical form is the only
// representation guaranteed to contain nothing but the certificates that
// were actually verified.
func canonicalPEMFromCerts(certs []parsedCert) string {
	var buf bytes.Buffer
	for _, c := range certs {
		buf.Write(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.raw}))
	}
	return buf.String()
}

// ParseCACertBundle performs a structural parse only: no clock, no expiry
// check. It is used both on the read path (surfacing the stored PEM's
// summary back to the operator) and at client-build time (registry.go),
// where an already-validated-but-since-expired PEM must still produce a
// pool — the operator should not be locked out of viewing or otherwise
// using a server they configured before its CA expired underneath them.
//
// Every block's Type must be exactly "CERTIFICATE"; anything else (a
// private key, a CSR) is rejected outright. So is ANY non-whitespace byte
// that is not part of a CERTIFICATE block — before the first block,
// between blocks, or after the last one — including a malformed or
// truncated block that pem.Decode itself would otherwise silently skip
// over on the way to a later valid one (see parseCACertBundleCerts for the
// mechanics). Zero blocks is ErrNoCertificate.
//
// The returned pool is built fresh via x509.NewCertPool and contains
// exactly the parsed certificates — it never starts from
// x509.SystemCertPool. A server pinned to a specific CA must be verified
// against that CA alone.
func ParseCACertBundle(pemText string) ([]CACertInfo, *x509.CertPool, error) {
	certs, pool, err := parseCACertBundleCerts(pemText)
	if err != nil {
		return nil, nil, err
	}
	infos := make([]CACertInfo, len(certs))
	for i, c := range certs {
		infos[i] = c.info
	}
	return infos, pool, nil
}

// ValidateCACertPEM is the write-path gate: on top of ParseCACertBundle's
// structural checks, it rejects an expired certificate (an expired CA can
// never verify a chain, so accepting it at write time would only move the
// failure to first use) and returns the CANONICAL re-encoding of the parsed
// bundle — see canonicalPEMFromCerts — rather than pemText itself. Callers
// (Create/Update in internal/http/api/mcp_handler.go) must store the
// returned canonicalPEM, never the operator's original bytes.
//
// IsCA is not required: Go's verifier accepts a leaf that is itself present
// in RootCAs, and pinning a homelab self-signed server certificate directly
// (rather than a separate CA certificate) is a legitimate configuration.
func ValidateCACertPEM(pemText string) (canonicalPEM string, infos []CACertInfo, err error) {
	if strings.TrimSpace(pemText) == "" {
		return "", nil, ErrEmptyCACert
	}
	if len(pemText) > maxCACertPEMBytes {
		return "", nil, fmt.Errorf("ca_cert_pem exceeds maximum size of %d bytes", maxCACertPEMBytes)
	}

	certs, _, err := parseCACertBundleCerts(pemText)
	if err != nil {
		return "", nil, err
	}

	now := timeNow()
	infos = make([]CACertInfo, len(certs))
	for i, c := range certs {
		if now.After(c.info.NotAfter) {
			return "", nil, fmt.Errorf("certificate %q expired at %s", c.info.Subject, c.info.NotAfter.Format(time.RFC3339))
		}
		infos[i] = c.info
	}

	return canonicalPEMFromCerts(certs), infos, nil
}
