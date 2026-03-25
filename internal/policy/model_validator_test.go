package policy

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// statusTransport returns a fixed HTTP status code with a minimal JSON body
// for every request. MaxRetries(0) on the client prevents retry loops.
type statusTransport struct {
	statusCode int
}

func (t *statusTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	body := `{"type":"error","error":{"type":"authentication_error","message":"invalid api key"}}`
	return &http.Response{
		StatusCode: t.statusCode,
		Body:       io.NopCloser(bytes.NewBufferString(body)),
		Header:     make(http.Header),
		Request:    req,
	}, nil
}

func newClientWithStatus(statusCode int) *anthropic.Client {
	client := anthropic.NewClient(
		option.WithHTTPClient(&http.Client{Transport: &statusTransport{statusCode: statusCode}}),
		option.WithAPIKey("fake-key"),
		option.WithMaxRetries(0),
	)
	return &client
}

func TestAnthropicModelValidator_401_ProducesAuthWarning(t *testing.T) {
	client := newClientWithStatus(http.StatusUnauthorized)
	v := NewAnthropicModelValidator(client)

	err := v.ValidateModel(context.Background(), "claude-sonnet-4-5")
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	msg := err.Error()
	if !strings.Contains(msg, "not configured or invalid") {
		t.Errorf("expected auth message, got: %s", msg)
	}
	if !strings.Contains(msg, "401") {
		t.Errorf("expected HTTP 401 in message, got: %s", msg)
	}
	if !strings.Contains(msg, "claude-sonnet-4-5") {
		t.Errorf("expected model ID in message, got: %s", msg)
	}
}

func TestAnthropicModelValidator_403_ProducesAuthWarning(t *testing.T) {
	client := newClientWithStatus(http.StatusForbidden)
	v := NewAnthropicModelValidator(client)

	err := v.ValidateModel(context.Background(), "claude-sonnet-4-5")
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	msg := err.Error()
	if !strings.Contains(msg, "not configured or invalid") {
		t.Errorf("expected auth message, got: %s", msg)
	}
	if !strings.Contains(msg, "403") {
		t.Errorf("expected HTTP 403 in message, got: %s", msg)
	}
}

func TestAnthropicModelValidator_500_ProducesGenericWarning(t *testing.T) {
	client := newClientWithStatus(http.StatusInternalServerError)
	v := NewAnthropicModelValidator(client)

	err := v.ValidateModel(context.Background(), "claude-sonnet-4-5")
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	msg := err.Error()
	// Should not claim the key is invalid — it's a server error.
	if strings.Contains(msg, "not configured or invalid") {
		t.Errorf("500 error should not produce auth message, got: %s", msg)
	}
	if !strings.Contains(msg, "could not verify model") {
		t.Errorf("expected generic verify message, got: %s", msg)
	}
}

func TestAnthropicModelValidator_Success(t *testing.T) {
	// 200 with a minimal model response body.
	body := `{"id":"claude-sonnet-4-5","type":"model","display_name":"Claude Sonnet 4.5","created_at":1234567890}`
	transport := &fixedResponseTransport{body: body, statusCode: http.StatusOK}
	client := anthropic.NewClient(
		option.WithHTTPClient(&http.Client{Transport: transport}),
		option.WithAPIKey("fake-key"),
		option.WithMaxRetries(0),
	)

	v := NewAnthropicModelValidator(&client)
	err := v.ValidateModel(context.Background(), "claude-sonnet-4-5")
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}
}

type fixedResponseTransport struct {
	body       string
	statusCode int
}

func (t *fixedResponseTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: t.statusCode,
		Body:       io.NopCloser(bytes.NewBufferString(t.body)),
		Header: http.Header{
			"Content-Type": []string{"application/json"},
		},
		Request: req,
	}, nil
}
