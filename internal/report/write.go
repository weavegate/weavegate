package report

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/weavegate/weavegate/internal/ci"
	"github.com/weavegate/weavegate/internal/scenario"
)

const (
	dirMode  os.FileMode = 0o755
	fileMode os.FileMode = 0o644
)

// FileNames lists the six run directory artifacts. ManifestFile and
// MergedFile are volatile; the rest are deterministic for identical inputs.
const (
	ManifestFile    = "manifest.json"
	ScenarioFile    = "scenario.json"
	ObservationFile = "observation.json"
	TraceFile       = "trace.json"
	MergedFile      = "report.json"
	MarkdownFile    = "report.md"
)

// FileNames lists the six artifacts in canonical write order.
var FileNames = []string{ManifestFile, ScenarioFile, ObservationFile, TraceFile, MergedFile, MarkdownFile}

// DeterministicFiles are byte-identical across two runs given identical
// config content and CLI flags.
var DeterministicFiles = []string{ScenarioFile, ObservationFile, TraceFile, MarkdownFile}

// WriteRun writes the complete run directory under <base>/runs/<run_id>/ and
// returns that directory's path. It always writes all six files, whether the
// run passed or found a violation, so PASS evidence is never dropped.
//
// Writing uses a same-filesystem temporary directory under <base>/runs/ and
// an atomic rename, so a partial failure never leaves a half-written run
// directory in place.
func WriteRun(base string, run Run) (dir string, returnErr error) {
	defer func() {
		if returnErr != nil {
			returnErr = ci.OutputError(returnErr)
		}
	}()

	if strings.TrimSpace(base) == "" {
		return "", errors.New("write run: base directory is required")
	}
	if strings.TrimSpace(run.Manifest.RunID) == "" {
		return "", errors.New("write run: manifest run ID is required")
	}
	run = normalizeRun(run)

	files, err := renderFiles(run)
	if err != nil {
		return "", fmt.Errorf("write run %q: %w", run.Manifest.RunID, err)
	}

	runsDir := filepath.Join(base, "runs")
	if err := os.MkdirAll(runsDir, dirMode); err != nil {
		return "", fmt.Errorf("write run %q: create runs directory: %w", run.Manifest.RunID, err)
	}

	tempDir := filepath.Join(runsDir, ".tmp-"+run.Manifest.RunID)
	if err := os.RemoveAll(tempDir); err != nil {
		return "", fmt.Errorf("write run %q: clear stale temporary directory: %w", run.Manifest.RunID, err)
	}
	if err := os.Mkdir(tempDir, dirMode); err != nil {
		return "", fmt.Errorf("write run %q: create temporary directory: %w", run.Manifest.RunID, err)
	}
	if err := os.Chmod(tempDir, dirMode); err != nil {
		return "", fmt.Errorf("write run %q: set temporary directory mode: %w", run.Manifest.RunID, err)
	}

	keepTemp := true
	defer func() {
		if keepTemp {
			os.RemoveAll(tempDir)
		}
	}()

	for _, name := range FileNames {
		path := filepath.Join(tempDir, name)
		if err := os.WriteFile(path, files[name], fileMode); err != nil {
			return "", fmt.Errorf("write run %q: write %s: %w", run.Manifest.RunID, name, err)
		}
		if err := os.Chmod(path, fileMode); err != nil {
			return "", fmt.Errorf("write run %q: set %s mode: %w", run.Manifest.RunID, name, err)
		}
	}

	finalDir := filepath.Join(runsDir, run.Manifest.RunID)
	if err := os.RemoveAll(finalDir); err != nil {
		return "", fmt.Errorf("write run %q: clear stale run directory: %w", run.Manifest.RunID, err)
	}
	if err := os.Rename(tempDir, finalDir); err != nil {
		return "", fmt.Errorf("write run %q: replace destination: %w", run.Manifest.RunID, err)
	}
	keepTemp = false

	return finalDir, nil
}

func renderFiles(run Run) (map[string][]byte, error) {
	files := make(map[string][]byte, len(FileNames))

	manifestJSON, err := canonicalJSON(run.Manifest)
	if err != nil {
		return nil, fmt.Errorf("encode manifest: %w", err)
	}
	files[ManifestFile] = manifestJSON

	scenarioJSON, err := canonicalJSON(run.Scenario)
	if err != nil {
		return nil, fmt.Errorf("encode scenario: %w", err)
	}
	files[ScenarioFile] = scenarioJSON

	observationJSON, err := canonicalJSON(run.Observation)
	if err != nil {
		return nil, fmt.Errorf("encode observation: %w", err)
	}
	files[ObservationFile] = observationJSON

	traceJSON, err := canonicalJSON(run.Trace)
	if err != nil {
		return nil, fmt.Errorf("encode trace: %w", err)
	}
	files[TraceFile] = traceJSON

	mergedJSON, err := canonicalJSON(Merged{
		Manifest:    run.Manifest,
		Scenario:    run.Scenario,
		Observation: run.Observation,
	})
	if err != nil {
		return nil, fmt.Errorf("encode merged report: %w", err)
	}
	files[MergedFile] = mergedJSON

	files[MarkdownFile] = []byte(renderMarkdown(run))

	return files, nil
}

func canonicalJSON(value any) ([]byte, error) {
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(encoded, '\n'), nil
}

// normalizeRun replaces nil slices with non-nil empty slices so JSON
// encodes them as [] rather than null.
func normalizeRun(run Run) Run {
	if run.Scenario.Workers == nil {
		run.Scenario.Workers = []scenario.Worker{}
	}
	if run.Scenario.SyncPoints == nil {
		run.Scenario.SyncPoints = []string{}
	}
	if run.Scenario.Params == nil {
		run.Scenario.Params = map[string]string{}
	}
	if run.Observation.AssertionViolations == nil {
		run.Observation.AssertionViolations = []string{}
	}
	if run.Observation.Oracles == nil {
		run.Observation.Oracles = []OracleDeclaration{}
	}
	if run.Trace.Events == nil {
		run.Trace.Events = run.Trace.Events.Clone()
	}
	if run.Trace.Terminals == nil {
		run.Trace.Terminals = run.Trace.Terminals.Clone()
	}
	return run
}
