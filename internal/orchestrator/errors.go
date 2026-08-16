package orchestrator

// FixtureError marks err as originating from the Fixture — a container or
// database reset failure — during a run, as distinct from an adapter,
// Oracle, or scenario-level failure. A caller mapping the outcome to a
// process exit code uses errors.As to recover this classification, without
// this package importing cmd/weavegate's internal/ci: this package has no
// notion of exit codes, only of where in the run an error originated.
type FixtureError struct {
	err error
}

func (e *FixtureError) Error() string { return e.err.Error() }
func (e *FixtureError) Unwrap() error { return e.err }

// NewFixtureError wraps err as a FixtureError, or returns nil for a nil err.
func NewFixtureError(err error) error {
	if err == nil {
		return nil
	}
	return &FixtureError{err: err}
}
