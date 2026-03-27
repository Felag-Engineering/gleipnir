package llm

import (
	"context"
	"strings"
	"sync"
	"testing"
)

// stubClient is a minimal LLMClient implementation for registry tests.
// Its methods are never called in these tests; they panic to catch misuse.
type stubClient struct {
	name string
}

func (s *stubClient) CreateMessage(_ context.Context, _ MessageRequest) (*MessageResponse, error) {
	panic("stubClient.CreateMessage should not be called in registry tests")
}

func (s *stubClient) StreamMessage(_ context.Context, _ MessageRequest) (<-chan MessageChunk, error) {
	panic("stubClient.StreamMessage should not be called in registry tests")
}

func (s *stubClient) ValidateOptions(_ map[string]any) error {
	panic("stubClient.ValidateOptions should not be called in registry tests")
}

func TestProviderRegistry_RegisterAndGet(t *testing.T) {
	r := NewProviderRegistry()
	stub := &stubClient{name: "anthropic"}

	r.Register("anthropic", stub)

	got, err := r.Get("anthropic")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if got != stub {
		t.Errorf("expected the registered stub instance, got a different value")
	}
}

func TestProviderRegistry_GetUnknown(t *testing.T) {
	r := NewProviderRegistry()

	_, err := r.Get("nonexistent")
	if err == nil {
		t.Fatal("expected an error for unknown provider, got nil")
	}
	if want := "nonexistent"; !strings.Contains(err.Error(), want) {
		t.Errorf("error %q does not contain provider name %q", err.Error(), want)
	}
}

func TestProviderRegistry_MultipleProviders(t *testing.T) {
	r := NewProviderRegistry()
	anthropic := &stubClient{name: "anthropic"}
	google := &stubClient{name: "google"}

	r.Register("anthropic", anthropic)
	r.Register("google", google)

	gotAnthropic, err := r.Get("anthropic")
	if err != nil {
		t.Fatalf("Get(anthropic): unexpected error: %v", err)
	}
	if gotAnthropic != anthropic {
		t.Errorf("Get(anthropic) returned wrong instance")
	}

	gotGoogle, err := r.Get("google")
	if err != nil {
		t.Fatalf("Get(google): unexpected error: %v", err)
	}
	if gotGoogle != google {
		t.Errorf("Get(google) returned wrong instance")
	}
}

func TestProviderRegistry_DuplicateOverwrites(t *testing.T) {
	r := NewProviderRegistry()
	stub1 := &stubClient{name: "first"}
	stub2 := &stubClient{name: "second"}

	r.Register("anthropic", stub1)
	r.Register("anthropic", stub2)

	got, err := r.Get("anthropic")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != stub2 {
		t.Errorf("expected stub2 after overwrite, got a different instance")
	}
}

func TestProviderRegistry_ConcurrentReads(t *testing.T) {
	r := NewProviderRegistry()
	r.Register("anthropic", &stubClient{name: "anthropic"})

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := r.Get("anthropic")
			if err != nil {
				t.Errorf("concurrent Get failed: %v", err)
			}
		}()
	}
	wg.Wait()
}
