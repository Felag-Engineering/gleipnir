package mcp

import (
	"crypto/x509"
	"errors"
	"fmt"
)

// TLSVerificationError names a TLS handshake failure that is specifically a
// certificate-verification problem, distinct from a generic connection
// failure. Acceptance for issue #928 requires that a misconfigured CA
// diagnose itself rather than surface as an opaque "could not reach
// server" — this is the typed error that carries that diagnosis end to end
// through humanizeMCPError.
type TLSVerificationError struct {
	Host   string
	Reason string
	Err    error
}

func (e *TLSVerificationError) Error() string {
	return fmt.Sprintf("TLS certificate verification failed for %s: %s", e.Host, e.Reason)
}

func (e *TLSVerificationError) Unwrap() error {
	return e.Err
}

// wrapTLSVerificationError inspects err for the x509 verification failure
// types the Go TLS stack produces and, when found, wraps it as a
// *TLSVerificationError carrying a human-readable, actionable reason.
// Anything else is returned unchanged.
//
// pinned reports whether this server has an operator-configured CA
// certificate (registry.go: WithRootCAs was applied), which changes the
// most likely explanation for x509.UnknownAuthorityError: with no pin, the
// server's certificate is simply not signed by a publicly trusted CA
// (probably a private CA the operator has not pinned yet); with a pin, the
// server presented a DIFFERENT certificate than the one the operator
// configured.
//
// Since Go 1.20, TLS handshake verification errors arrive wrapped in
// *tls.CertificateVerificationError, itself usually wrapped in *url.Error
// by net/http — errors.As walks through both to find the underlying x509
// type, so this works regardless of how many layers wrap it.
func wrapTLSVerificationError(host string, pinned bool, err error) error {
	var unknownAuthority x509.UnknownAuthorityError
	if errors.As(err, &unknownAuthority) {
		reason := "the server's certificate is signed by an unknown authority — if this server uses a private CA, add its CA certificate (PEM) to this MCP server"
		if pinned {
			reason = "the server's certificate is not signed by this server's configured CA certificate"
		}
		return &TLSVerificationError{Host: host, Reason: reason, Err: err}
	}

	var hostnameErr x509.HostnameError
	if errors.As(err, &hostnameErr) {
		return &TLSVerificationError{
			Host:   host,
			Reason: fmt.Sprintf("the certificate is not valid for the host in the URL (%v) — the URL host must match a name in the server certificate", hostnameErr),
			Err:    err,
		}
	}

	var certInvalid x509.CertificateInvalidError
	if errors.As(err, &certInvalid) {
		reason := fmt.Sprintf("the certificate is invalid: %v", certInvalid)
		if certInvalid.Reason == x509.Expired {
			reason = "the certificate has expired or is not yet valid"
		}
		return &TLSVerificationError{Host: host, Reason: reason, Err: err}
	}

	return err
}
