package arcade_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/felag-engineering/gleipnir/internal/arcade"
)

// stubArcade starts an httptest.Server that responds to Arcade API paths.
// The handler function receives the request and writes the desired response.
func stubArcade(t *testing.T, handler http.HandlerFunc) (*httptest.Server, *arcade.Client) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	client := arcade.NewClient(srv.Client(), "test-api-key", arcade.WithBaseURL(srv.URL))
	return srv, client
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func TestAuthorize_Completed(t *testing.T) {
	_, client := stubArcade(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]string{"id": "auth-1", "status": "completed"})
	})

	resp, err := client.Authorize(context.Background(), "Gmail.SendEmail", "user@example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Status != "completed" {
		t.Errorf("expected status completed, got %q", resp.Status)
	}
	if resp.ID != "auth-1" {
		t.Errorf("expected id auth-1, got %q", resp.ID)
	}
}

func TestAuthorize_Pending(t *testing.T) {
	_, client := stubArcade(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]string{
			"id":     "auth-2",
			"status": "pending",
			"url":    "https://arcade.dev/oauth",
		})
	})

	resp, err := client.Authorize(context.Background(), "Gmail.SendEmail", "user@example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Status != "pending" {
		t.Errorf("expected status pending, got %q", resp.Status)
	}
	if resp.URL != "https://arcade.dev/oauth" {
		t.Errorf("unexpected URL: %q", resp.URL)
	}
}

func TestAuthorize_CtxCancellation(t *testing.T) {
	_, client := stubArcade(t, func(w http.ResponseWriter, r *http.Request) {
		// Intentionally never responds — context cancels first.
		<-r.Context().Done()
		w.WriteHeader(http.StatusRequestTimeout)
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	_, err := client.Authorize(ctx, "Gmail.SendEmail", "user@example.com")
	if err == nil {
		t.Fatal("expected error from cancelled context, got nil")
	}
}

func TestAuthorize_NonTwoxx(t *testing.T) {
	_, client := stubArcade(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid api key"}`))
	})

	_, err := client.Authorize(context.Background(), "Gmail.SendEmail", "user@example.com")
	if err == nil {
		t.Fatal("expected error for 401 response, got nil")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("expected error to mention status 401, got: %v", err)
	}
	if !strings.Contains(err.Error(), "invalid api key") {
		t.Errorf("expected error to include body excerpt, got: %v", err)
	}
}

// The client caps body reads at maxBodyBytes (64KB) so a misbehaving Arcade
// can't stream unbounded data into our logs, but otherwise we surface the full
// body in error messages — truncating sub-KB caused real bugs (large success
// responses got mangled into "unexpected end of JSON input" errors).
func TestAuthorize_LargeErrorBodyBounded(t *testing.T) {
	_, client := stubArcade(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		// Write much more than the read cap.
		_, _ = w.Write(bytes.Repeat([]byte("x"), 200*1024))
	})

	_, err := client.Authorize(context.Background(), "Gmail.SendEmail", "user@example.com")
	if err == nil {
		t.Fatal("expected error for 500 response, got nil")
	}
	// Error message includes the read body, capped to maxBodyBytes (~64KB).
	// We assert it's bounded but not artificially small.
	if len(err.Error()) > 70*1024 {
		t.Errorf("error string %d chars exceeds maxBodyBytes cap", len(err.Error()))
	}
}

