package main

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/weavegate/weavegate/internal/ci"
)

// version is the reported weavegate version. It is overridden at build time
// with -ldflags "-X main.version=...".
var version = "0.0.0-dev"

// newRootCommand builds the root command bound to stdout and stderr. Results
// meant for a human or a script go to stdout; diagnostics go to stderr.
// Cobra's automatic usage dump is suppressed on error so a diagnostic tool
// does not bury its one-line error under a usage listing.
func newRootCommand(stdout, stderr io.Writer) *cobra.Command {
	root := &cobra.Command{
		Use:           "weavegate",
		Short:         "Reach a verdict on a concurrent workflow and save the evidence.",
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       version,
		// No positional argument is meaningful directly on the root command.
		// A future subcommand ("run", "report", ...) is matched by Cobra
		// before this validator runs; anything left unmatched is reported as
		// an unknown command.
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	root.SetOut(stdout)
	root.SetErr(stderr)
	root.SetVersionTemplate("{{.Version}}\n")
	return root
}

// Execute runs the root command with args and returns its outcome, wrapped
// as an input error (exit 5) on failure: an unknown command, an unknown
// flag, or invalid usage are all configuration-shaped problems, not fixture
// or determinism failures.
func Execute(args []string, stdout, stderr io.Writer) error {
	root := newRootCommand(stdout, stderr)
	root.SetArgs(args)

	if err := root.Execute(); err != nil {
		fmt.Fprintf(stderr, "weavegate: %v\n", err)
		return ci.InputError(err)
	}
	return nil
}
