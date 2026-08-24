package events

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// jsonrpcRequest is the wire shape of an incoming JSON-RPC 2.0 request. ID
// is held as raw JSON (a request id may be a string or a number) so it can
// be echoed back verbatim in a response.
type jsonrpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// jsonrpcResponse is the wire shape of a JSON-RPC 2.0 response.
type jsonrpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *jsonrpcError   `json:"error,omitempty"`
}

type jsonrpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// jsonrpcNotification is the wire shape of a JSON-RPC 2.0 notification —
// no id, so it draws no response (doc §7.1).
type jsonrpcNotification struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params"`
}

// cloudEventWire is the CloudEvents 1.0 envelope events/event delivers
// (doc §7.3). gleipnirseq is the one field this contract coins; every other
// field name is CloudEvents' own.
type cloudEventWire struct {
	SpecVersion string `json:"specversion"`
	Source      string `json:"source"`
	Type        string `json:"type"`
	ID          string `json:"id"`
	Time        string `json:"time"`
	Data        any    `json:"data,omitempty"`
	GleipnirSeq uint64 `json:"gleipnirseq"`
}

// closeResult is the {reason, cursor} result a clean close sends as the
// JSON-RPC response to the original events/listen request id (doc §7.1).
type closeResult struct {
	Reason string `json:"reason"`
	Cursor string `json:"cursor"`
}

// writeJSONRPCResult writes a plain (non-streaming) JSON-RPC success
// response — used by server/discover and events/discover, neither of which
// ever upgrades to SSE.
func writeJSONRPCResult(w http.ResponseWriter, id json.RawMessage, result any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(jsonrpcResponse{JSONRPC: "2.0", ID: id, Result: result})
}

// writeJSONRPCError writes a plain JSON-RPC error response.
func writeJSONRPCError(w http.ResponseWriter, id json.RawMessage, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(jsonrpcResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error:   &jsonrpcError{Code: code, Message: message},
	})
}

// writeCursorUnknownError writes the distinguishable cursor-unknown
// response: a plain JSON-RPC error, not an SSE frame, because the stream
// never opens for a resume request the buffer cannot satisfy — starting it
// anyway and reporting the problem mid-stream would be exactly the silent
// gap this error exists to prevent.
func writeCursorUnknownError(w http.ResponseWriter, id json.RawMessage) {
	writeJSONRPCError(w, id, CodeCursorUnknown, ErrCursorUnknown.Error())
}

// writeSSEFrame writes one SSE "event: message" / "data: ..." frame,
// terminated by the blank line the SSE spec requires between frames.
func writeSSEFrame(w io.Writer, data []byte) {
	fmt.Fprintf(w, "event: message\ndata: %s\n\n", data)
}

// writeHeartbeatComment writes an SSE comment frame: a line beginning with
// ":", content-free and invisible to events/event consumers (doc §7.4).
func writeHeartbeatComment(w io.Writer) {
	fmt.Fprint(w, ": heartbeat\n\n")
}

// writeEventNotification writes one delivered event as an events/event
// JSON-RPC notification, SSE-framed (doc §7.1, §7.3).
func writeEventNotification(w io.Writer, e StoredEvent) {
	env := cloudEventWire{
		SpecVersion: "1.0",
		Source:      e.Source,
		Type:        e.Type,
		ID:          e.ID,
		Time:        e.Time.UTC().Format(time.RFC3339Nano),
		Data:        e.Data,
		GleipnirSeq: e.Seq,
	}
	msg := jsonrpcNotification{JSONRPC: "2.0", Method: methodEventsEvent, Params: env}
	b, err := json.Marshal(msg)
	if err != nil {
		// Data is author-supplied and may not be JSON-marshalable; drop the
		// event rather than corrupt the stream with a half-written frame.
		// The event stays in the buffer for a resumer either way.
		return
	}
	writeSSEFrame(w, b)
}

// writeCloseResult writes a clean-close JSON-RPC response, SSE-framed, to
// the original events/listen request id (doc §7.1).
func writeCloseResult(w io.Writer, id json.RawMessage, reason, cursor string) {
	resp := jsonrpcResponse{
		JSONRPC: "2.0",
		ID:      id,
		Result:  closeResult{Reason: reason, Cursor: cursor},
	}
	b, err := json.Marshal(resp)
	if err != nil {
		return
	}
	writeSSEFrame(w, b)
}
