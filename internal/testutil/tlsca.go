package testutil

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestCA is a self-signed certificate authority minted for TLS tests that
// need real certificate-chain verification against a caller-chosen trust
// root — issue #928's per-server CA pin cannot be tested meaningfully
// against httptest.NewTLSServer, whose every instance presents the exact
// same baked-in certificate (see StartTLSServer's doc for why that matters).
//
// This file is stdlib-only and does not import internal/mcp, so it can be
// used both by internal/mcp's own tests and by internal/http/api's, without
// either creating an import cycle.
type TestCA struct {
	Cert *x509.Certificate
	Key  crypto.Signer
	PEM  string // PEM-encoded certificate; what an operator would paste as ca_cert_pem
}

// TestCAOption configures NewTestCA.
type TestCAOption func(*x509.Certificate)

// WithValidity overrides the CA's default (long) validity window. Used to
// mint an already-expired CA for expiry-rejection tests.
func WithValidity(notBefore, notAfter time.Time) TestCAOption {
	return func(tmpl *x509.Certificate) {
		tmpl.NotBefore = notBefore
		tmpl.NotAfter = notAfter
	}
}

// NewTestCA mints a self-signed ECDSA P-256 CA certificate valid for ten
// years by default.
func NewTestCA(t *testing.T, opts ...TestCAOption) *TestCA {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate CA key: %v", err)
	}

	tmpl := &x509.Certificate{
		SerialNumber:          newTestSerial(t),
		Subject:               pkix.Name{CommonName: "gleipnir-test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(10 * 365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	for _, opt := range opts {
		opt(tmpl)
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create CA certificate: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse CA certificate: %v", err)
	}

	return &TestCA{
		Cert: cert,
		Key:  key,
		PEM:  string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
	}
}

// IssueServerCert mints a leaf certificate signed by ca, valid for dnsNames
// and ips.
func (ca *TestCA) IssueServerCert(t *testing.T, dnsNames []string, ips []net.IP) tls.Certificate {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate leaf key: %v", err)
	}

	tmpl := &x509.Certificate{
		SerialNumber: newTestSerial(t),
		Subject:      pkix.Name{CommonName: "gleipnir-test-leaf"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     dnsNames,
		IPAddresses:  ips,
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.Cert, &key.PublicKey, ca.Key)
	if err != nil {
		t.Fatalf("create leaf certificate: %v", err)
	}
	leafPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})

	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshal leaf key: %v", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})

	tlsCert, err := tls.X509KeyPair(leafPEM, keyPEM)
	if err != nil {
		t.Fatalf("build tls.Certificate: %v", err)
	}
	return tlsCert
}

// StartTLSServer starts h behind a TLS listener presenting cert.
//
// Deliberately does NOT use httptest.NewTLSServer: every instance it creates
// shares the package's one hard-coded internal test certificate/CA, which
// makes a "client trusts a different, unrelated CA" test impossible to
// construct. This helper never sets InsecureSkipVerify anywhere, on either
// the server or a client dialing it — a test proves trust by handing the
// client the issuing TestCA's PEM, the same thing an operator would paste.
func StartTLSServer(t *testing.T, h http.Handler, cert tls.Certificate) *httptest.Server {
	t.Helper()

	srv := httptest.NewUnstartedServer(h)
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv
}

// newTestSerial returns a random 128-bit serial number suitable for a test
// certificate.
func newTestSerial(t *testing.T) *big.Int {
	t.Helper()
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatalf("generate certificate serial: %v", err)
	}
	return serial
}
