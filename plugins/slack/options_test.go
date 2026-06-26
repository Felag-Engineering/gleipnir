package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	hostv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/host/v1"
	optionsv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/options/v1"
	plugintest "github.com/felag-engineering/gleipnir/plugin-sdk/testing"
)

// setupOptionsService starts an in-process gRPC server for OptionsService backed
// by a fake host and returns an optionsv1 client plus a cleanup function.
// slackBackend is the httptest.Server whose URL+client are injected into
// OptionsService so Slack API calls go to the test server.
func setupOptionsService(t *testing.T, slackBackend *httptest.Server, hostOpts ...plugintest.Option) (optionsv1.ConfigOptionsServiceClient, func()) {
	t.Helper()

	host := plugintest.NewFakeHost(hostOpts...)

	hostLis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen host: %v", err)
	}
	hostSrv := grpc.NewServer()
	host.Register(hostSrv)
	go func() { _ = hostSrv.Serve(hostLis) }()

	hostConn, err := grpc.NewClient(hostLis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial host: %v", err)
	}
	hostClient := hostv1.NewHostServiceClient(hostConn)

	var svc *OptionsService
	if slackBackend != nil {
		svc = NewOptionsService(hostClient, slackBackend.Client(), slackBackend.URL+"/")
	} else {
		svc = NewOptionsService(hostClient, nil, "")
	}

	optLis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen options: %v", err)
	}
	optSrv := grpc.NewServer()
	optionsv1.RegisterConfigOptionsServiceServer(optSrv, svc)
	go func() { _ = optSrv.Serve(optLis) }()

	optConn, err := grpc.NewClient(optLis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial options: %v", err)
	}

	cleanup := func() {
		optConn.Close()
		optSrv.Stop()
		hostConn.Close()
		hostSrv.Stop()
	}
	return optionsv1.NewConfigOptionsServiceClient(optConn), cleanup
}

// channelsPage builds a conversations.list JSON response. channels is the list
// of channel objects, nextCursor is passed through to response_metadata.
func channelsPage(channels []map[string]any, nextCursor string) []byte {
	b, _ := json.Marshal(map[string]any{
		"ok":       true,
		"channels": channels,
		"response_metadata": map[string]any{
			"next_cursor": nextCursor,
		},
	})
	return b
}

// usersPage builds a users.list JSON response.
func usersPage(users []map[string]any) []byte {
	b, _ := json.Marshal(map[string]any{
		"ok":      true,
		"members": users,
	})
	return b
}

// slackCh builds a minimal Slack channel map for channelsPage.
func slackCh(id, name string, isMember, isArchived bool) map[string]any {
	return map[string]any{
		"id":          id,
		"name":        name,
		"is_private":  false,
		"is_archived": isArchived,
		"is_member":   isMember,
	}
}

// slackUser builds a minimal Slack user map for usersPage.
func slackUser(id, name, realName string, isBot, isDeleted bool) map[string]any {
	return map[string]any{
		"id":        id,
		"name":      name,
		"real_name": realName,
		"is_bot":    isBot,
		"deleted":   isDeleted,
		"profile":   map[string]any{"real_name": realName},
	}
}

// TestOptionsListChannelsHappyPath tests that channels are returned with correct
// label, group, value, and disabled fields.
func TestOptionsListChannelsHappyPath(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/conversations.list", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(channelsPage([]map[string]any{
			slackCh("C001", "general", true, false),
			slackCh("C002", "alerts", false, false),
		}, ""))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client, cleanup := setupOptionsService(t, srv, plugintest.WithCredentialsJSON(credsJSON))
	defer cleanup()

	resp, err := client.ListOptions(context.Background(), &optionsv1.ListOptionsRequest{
		Source: "channels",
	})
	if err != nil {
		t.Fatalf("ListOptions: %v", err)
	}

	opts := resp.GetOptions()
	if len(opts) != 2 {
		t.Fatalf("want 2 options, got %d", len(opts))
	}

	// general: joined → enabled, label "#general", group "Joined"
	if opts[0].GetValue() != "C001" {
		t.Errorf("opts[0] value: want C001, got %q", opts[0].GetValue())
	}
	if opts[0].GetLabel() != "#general" {
		t.Errorf("opts[0] label: want #general, got %q", opts[0].GetLabel())
	}
	if opts[0].GetGroup() != "Joined" {
		t.Errorf("opts[0] group: want Joined, got %q", opts[0].GetGroup())
	}
	if opts[0].GetDisabled() {
		t.Error("opts[0] disabled: want false (member channel)")
	}

	// alerts: not joined → disabled, label has "(not joined)" suffix, group "Not joined"
	if opts[1].GetValue() != "C002" {
		t.Errorf("opts[1] value: want C002, got %q", opts[1].GetValue())
	}
	if opts[1].GetLabel() != "#alerts (not joined)" {
		t.Errorf("opts[1] label: want #alerts (not joined), got %q", opts[1].GetLabel())
	}
	if opts[1].GetGroup() != "Not joined" {
		t.Errorf("opts[1] group: want Not joined, got %q", opts[1].GetGroup())
	}
	if !opts[1].GetDisabled() {
		t.Error("opts[1] disabled: want true (non-member channel)")
	}
}

