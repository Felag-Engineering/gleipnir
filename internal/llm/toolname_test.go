package llm_test

import (
	"strings"
	"testing"

	"github.com/rapp992/gleipnir/internal/llm"
)

func TestSanitizeToolName(t *testing.T) {
	tests := []struct {
		name         string
		input        string
		allowedExtra string
		want         string
	}{
		// Alphanumeric and underscore pass through for both variants.
		{
			name:         "alphanumeric_passthrough_anthropic",
			input:        "already_valid",
			allowedExtra: "-",
			want:         "already_valid",
		},
		{
			name:         "alphanumeric_passthrough_google",
			input:        "already_valid",
			allowedExtra: "",
			want:         "already_valid",
		},
		// Dots are always replaced.
		{
			name:         "dot_separator_replaced_anthropic",
			input:        "my-server.tool_name",
			allowedExtra: "-",
			want:         "my-server_tool_name",
		},
		{
			name:         "dot_separator_replaced_google",
			input:        "my-server.tool_name",
			allowedExtra: "",
			want:         "my_server_tool_name",
		},
		{
			name:         "multiple_dots_replaced_anthropic",
			input:        "server.tool.with.many.dots",
			allowedExtra: "-",
			want:         "server_tool_with_many_dots",
		},
		{
			name:         "multiple_dots_replaced_google",
			input:        "server.tool.with.many.dots",
			allowedExtra: "",
			want:         "server_tool_with_many_dots",
		},
		// Hyphens are preserved by Anthropic but replaced by Google.
		{
			name:         "hyphen_allowed_anthropic",
			input:        "my-tool-name",
			allowedExtra: "-",
			want:         "my-tool-name",
		},
		{
			name:         "hyphen_replaced_google",
			input:        "my-tool-name",
			allowedExtra: "",
			want:         "my_tool_name",
		},
		// Spaces are always replaced.
		{
			name:         "spaces_replaced_anthropic",
			input:        "server name with spaces",
			allowedExtra: "-",
			want:         "server_name_with_spaces",
		},
		{
			name:         "spaces_replaced_google",
			input:        "server name with spaces",
			allowedExtra: "",
			want:         "server_name_with_spaces",
		},
		// Truncation at 128 characters.
		{
			name:         "truncated_to_128_chars",
			input:        strings.Repeat("a", 200),
			allowedExtra: "",
			want:         strings.Repeat("a", 128),
		},
		// Empty input.
		{
			name:         "empty_string",
			input:        "",
			allowedExtra: "",
			want:         "",
		},
		// All invalid characters become underscores.
		{
			name:         "all_invalid_chars",
			input:        "...",
			allowedExtra: "",
			want:         "___",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := llm.SanitizeToolName(tc.input, tc.allowedExtra)
			if got != tc.want {
				t.Errorf("SanitizeToolName(%q, %q) = %q, want %q", tc.input, tc.allowedExtra, got, tc.want)
			}
		})
	}
}

func TestBuildNameMapping(t *testing.T) {
	t.Run("basic_mapping_with_dots", func(t *testing.T) {
		tools := []llm.ToolDefinition{
			{Name: "server.read"},
			{Name: "server.write"},
		}
		names := llm.BuildNameMapping(tools, "")

		if names.SanitizedToOriginal["server_read"] != "server.read" {
			t.Errorf("SanitizedToOriginal[server_read] = %q, want %q", names.SanitizedToOriginal["server_read"], "server.read")
		}
		if names.OriginalToSanitized["server.read"] != "server_read" {
			t.Errorf("OriginalToSanitized[server.read] = %q, want %q", names.OriginalToSanitized["server.read"], "server_read")
		}
		if names.SanitizedToOriginal["server_write"] != "server.write" {
			t.Errorf("SanitizedToOriginal[server_write] = %q, want %q", names.SanitizedToOriginal["server_write"], "server.write")
		}
	})

	t.Run("empty_tools_returns_empty_maps", func(t *testing.T) {
		names := llm.BuildNameMapping(nil, "")
		if len(names.SanitizedToOriginal) != 0 {
			t.Errorf("expected empty SanitizedToOriginal, got %v", names.SanitizedToOriginal)
		}
		if len(names.OriginalToSanitized) != 0 {
			t.Errorf("expected empty OriginalToSanitized, got %v", names.OriginalToSanitized)
		}
	})

	t.Run("collision_overwrites_silently_last_wins", func(t *testing.T) {
		// "my.tool" and "my_tool" both sanitize to "my_tool".
		tools := []llm.ToolDefinition{
			{Name: "my.tool"},
			{Name: "my_tool"},
		}
		names := llm.BuildNameMapping(tools, "")

		// The last tool ("my_tool") overwrites the first.
		if names.SanitizedToOriginal["my_tool"] != "my_tool" {
			t.Errorf("SanitizedToOriginal[my_tool] = %q, want %q", names.SanitizedToOriginal["my_tool"], "my_tool")
		}
	})

	t.Run("allowedExtra_preserved_for_anthropic", func(t *testing.T) {
		tools := []llm.ToolDefinition{
			{Name: "my-server.tool"},
		}
		names := llm.BuildNameMapping(tools, "-")

		if names.SanitizedToOriginal["my-server_tool"] != "my-server.tool" {
			t.Errorf("SanitizedToOriginal[my-server_tool] = %q, want %q", names.SanitizedToOriginal["my-server_tool"], "my-server.tool")
		}
	})
}
