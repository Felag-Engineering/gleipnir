// Package cmd contains the cobra subcommands for the gleipnir-plugin CLI.
//
// Each subcommand follows the gleipnirctl pattern: the cobra.Command constructor
// (NewXxxCmd) registers flags and calls an extracted runXxx function. Tests
// target runXxx directly without going through the cobra layer.
package cmd