// TestOptionsListChannelsCursorPagination tests that the next_cursor from the
// first page is forwarded in the response.
func TestOptionsListChannelsCursorPagination(t *testing.T) {
	const cursor1 = "dXNlcjpVMDYxTkZUVDI="

	var requestCount int
	mux := http.NewServeMux()
	mux.HandleFunc("/conversations.list", func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		w.Header().Set("Content-Type", "application/json")

		// First call: return one channel + a next_cursor.
		// Second call (cursor provided): return one channel + empty cursor.
		if r.FormValue("cursor") == "" {
			w.Write(channelsPage([]map[string]any{
				slackCh("C001", "general", true, false),
			}, cursor1))
		} else {
			w.Write(channelsPage([]map[string]any{
				slackCh("C002", "random", true, false),
			}, ""))
		}
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client, cleanup := setupOptionsService(t, srv, plugintest.WithCredentialsJSON(credsJSON))
	defer cleanup()

	// First page: no cursor
	resp1, err := client.ListOptions(context.Background(), &optionsv1.ListOptionsRequest{
		Source: "channels",
	})
	if err != nil {
		t.Fatalf("page 1: %v", err)
	}
	if len(resp1.GetOptions()) != 1 {
		t.Errorf("page 1: want 1 option, got %d", len(resp1.GetOptions()))
	}
	if resp1.GetNextCursor() != cursor1 {
		t.Errorf("page 1 next_cursor: want %q, got %q", cursor1, resp1.GetNextCursor())
	}

	// Second page: pass the cursor from page 1 via the cursor field.
	resp2, err := client.ListOptions(context.Background(), &optionsv1.ListOptionsRequest{
		Source: "channels",
		Cursor: cursor1,
	})
	if err != nil {
		t.Fatalf("page 2: %v", err)
	}
	if len(resp2.GetOptions()) != 1 {
		t.Errorf("page 2: want 1 option, got %d", len(resp2.GetOptions()))
	}
	if resp2.GetNextCursor() != "" {
		t.Errorf("page 2 next_cursor: want empty, got %q", resp2.GetNextCursor())
	}
}

// TestOptionsListChannelsQueryFilter tests that the query parameter filters
// channels whose names do not contain the query substring.
func TestOptionsListChannelsQueryFilter(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/conversations.list", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Return both channels regardless; OptionsService filters client-side.
		w.Write(channelsPage([]map[string]any{
			slackCh("C001", "general", true, false),
			slackCh("C002", "alerts", true, false),
			slackCh("C003", "deployments", true, false),
		}, ""))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client, cleanup := setupOptionsService(t, srv, plugintest.WithCredentialsJSON(credsJSON))
	defer cleanup()

	resp, err := client.ListOptions(context.Background(), &optionsv1.ListOptionsRequest{
		Source: "channels",
		Query:  "al",
	})
	if err != nil {
		t.Fatalf("ListOptions: %v", err)
	}

	// Only "general" (contains "al") and "alerts" (contains "al") match.
	// "deployments" does not.
	opts := resp.GetOptions()
	if len(opts) != 2 {
		t.Fatalf("want 2 filtered options, got %d: %v", len(opts), opts)
	}
	names := []string{opts[0].GetLabel(), opts[1].GetLabel()}
	for _, n := range names {
		if n != "#general" && n != "#alerts" {
			t.Errorf("unexpected option label %q in filtered results", n)
		}
	}
}

// TestOptionsListChannelsExcludeArchived tests that archived channels are not returned.
// The ExcludeArchived flag is passed to the Slack API; the fake backend here
// returns only non-archived channels to simulate the API behavior.
func TestOptionsListChannelsExcludeArchived(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/conversations.list", func(w http.ResponseWriter, r *http.Request) {
		// Verify that exclude_archived=true is sent.
		if r.FormValue("exclude_archived") != "true" {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintf(w, `{"ok":false,"error":"exclude_archived not set"}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		// The backend should only return non-archived channels when the flag is set.
		w.Write(channelsPage([]map[string]any{
			slackCh("C001", "general", true, false),
		}, ""))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client, cleanup := setupOptionsService(t, srv, plugintest.WithCredentialsJSON(credsJSON))
	defer cleanup()

	resp, err := client.ListOptions(context.Background(), &optionsv1.ListOptionsRequest{
		Source: "channels",
	})
	if err != nil {
		t.Fatalf("ListOptions: %v", err)
	}
	if len(resp.GetOptions()) != 1 {
		t.Errorf("want 1 non-archived option, got %d", len(resp.GetOptions()))
	}
}

// TestOptionsListChannelsIsMemberAnnotation verifies that non-member channels
// are marked disabled with the "(not joined)" label suffix.
func TestOptionsListChannelsIsMemberAnnotation(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/conversations.list", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(channelsPage([]map[string]any{
			slackCh("C001", "not-a-member", false, false),
		}, ""))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client, cleanup := setupOptionsService(t, srv, plugintest.WithCredentialsJSON(credsJSON))
	defer cleanup()

	resp, err := client.ListOptions(context.Background(), &optionsv1.ListOptionsRequest{
		Source: "channels",
	})
	if err != nil {
		t.Fatalf("ListOptions: %v", err)
	}
	opts := resp.GetOptions()
	if len(opts) != 1 {
		t.Fatalf("want 1 option, got %d", len(opts))
	}
	if !opts[0].GetDisabled() {
		t.Error("non-member channel: want disabled=true")
	}
	if opts[0].GetLabel() != "#not-a-member (not joined)" {
		t.Errorf("label: want %q, got %q", "#not-a-member (not joined)", opts[0].GetLabel())
	}
}

// TestOptionsListUsersHappyPath tests that regular users are returned correctly.
func TestOptionsListUsersHappyPath(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/users.list", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(usersPage([]map[string]any{
			slackUser("U001", "alice", "Alice Smith", false, false),
			slackUser("U002", "bob", "", false, false), // no real_name → falls back to name
		}))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client, cleanup := setupOptionsService(t, srv, plugintest.WithCredentialsJSON(credsJSON))
	defer cleanup()

	resp, err := client.ListOptions(context.Background(), &optionsv1.ListOptionsRequest{
		Source: "users",
	})
	if err != nil {
		t.Fatalf("ListOptions: %v", err)
	}
	opts := resp.GetOptions()
	if len(opts) != 2 {
		t.Fatalf("want 2 users, got %d", len(opts))
	}
	if opts[0].GetValue() != "U001" {
		t.Errorf("opts[0] value: want U001, got %q", opts[0].GetValue())
	}
	if opts[0].GetLabel() != "Alice Smith" {
		t.Errorf("opts[0] label: want Alice Smith, got %q", opts[0].GetLabel())
	}
	// bob has no real_name: falls back to display name
	if opts[1].GetValue() != "U002" {
		t.Errorf("opts[1] value: want U002, got %q", opts[1].GetValue())
	}
	if opts[1].GetLabel() != "bob" {
		t.Errorf("opts[1] label: want bob, got %q", opts[1].GetLabel())
	}
}

// TestOptionsListUsersQueryFilter tests that the query parameter filters users
// by real name and display name.
func TestOptionsListUsersQueryFilter(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/users.list", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(usersPage([]map[string]any{
			slackUser("U001", "alice", "Alice Smith", false, false),
			slackUser("U002", "bob", "Bob Jones", false, false),
			slackUser("U003", "charlie", "Charlie Brown", false, false),
		}))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client, cleanup := setupOptionsService(t, srv, plugintest.WithCredentialsJSON(credsJSON))
	defer cleanup()

	resp, err := client.ListOptions(context.Background(), &optionsv1.ListOptionsRequest{
		Source: "users",
		Query:  "bob",
	})
	if err != nil {
		t.Fatalf("ListOptions: %v", err)
	}
	opts := resp.GetOptions()
	if len(opts) != 1 {
		t.Fatalf("want 1 filtered user, got %d", len(opts))
	}
	if opts[0].GetValue() != "U002" {
		t.Errorf("filtered user value: want U002, got %q", opts[0].GetValue())
	}
}

// TestOptionsListUsersSkipDeletedAndBots tests that deleted users and bots
// are excluded from the results.
func TestOptionsListUsersSkipDeletedAndBots(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/users.list", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(usersPage([]map[string]any{
			slackUser("U001", "alice", "Alice Smith", false, false),      // kept
			slackUser("U002", "ex-employee", "Ex Employee", false, true), // deleted → skip
			slackUser("U003", "mybot", "My Bot", true, false),            // bot → skip
			slackUser("USLACKBOT", "slackbot", "Slackbot", false, false), // USLACKBOT → skip
			slackUser("U004", "carol", "Carol White", false, false),      // kept
		}))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client, cleanup := setupOptionsService(t, srv, plugintest.WithCredentialsJSON(credsJSON))
	defer cleanup()

	resp, err := client.ListOptions(context.Background(), &optionsv1.ListOptionsRequest{
		Source: "users",
	})
	if err != nil {
		t.Fatalf("ListOptions: %v", err)
	}
	opts := resp.GetOptions()
	if len(opts) != 2 {
		t.Fatalf("want 2 non-deleted non-bot users, got %d: %+v", len(opts), opts)
	}
	ids := map[string]bool{opts[0].GetValue(): true, opts[1].GetValue(): true}
	if !ids["U001"] || !ids["U004"] {
		t.Errorf("expected U001 and U004, got %v", ids)
	}
}

// TestOptionsUnknownSourceReturnsInvalidArgument tests that an unknown source
// returns codes.InvalidArgument.
func TestOptionsUnknownSourceReturnsInvalidArgument(t *testing.T) {
	// No Slack backend needed: the dispatch happens before any API call.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected Slack API call to %s", r.URL.Path)
	}))
	defer srv.Close()

	client, cleanup := setupOptionsService(t, srv, plugintest.WithCredentialsJSON(credsJSON))
	defer cleanup()

	_, err := client.ListOptions(context.Background(), &optionsv1.ListOptionsRequest{
		Source: "unknown_source",
	})
	if err == nil {
		t.Fatal("expected error for unknown source, got nil")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got: %v", err)
	}
	if st.Code() != codes.InvalidArgument {
		t.Errorf("code: want InvalidArgument, got %v", st.Code())
	}
}

// TestOptionsMissingCredentialsReturnsUnauthenticated tests that calling
// ListOptions with no Slack credentials returns codes.Unauthenticated.
func TestOptionsMissingCredentialsReturnsUnauthenticated(t *testing.T) {
	// No Slack backend call expected — the error occurs before the API call.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected Slack API call to %s", r.URL.Path)
	}))
	defer srv.Close()

	// Empty credentials JSON — no access_token.
	client, cleanup := setupOptionsService(t, srv, plugintest.WithCredentialsJSON(`{}`))
	defer cleanup()

	_, err := client.ListOptions(context.Background(), &optionsv1.ListOptionsRequest{
		Source: "channels",
	})
	if err == nil {
		t.Fatal("expected Unauthenticated error, got nil")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got: %v", err)
	}
	if st.Code() != codes.Unauthenticated {
		t.Errorf("code: want Unauthenticated, got %v", st.Code())
	}
}

// TestOptionsSlackErrorReturnsInternal tests that a Slack API ok:false response
// propagates as a codes.Internal error.
func TestOptionsSlackErrorReturnsInternal(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/conversations.list", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":false,"error":"not_authed"}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client, cleanup := setupOptionsService(t, srv, plugintest.WithCredentialsJSON(credsJSON))
	defer cleanup()

	_, err := client.ListOptions(context.Background(), &optionsv1.ListOptionsRequest{
		Source: "channels",
	})
	if err == nil {
		t.Fatal("expected error for Slack API failure, got nil")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got: %v", err)
	}
	if st.Code() != codes.Internal {
		t.Errorf("code: want Internal, got %v", st.Code())
	}
}
