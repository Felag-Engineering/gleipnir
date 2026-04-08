package openaicompat_test

import (
	"context"
	"testing"

	"github.com/rapp992/gleipnir/internal/admin"
	"github.com/rapp992/gleipnir/internal/llm"
	"github.com/rapp992/gleipnir/internal/llm/openaicompat"
)

// fakeLoaderQuerier returns canned rows and tracks which rows were seen.
type fakeLoaderQuerier struct {
	rows []openaicompat.LoaderRow
	err  error
}

func (f *fakeLoaderQuerier) ListOpenAICompatProvidersForLoader(ctx context.Context) ([]openaicompat.LoaderRow, error) {
	return f.rows, f.err
}

func TestLoadAndRegister_NoRows(t *testing.T) {
	reg := llm.NewProviderRegistry()
	q := &fakeLoaderQuerier{}
	if err := openaicompat.LoadAndRegister(context.Background(), q, []byte("01234567890123456789012345678901"), reg, admin.Decrypt); err != nil {
		t.Fatalf("LoadAndRegister: %v", err)
	}
	if len(reg.Providers()) != 0 {
		t.Errorf("registry should be empty, got %v", reg.Providers())
	}
}

func TestLoadAndRegister_MultipleRows(t *testing.T) {
	key := []byte("01234567890123456789012345678901")
	enc1, _ := admin.Encrypt(key, "sk-one")
	enc2, _ := admin.Encrypt(key, "sk-two")

	reg := llm.NewProviderRegistry()
	q := &fakeLoaderQuerier{rows: []openaicompat.LoaderRow{
		{Name: "openai", BaseURL: "https://api.openai.com/v1", APIKeyEncrypted: enc1},
		{Name: "ollama-local", BaseURL: "http://ollama:11434/v1", APIKeyEncrypted: enc2},
	}}
	if err := openaicompat.LoadAndRegister(context.Background(), q, key, reg, admin.Decrypt); err != nil {
		t.Fatalf("LoadAndRegister: %v", err)
	}
	names := reg.Providers()
	if len(names) != 2 {
		t.Errorf("want 2 providers, got %v", names)
	}
	if _, err := reg.Get("openai"); err != nil {
		t.Errorf("openai not registered: %v", err)
	}
	if _, err := reg.Get("ollama-local"); err != nil {
		t.Errorf("ollama-local not registered: %v", err)
	}
}

func TestLoadAndRegister_CorruptCiphertextRowIsSkipped(t *testing.T) {
	key := []byte("01234567890123456789012345678901")
	enc, _ := admin.Encrypt(key, "sk-good")
	reg := llm.NewProviderRegistry()
	q := &fakeLoaderQuerier{rows: []openaicompat.LoaderRow{
		{Name: "corrupt", BaseURL: "https://api.openai.com/v1", APIKeyEncrypted: "not-valid-ciphertext"},
		{Name: "good", BaseURL: "https://api.openai.com/v1", APIKeyEncrypted: enc},
	}}
	if err := openaicompat.LoadAndRegister(context.Background(), q, key, reg, admin.Decrypt); err != nil {
		t.Fatalf("LoadAndRegister should not abort on corrupt row: %v", err)
	}
	if _, err := reg.Get("good"); err != nil {
		t.Errorf("good row should be registered: %v", err)
	}
	if _, err := reg.Get("corrupt"); err == nil {
		t.Errorf("corrupt row should have been skipped")
	}
}
