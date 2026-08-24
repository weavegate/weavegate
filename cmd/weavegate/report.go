package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/spf13/cobra"

	"github.com/weavegate/weavegate/internal/ci"
	"github.com/weavegate/weavegate/internal/report"
)

var runIDPattern = regexp.MustCompile(`^run_[0-9]{8}T[0-9]{6}\.[0-9]{9}Z_[0-9a-f]{32}$`)

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
	} else if !validRunID(runID) {
		return reportCommandFailure(stderr, fmt.Errorf("report: invalid run ID %q", runID))
	}

	path := filepath.Join(out, "runs", runID, filename)
	content, err := os.ReadFile(path)
	if err != nil {
		return reportCommandFailure(stderr, fmt.Errorf("report: read %q: %w", path, err))
	}

	if err := writeAll(stdout, content); err != nil {
		return reportCommandFailure(stderr, fmt.Errorf("report: write output: %w", err))
	}
	return nil
}

// latestRunID selects the valid, complete run whose manifest.started_at is
// greatest. Run IDs are opaque and serve only as a deterministic tie-break
// for identical timestamps.
func latestRunID(out string) (string, error) {
	entries, err := os.ReadDir(filepath.Join(out, "runs"))
	if err != nil {
		return "", fmt.Errorf("list runs under %q: %w", out, err)
	}

	var selected string
	var selectedAt time.Time
	for _, entry := range entries {
		name := entry.Name()
		if !entry.IsDir() || !validRunID(name) {
			continue
		}
		runDir := filepath.Join(out, "runs", name)
		if !completeRunDirectory(runDir) {
			continue
		}
		content, err := os.ReadFile(filepath.Join(runDir, report.ManifestFile))
		if err != nil {
			continue
		}
		var manifest report.Manifest
		if err := json.Unmarshal(content, &manifest); err != nil || manifest.RunID != name || manifest.StartedAt.IsZero() {
			continue
		}
		if selected == "" || manifest.StartedAt.After(selectedAt) ||
			(manifest.StartedAt.Equal(selectedAt) && name > selected) {
			selected = name
			selectedAt = manifest.StartedAt
		}
	}
	if selected == "" {
		return "", fmt.Errorf("no runs found under %q", out)
	}
	return selected, nil
}

func validRunID(value string) bool {
	return runIDPattern.MatchString(value)
}

func completeRunDirectory(dir string) bool {
	for _, name := range []string{
		report.ManifestFile,
		report.ScenarioFile,
		report.ObservationFile,
		report.TraceFile,
		report.MarkdownFile,
		report.MergedFile,
	} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil || !info.Mode().IsRegular() {
			return false
		}
	}
	return true
}

func reportCommandFailure(stderr io.Writer, err error) error {
	wrapped := ci.InputError(err)
	_, _ = fmt.Fprintf(stderr, "weavegate: %v\n", wrapped)
	return &exitError{err: wrapped}
}
