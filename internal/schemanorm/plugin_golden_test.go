package schemanorm_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/felag-engineering/gleipnir/internal/schemanorm"
)

// pluginToolsListGoldens are tools/list results that first-party plugin modules
// pin in their own tests. The root module cannot import a plugin's package
// main, so each plugin writes the result it serves to testdata and this test
// holds those bytes to the same canonicalization the host applies at MCP
// discovery. A plugin schema schemanorm rejects would otherwise only surface
// when an operator installed the plugin.
const pluginToolsListGoldens = "../../plugins/*/testdata/tools_list.golden.json"

func TestPluginToolsListSchemasCanonicalize(t *testing.T) {
	paths, err := filepath.Glob(pluginToolsListGoldens)
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(paths) == 0 {
		t.Skip("no plugin tools/list goldens present")
	}

	for _, path := range paths {
		t.Run(filepath.Base(filepath.Dir(filepath.Dir(path))), func(t *testing.T) {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read golden: %v", err)
			}
			var list struct {
				Tools []struct {
					Name        string          `json:"name"`
					InputSchema json.RawMessage `json:"inputSchema"`
				} `json:"tools"`
			}
			if err := json.Unmarshal(data, &list); err != nil {
				t.Fatalf("decode golden: %v", err)
			}
			if len(list.Tools) == 0 {
				t.Fatal("golden lists no tools")
			}
			for _, tool := range list.Tools {
				if _, err := schemanorm.Normalize(tool.InputSchema); err != nil {
					t.Errorf("tool %q: inputSchema rejected by schemanorm: %v", tool.Name, err)
				}
			}
		})
	}
}
