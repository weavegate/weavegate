package main

import (
	"errors"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/weavegate/weavegate/internal/ci"
)

// version is the reported weavegate version. It is overridden at build time
// with -ldflags "-X main.version=...".
var version = "0.0.0-dev"

// exitError carries a decided process exit code through cobra's error-based
// control flow. A subcommand whose outcome is a verdict rather than a
// malformed invocation — a violation, a flaky determinism check, a passing
// run — reports it this way instead of through Cobra's own error path,
// which exists for usage problems. err, when non-nil, is the underlying
// cause; it has already been reported to stderr by the command that built
// this value.
type exitError struct {
	code int
	err  error
}

func (e *exitError) Error() string {
	if e.err != nil {
		return e.err.Error()
	}
	return fmt.Sprintf("exit %d", e.code)
}

func (e *exitError) Unwrap() error { return e.err }

// exitCodeFromError resolves any error returned by root.Execute() (or by
// runScenario directly, in tests) into a process exit code, preferring an
// already-decided exitError over the generic input-error fallback.
func exitCodeFromError(err error) int {
	if err == nil {
		return ci.ExitOK
	}

	var exit *exitError
	if errors.As(err, &exit) {
		return exit.code
	}

	return ci.ExitCode(ci.InputError(err), ci.Verdict{})
}

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
		// A subcommand ("run", "report", ...) is matched by Cobra before this
		// validator runs; anything left unmatched is reported as an unknown
		// command.
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	root.SetOut(stdout)
	root.SetErr(stderr)
	root.SetVersionTemplate("{{.Version}}\n")

	root.AddCommand(newRunCommand(stdout, stderr))

	return root
}

// Execute runs the root command with args and returns the process exit
// code. A subcommand that reached a verdict reports its exit code through
// exitError; any other Cobra-level failure — an unknown command, an unknown
// flag, invalid usage — is a configuration-shaped problem (exit 5), not a
// fixture or determinism failure.
func Execute(args []string, stdout, stderr io.Writer) int {
	root := newRootCommand(stdout, stderr)
	root.SetArgs(args)

	err := root.Execute()
	if err == nil {
		return ci.ExitOK
	}

	var exit *exitError
	if !errors.As(err, &exit) {
		fmt.Fprintf(stderr, "weavegate: %v\n", err)
	}
	return exitCodeFromError(err)
}
