// Package ci maps a run's outcome to the weavegate process exit code. It is
// a pure policy package: it classifies errors and a verdict, and depends on
// neither the orchestrator nor the config package.
package ci

import "errors"

// Process exit codes. See docs/reference/exit-codes.md for the full contract.
const (
	ExitOK          = 0
	ExitViolation   = 2
	ExitFlaky       = 3
	ExitFixture     = 4
	ExitInput       = 5
	ExitInterrupted = 130
)

type interruptedError struct{ err error }

func (e *interruptedError) Error() string { return e.err.Error() }
func (e *interruptedError) Unwrap() error { return e.err }

type fixtureError struct{ err error }

func (e *fixtureError) Error() string { return e.err.Error() }
func (e *fixtureError) Unwrap() error { return e.err }

type inputError struct{ err error }

func (e *inputError) Error() string { return e.err.Error() }
func (e *inputError) Unwrap() error { return e.err }

type outputError struct{ err error }

func (e *outputError) Error() string { return e.err.Error() }
func (e *outputError) Unwrap() error { return e.err }

// FixtureError marks err as a fixture or container failure (exit 4).
func FixtureError(err error) error {
	if err == nil {
		return nil
	}
	return &fixtureError{err: err}
}

// InputError marks err as a config, adapter, or assertion failure (exit 5).
func InputError(err error) error {
	if err == nil {
		return nil
	}
	return &inputError{err: err}
}

// OutputError marks err as an artifact I/O failure (exit 5).
func OutputError(err error) error {
	if err == nil {
		return nil
	}
	return &outputError{err: err}
}

// InterruptedError marks cancellation caused by SIGINT or SIGTERM (exit 130).
func InterruptedError(err error) error {
	if err == nil {
		return nil
	}
	return &interruptedError{err: err}
}

// Verdict is the run outcome once execution has completed without error.
type Verdict struct {
	Violations    int
	Flaky         bool
	CleanupFailed bool
}

// ExitCode resolves a run outcome into a process exit code. Priority is
// error > flaky > violation > pass. CleanupFailed never lowers an already
// decided code; it only raises a passing verdict (0) to 4, so a leaked
// fixture is never reported as PASS and a real violation is never hidden by
// a teardown failure.
func ExitCode(err error, verdict Verdict) int {
	if err != nil {
		var interrupted *interruptedError
		if errors.As(err, &interrupted) {
			return ExitInterrupted
		}
		var fixtureErr *fixtureError
		if errors.As(err, &fixtureErr) {
			return ExitFixture
		}
		// input and output errors, and any unclassified error, share exit 5.
		return ExitInput
	}

	switch {
	case verdict.Flaky:
		return ExitFlaky
	case verdict.Violations > 0:
		return ExitViolation
	case verdict.CleanupFailed:
		return ExitFixture
	default:
		return ExitOK
	}
}
