// Command gleipnir-plugin is the developer CLI for Gleipnir plugin authors.
//
// Usage: gleipnir-plugin <command> [flags]
package main

import (
	"os"

	"github.com/spf13/cobra"

	plugincmd "github.com/felag-engineering/gleipnir/plugin-sdk/cmd/gleipnir-plugin/cmd"
)

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "gleipnir-plugin",
		Short: "Developer CLI for Gleipnir plugin authors",
		Long: `gleipnir-plugin provides scaffolding, signing, packaging, and local
development tooling for Gleipnir plugin authors.

See plugin-sdk/cmd/gleipnir-plugin/README.md for usage, and
docs/developer/plugin-system-spec.md §14.2 for the full subcommand reference.`,
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	root.AddCommand(plugincmd.NewNewCmd())
	root.AddCommand(plugincmd.NewGenManifestCmd())
	root.AddCommand(plugincmd.NewValidateCmd())
	root.AddCommand(plugincmd.NewKeygenCmd())
	root.AddCommand(plugincmd.NewSignCmd())
	root.AddCommand(plugincmd.NewPackageCmd())

	return root
}

func main() {
	root := newRootCmd()
	if err := root.Execute(); err != nil {
		root.PrintErrln("error:", err)
		os.Exit(1)
	}
}
