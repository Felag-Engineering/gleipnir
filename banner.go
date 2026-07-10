package main

import (
	"fmt"
	"io"
	"net"
)

// resolveDisplayURL picks the browser URL to advertise in the startup banner
// and reports whether a Compose host-port hint should accompany it.
//
// When the operator has set public_url (ADR-035) it is authoritative and no
// hint is needed. Otherwise we fall back to localhost + the bound port. That
// bound port is the container-INTERNAL port (default :8080); under Docker or
// Podman Compose it is remapped to a host port (default 3000) that the process
// cannot observe from inside the container — hence the hint so a Compose user
// is not sent to the wrong port.
func resolveDisplayURL(listenAddr, publicURL string) (url string, composeHint bool) {
	if publicURL != "" {
		return publicURL, false
	}
	host, port, err := net.SplitHostPort(listenAddr)
	if err != nil {
		// Unparseable addr — show it raw rather than guessing a port.
		return "http://localhost" + listenAddr, true
	}
	// A wildcard/empty bind host is not a browsable address; localhost is.
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "localhost"
	}
	return "http://" + net.JoinHostPort(host, port), true
}

// printReadyBanner writes a plain-text "ready" notice to w with the URL to open.
// It is deliberately plain text (not a slog JSON line) and level-independent so
// it is easy to spot in a terminal and survives GLEIPNIR_LOG_LEVEL=error. The
// structured "server listening" log line is emitted separately for machine
// consumers.
func printReadyBanner(w io.Writer, ver, listenAddr, publicURL string) {
	url, composeHint := resolveDisplayURL(listenAddr, publicURL)

	const rule = "──────────────────────────────────────────────────────────"
	fmt.Fprintf(w, "\n%s\n", rule)
	fmt.Fprintf(w, "  Gleipnir %s is ready.\n\n", ver)
	fmt.Fprintf(w, "  → Open the web UI:  %s\n", url)
	if composeHint {
		fmt.Fprintf(w, "\n  Running under Docker/Podman Compose? The port above is the\n")
		fmt.Fprintf(w, "  container's internal port — open the host port you mapped\n")
		fmt.Fprintf(w, "  instead (default: http://localhost:3000).\n")
	}
	fmt.Fprintf(w, "%s\n\n", rule)
}
