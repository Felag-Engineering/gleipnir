package main

import (
	"fmt"
	"io"
	"net"
)

// resolveDisplayURL picks the browser URL to advertise in the startup banner.
//
// When the operator has set public_url (ADR-035) it is authoritative; otherwise
// we fall back to localhost + the bound port. The default Docker/Podman Compose
// mapping publishes the container port unchanged (8080:8080), so localhost:<port>
// is correct for the common case. An operator who remaps the host port set that
// mapping themselves and already knows where to look.
func resolveDisplayURL(listenAddr, publicURL string) string {
	if publicURL != "" {
		return publicURL
	}
	host, port, err := net.SplitHostPort(listenAddr)
	if err != nil {
		// Unparseable addr — show it raw rather than guessing a port.
		return "http://localhost" + listenAddr
	}
	// A wildcard/empty bind host is not a browsable address; localhost is.
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "localhost"
	}
	return "http://" + net.JoinHostPort(host, port)
}

// printReadyBanner writes a plain-text "ready" notice to w with the URL to open.
// It is deliberately plain text (not a slog JSON line) and level-independent so
// it is easy to spot in a terminal and survives GLEIPNIR_LOG_LEVEL=error. The
// structured "server listening" log line is emitted separately for machine
// consumers.
func printReadyBanner(w io.Writer, ver, listenAddr, publicURL string) {
	url := resolveDisplayURL(listenAddr, publicURL)

	const rule = "──────────────────────────────────────────────────────────"
	fmt.Fprintf(w, "\n%s\n", rule)
	fmt.Fprintf(w, "  Gleipnir %s is ready.\n\n", ver)
	fmt.Fprintf(w, "  → Open the web UI:  %s\n", url)
	fmt.Fprintf(w, "%s\n\n", rule)
}
