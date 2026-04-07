package openai

// Client implements llm.LLMClient against the OpenAI Chat Completions API.
// Additional fields are added in Task 7.
type Client struct{}

// ValidateOptions parses and validates policy YAML options for this provider.
// Empty or nil options are valid.
func (c *Client) ValidateOptions(options map[string]any) error {
	_, err := parseHints(options)
	return err
}
