package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/felag-engineering/gleipnir/plugin-sdk/hostclient"
	"github.com/slack-go/slack"
)

// defaultSlackAPIURL is the real Slack Web API. Egress, not this constant, is
// what bounds where the plugin can actually connect.
const defaultSlackAPIURL = "https://slack.com/api/"

// credentialSource is the slice of hostclient.Client the Slack client needs.
type credentialSource interface {
	GetCredentials(ctx context.Context) (*hostclient.GetCredentialsResponse, error)
}

// storedCredentials mirrors the part of the host's OAuth credential blob this
// plugin reads. Unknown fields are ignored.
type storedCredentials struct {
	Token struct {
		AccessToken string `json:"access_token"`
	} `json:"token"`
}

// slackProvider hands out a Slack client for the instance's current token.
//
// Credentials are fetched on every call because the host rotates OAuth tokens
// underneath the plugin. The client built from them is cached by token, so a
// steady token costs one cheap host call per tool call and a rotated token
// rebuilds the client on its next use, without a restart or a change
// notification.
type slackProvider struct {
	creds      credentialSource
	apiURL     string
	httpClient *http.Client

	mu          sync.Mutex
	cachedToken string
	cached      *slack.Client
}

// normalizeAPIURL gives apiURL the trailing slash slack-go requires, since it
// appends method names to the base verbatim.
func normalizeAPIURL(apiURL string) string {
	if apiURL == "" {
		return defaultSlackAPIURL
	}
	if !strings.HasSuffix(apiURL, "/") {
		return apiURL + "/"
	}
	return apiURL
}

func newSlackProvider(creds credentialSource, apiURL string, httpClient *http.Client) *slackProvider {
	return &slackProvider{creds: creds, apiURL: normalizeAPIURL(apiURL), httpClient: httpClient}
}

// client returns a Slack client for the current token, or errNoCredentials
// when none is configured.
func (p *slackProvider) client(ctx context.Context) (*slack.Client, error) {
	resp, err := p.creds.GetCredentials(ctx)
	if err != nil {
		return nil, fmt.Errorf("get credentials: %w", err)
	}
	if resp.CredentialsJSON == "" || resp.CredentialsJSON == "{}" {
		return nil, errNoCredentials
	}
	var creds storedCredentials
	if err := json.Unmarshal([]byte(resp.CredentialsJSON), &creds); err != nil {
		return nil, fmt.Errorf("parse credentials: %w", err)
	}
	token := creds.Token.AccessToken
	if token == "" {
		return nil, errNoCredentials
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cached == nil || p.cachedToken != token {
		opts := []slack.Option{slack.OptionAPIURL(p.apiURL)}
		if p.httpClient != nil {
			opts = append(opts, slack.OptionHTTPClient(p.httpClient))
		}
		p.cached = slack.New(token, opts...)
		p.cachedToken = token
	}
	return p.cached, nil
}
