package main

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/rapp992/gleipnir/internal/llm"
)

// stubLLMClient satisfies llm.LLMClient for tests that don't need real API calls.
type stubLLMClient struct{}

func (s *stubLLMClient) CreateMessage(_ context.Context, _ llm.MessageRequest) (*llm.MessageResponse, error) {
	panic("stubLLMClient.CreateMessage should not be called in registry tests")
}

func (s *stubLLMClient) StreamMessage(_ context.Context, _ llm.MessageRequest) (<-chan llm.MessageChunk, error) {
	panic("stubLLMClient.StreamMessage should not be called in registry tests")
}

func (s *stubLLMClient) ValidateOptions(_ map[string]any) error              { return nil }
func (s *stubLLMClient) ValidateModelName(_ context.Context, _ string) error { return nil }
func (s *stubLLMClient) ListModels(_ context.Context) ([]llm.ModelInfo, error) {
	return nil, nil
}
func (s *stubLLMClient) InvalidateModelCache() {}

func stubFactory(_ context.Context, _ string) (llm.LLMClient, error) {
	return &stubLLMClient{}, nil
}

func failFactory(_ context.Context, _ string) (llm.LLMClient, error) {
	return nil, fmt.Errorf("should not be called")
}

func TestBuildProviderRegistry(t *testing.T) {
	ctx := context.Background()

	cases := []struct {
		name          string
		anthropicKey  string
		googleKey     string
		newAnthropic  providerFactory
		newGoogle     providerFactory
		wantErr       string // non-empty: error must contain this substring
		wantAnthropic bool
		wantGoogle    bool
	}{
		{
			name:          "both keys present",
			anthropicKey:  "sk-ant-test",
			googleKey:     "goog-test",
			newAnthropic:  stubFactory,
			newGoogle:     stubFactory,
			wantAnthropic: true,
			wantGoogle:    true,
		},
		{
			name:          "only anthropic key",
			anthropicKey:  "sk-ant-test",
			googleKey:     "",
			newAnthropic:  stubFactory,
			newGoogle:     failFactory,
			wantAnthropic: true,
			wantGoogle:    false,
		},
		{
			name:          "only google key",
			anthropicKey:  "",
			googleKey:     "goog-test",
			newAnthropic:  failFactory,
			newGoogle:     stubFactory,
			wantAnthropic: false,
			wantGoogle:    true,
		},
		{
			name:         "neither key set",
			anthropicKey: "",
			googleKey:    "",
			newAnthropic: failFactory,
			newGoogle:    failFactory,
			wantErr:      "no LLM providers available",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reg, err := buildProviderRegistry(ctx, tc.anthropicKey, tc.googleKey, tc.newAnthropic, tc.newGoogle)

			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("error %q does not contain %q", err.Error(), tc.wantErr)
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			_, anthropicErr := reg.Get("anthropic")
			_, googleErr := reg.Get("google")

			if tc.wantAnthropic && anthropicErr != nil {
				t.Errorf("expected anthropic provider to be registered, got error: %v", anthropicErr)
			}
			if !tc.wantAnthropic && anthropicErr == nil {
				t.Error("expected anthropic provider to be absent, but Get succeeded")
			}
			if tc.wantGoogle && googleErr != nil {
				t.Errorf("expected google provider to be registered, got error: %v", googleErr)
			}
			if !tc.wantGoogle && googleErr == nil {
				t.Error("expected google provider to be absent, but Get succeeded")
			}
		})
	}
}

func TestBuildProviderRegistry_FactoryError(t *testing.T) {
	ctx := context.Background()
	factoryErr := fmt.Errorf("client init failed")

	errFactory := func(_ context.Context, _ string) (llm.LLMClient, error) {
		return nil, factoryErr
	}

	_, err := buildProviderRegistry(ctx, "sk-ant-test", "", errFactory, failFactory)
	if err == nil {
		t.Fatal("expected error from failing factory, got nil")
	}
	if !strings.Contains(err.Error(), "create anthropic LLM client") {
		t.Errorf("error %q does not contain expected context", err.Error())
	}
}
