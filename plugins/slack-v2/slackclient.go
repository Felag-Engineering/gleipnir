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

// token returns the instance's current bot token, or errNoCredentials when
// none is configured.
func (p *slackProvider) token(ctx context.Context) (string, error) {
	resp, err := p.creds.GetCredentials(ctx)
	if err != nil {
		return "", fmt.Errorf("get credentials: %w", err)
	}
	if resp.CredentialsJSON == "" || resp.CredentialsJSON == "{}" {
		return "", errNoCredentials
	}
	var creds storedCredentials
	if err := json.Unmarshal([]byte(resp.CredentialsJSON), &creds); err != nil {
		return "", fmt.Errorf("parse credentials: %w", err)
	}
	if creds.Token.AccessToken == "" {
		return "", errNoCredentials
	}
	return creds.Token.AccessToken, nil
}

// socketAPI builds an uncached Slack client carrying both the current bot
// token and the app-level token, which is what a Socket Mode connection needs.
func (p *slackProvider) socketAPI(ctx context.Context, appToken string) (*slack.Client, error) {
	token, err := p.token(ctx)
	if err != nil {
		return nil, err
	}
	opts := []slack.Option{slack.OptionAPIURL(p.apiURL), slack.OptionAppLevelToken(appToken)}
	if p.httpClient != nil {
		opts = append(opts, slack.OptionHTTPClient(p.httpClient))
	}
	return slack.New(token, opts...), nil
}

// client returns a Slack client for the current token, or errNoCredentials
// when none is configured.
func (p *slackProvider) client(ctx context.Context) (*slack.Client, error) {
	token, err := p.token(ctx)
	if err != nil {
		return nil, err
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
