package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"github.com/spf13/cobra"

	"github.com/weavegate/weavegate/internal/ci"
	"github.com/weavegate/weavegate/internal/report"
)

func newReportCommand(stdout, stderr io.Writer) *cobra.Command {
	var format string
	var out string

	cmd := &cobra.Command{
		Use:           "report [run_id]",
		Short:         "Print a saved run report.",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			runID := ""
			if len(args) == 1 {
				runID = args[0]
			}
			return printReport(stdout, stderr, out, runID, format)
		},
	}

	cmd.Flags().StringVar(&format, "format", "json", "output format: json or md")
	cmd.Flags().StringVar(&out, "out", ".weavegate", "run directory base")

	return cmd
}

// printReport streams a saved run artifact to stdout exactly as it was
// written — never re-rendered — so the printed report can never drift from
// the file it was read from.
func printReport(stdout, stderr io.Writer, out, runID, format string) error {
	var filename string
	switch format {
	case "json":
		filename = report.MergedFile
	case "md":
		filename = report.MarkdownFile
	default:
		return reportCommandFailure(stderr, fmt.Errorf(
			"report: unknown format %q; supported formats: json, md",
			format,
		))
	}

	if runID == "" {
		latest, err := latestRunID(out)
		if err != nil {
			return reportCommandFailure(stderr, fmt.Errorf("report: %w", err))
		}
		runID = latest
	}

	path := filepath.Join(out, "runs", runID, filename)
	content, err := os.ReadFile(path)
	if err != nil {
		return reportCommandFailure(stderr, fmt.Errorf("report: read %q: %w", path, err))
	}

	if _, err := stdout.Write(content); err != nil {
		return reportCommandFailure(stderr, fmt.Errorf("report: write output: %w", err))
	}
	return nil
}

// latestRunID picks the lexicographically greatest run directory name.
// A-6's fixed-width-millisecond run ID makes lexicographic order the same
// as time order, including runs created within the same second, so "most
// recent" needs no separate tie-break.
func latestRunID(out string) (string, error) {
	entries, err := os.ReadDir(filepath.Join(out, "runs"))
	if err != nil {
		return "", fmt.Errorf("list runs under %q: %w", out, err)
	}

	var names []string
	for _, entry := range entries {
		if entry.IsDir() {
			names = append(names, entry.Name())
		}
	}
	if len(names) == 0 {
		return "", fmt.Errorf("no runs found under %q", out)
	}

	sort.Strings(names)
	return names[len(names)-1], nil
}

func reportCommandFailure(stderr io.Writer, err error) error {
	wrapped := ci.InputError(err)
	fmt.Fprintf(stderr, "weavegate: %v\n", wrapped)
	return &exitError{code: ci.ExitInput, err: wrapped}
}