// Arcade returns the full grant payload on completed authorize calls — auth
// id, user_info, scopes array, OAuth context. This routinely exceeds 1KB. The
// client must read the entire body so JSON parsing succeeds; truncating mid-
// JSON produces "unexpected end of JSON input".
func TestAuthorize_LargeSuccessBodyParses(t *testing.T) {
	bigPayload := map[string]any{
		"id":          "ac_xyz",
		"status":      "completed",
		"user_id":     "user@example.com",
		"provider_id": "arcade-google",
		// padding to push the response well past the old 1024-byte cap.
		"scopes": []string{
			"https://www.googleapis.com/auth/calendar.events",
			"https://www.googleapis.com/auth/calendar.readonly",
			"https://www.googleapis.com/auth/gmail.labels",
			"https://www.googleapis.com/auth/gmail.modify",
			"https://www.googleapis.com/auth/gmail.readonly",
			"https://www.googleapis.com/auth/userinfo.email",
			"https://www.googleapis.com/auth/userinfo.profile",
			"openid",
		},
		"context": map[string]any{
			"token": strings.Repeat("a", 800),
			"user_info": map[string]string{
				"email":          "user@example.com",
				"family_name":    "Doe",
				"given_name":     "Jane",
				"id":             "100436629886563732515",
				"name":           "Jane Doe",
				"picture":        "https://lh3.googleusercontent.com/a/" + strings.Repeat("a", 200),
				"verified_email": "true",
			},
		},
	}
	_, client := stubArcade(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, bigPayload)
	})

	resp, err := client.Authorize(context.Background(), "Gmail.SendEmail", "user@example.com")
	if err != nil {
		t.Fatalf("unexpected error parsing large success body: %v", err)
	}
	if resp.Status != "completed" {
		t.Errorf("expected completed, got %q", resp.Status)
	}
	if resp.ID != "ac_xyz" {
		t.Errorf("expected id ac_xyz, got %q", resp.ID)
	}
}

// 2xx with an empty body should be treated as a completed grant — Arcade
// occasionally responds with 204 No Content for already-authorized pairs.
func TestAuthorize_EmptyBodyTreatedAsCompleted(t *testing.T) {
	_, client := stubArcade(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	resp, err := client.Authorize(context.Background(), "Gmail.SendEmail", "user@example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Status != "completed" {
		t.Errorf("expected completed, got %q", resp.Status)
	}
}

func TestWaitForCompletion_ReturnsStatus(t *testing.T) {
	_, client := stubArcade(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.RawQuery, "wait=10") {
			http.Error(w, "missing wait=10", http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]string{"id": "auth-1", "status": "completed"})
	})

	resp, err := client.WaitForCompletion(context.Background(), "auth-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Status != "completed" {
		t.Errorf("expected completed, got %q", resp.Status)
	}
}

func TestWaitForCompletion_ReturnsStillPending(t *testing.T) {
	// WaitForCompletion makes a single call; it may return pending if the user
	// has not yet completed OAuth. The frontend loop is responsible for retrying.
	_, client := stubArcade(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]string{"id": "auth-1", "status": "pending", "url": "https://arcade.dev/oauth"})
	})

	resp, err := client.WaitForCompletion(context.Background(), "auth-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Status != "pending" {
		t.Errorf("expected pending, got %q", resp.Status)
	}
}

func TestWaitForCompletion_CtxCancellation(t *testing.T) {
	_, client := stubArcade(t, func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
		w.WriteHeader(http.StatusRequestTimeout)
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := client.WaitForCompletion(ctx, "auth-1")
	if err == nil {
		t.Fatal("expected error from cancelled context, got nil")
	}
}

func TestWaitForCompletion_URLContainsWait10(t *testing.T) {
	var capturedURL string
	_, client := stubArcade(t, func(w http.ResponseWriter, r *http.Request) {
		capturedURL = r.URL.String()
		writeJSON(w, map[string]string{"id": "auth-1", "status": "completed"})
	})

	_, err := client.WaitForCompletion(context.Background(), "auth-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(capturedURL, "wait=10") {
		t.Errorf("expected URL to contain wait=10, got: %q", capturedURL)
	}
}

// An authID containing reserved characters must be percent-encoded so the
// wait parameter is not lost or the request mis-routed.
func TestWaitForCompletion_AuthIDIsEscaped(t *testing.T) {
	var gotID, gotWait string
	_, client := stubArcade(t, func(w http.ResponseWriter, r *http.Request) {
		gotID = r.URL.Query().Get("id")
		gotWait = r.URL.Query().Get("wait")
		writeJSON(w, map[string]string{"id": gotID, "status": "completed"})
	})

	_, err := client.WaitForCompletion(context.Background(), "weird&id=injected")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotID != "weird&id=injected" {
		t.Errorf("authID round-trip failed: got %q", gotID)
	}
	if gotWait != "10" {
		t.Errorf("wait param lost to query injection: got %q", gotWait)
	}
}
