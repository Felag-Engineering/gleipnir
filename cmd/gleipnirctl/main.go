// Command gleipnirctl is the local admin CLI for Gleipnir.
//
// Usage: gleipnirctl <command> [flags]
package main

import (
	"os"

	"github.com/spf13/cobra"
)

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "gleipnirctl",
		Short:         "Local admin CLI for Gleipnir",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	return root
}

func main() {
	root := newRootCmd()
	root.AddCommand(newRotateKeyCmd())
	root.AddCommand(newResetPasswordCmd())
	if err := root.Execute(); err != nil {
		root.PrintErrln("error:", err)
		os.Exit(1)
	}
}
