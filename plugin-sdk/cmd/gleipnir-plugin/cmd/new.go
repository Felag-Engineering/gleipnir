package cmd

import (
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/felag-engineering/gleipnir/plugin-sdk/cmd/gleipnir-plugin/internal/scaffold"
)

// NewNewCmd returns the cobra.Command for the `new` subcommand.
func NewNewCmd() *cobra.Command {
	var kind, dir, module, sdkReplace string

	cmd := &cobra.Command{
		Use:   "new <name>",
		Short: "Scaffold a new Gleipnir plugin project",
		Long: `Scaffold a new Gleipnir plugin project in a subdirectory named <name>.

The generated project includes main.go, manifest.go, service.go, service_test.go,
go.mod, Makefile, README.md, and .gitignore.

Use --kind to select the plugin capability surface:
  tool     (default) — ToolService with one example tool
  channel             — ChannelService with Notify + Request stubs
  trigger             — TriggerService with one EmitEvent example
  combo               — all three services (kitchen-sink)

No signing key is generated or committed. See .gitignore for excluded patterns.

Use --sdk-replace to add a go.mod replace directive pointing to a local
plugin-sdk checkout. This is only needed for local development before the SDK
is published. Remove the replace directive before distributing the plugin.

See docs/developer/plugin-system-spec.md §14.6 for the full scaffold specification.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runNew(args[0], kind, dir, module, sdkReplace, cmd)
		},
	}

	cmd.Flags().StringVar(&kind, "kind", "tool", "plugin kind: tool, channel, trigger, combo")
	cmd.Flags().StringVar(&dir, "dir", "", "output directory (default: ./<name>)")
	cmd.Flags().StringVar(&module, "module", "", `Go module path for go.mod (default: example.com/<name>)`)
	cmd.Flags().StringVar(&sdkReplace, "sdk-replace", "", "local filesystem path to plugin-sdk; adds a replace directive to go.mod (for local dev only)")

	return cmd
}

// runNew implements the `new` subcommand logic. It is extracted so tests can
// call it directly without going through cobra.
func runNew(name, kind, dir, module, sdkReplace string, cmd *cobra.Command) error {
	if kind == "" {
		kind = "tool"
	}
	if dir == "" {
		dir = filepath.Join(".", name)
	}

	if err := scaffold.Generate(scaffold.Opts{
		Name:       name,
		Kind:       kind,
		Dir:        dir,
		Module:     module,
		SDKReplace: sdkReplace,
	}); err != nil {
		return err
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Created plugin %q in %s\n", name, dir)
	fmt.Fprintf(cmd.OutOrStdout(), "\nNext steps:\n")
	fmt.Fprintf(cmd.OutOrStdout(), "  cd %s\n", dir)
	fmt.Fprintf(cmd.OutOrStdout(), "  go mod tidy\n")
	fmt.Fprintf(cmd.OutOrStdout(), "  make build\n")
	fmt.Fprintf(cmd.OutOrStdout(), "  make gen-manifest\n")
	return nil
}
