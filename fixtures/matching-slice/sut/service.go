package matchingsut

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

const (
	AfterReadRequest       = "after_read_request"
	BeforeInsertAssignment = "before_insert_assignment"
)

var (
	ErrRequestNotFound = errors.New("project request not found")
	ErrRequestInactive = errors.New("project request is not active")
)

// SyncPoint is the matching workflow's consumer-side coordination seam.
type SyncPoint interface {
	Arrive(ctx context.Context, workerID string, point string) error
}

// NoopSyncPoint leaves production command execution uncontrolled.
type NoopSyncPoint struct{}

func (NoopSyncPoint) Arrive(context.Context, string, string) error {
	return nil
}

type service struct {
	repository *repository
	syncPoint  SyncPoint
}

func (s *service) assign(
	ctx context.Context,
	workerID string,
	conn *sql.Conn,
	requestID int64,
) (returnErr error) {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin assignment transaction: %w", err)
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			returnErr = errors.Join(
				returnErr,
				fmt.Errorf("rollback assignment transaction: %w", rollbackErr),
			)
		}
	}()

	status, err := s.repository.requestStatus(ctx, tx, requestID)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("assign request %d: %w", requestID, ErrRequestNotFound)
	}
	if err != nil {
		return fmt.Errorf("assign request %d: %w", requestID, err)
	}
	if status != "ACTIVE" {
		return fmt.Errorf("assign request %d with status %q: %w", requestID, status, ErrRequestInactive)
	}

	if err := s.syncPoint.Arrive(ctx, workerID, AfterReadRequest); err != nil {
		return fmt.Errorf("worker %q sync-point %q: %w", workerID, AfterReadRequest, err)
	}

	alreadyAssigned, err := s.repository.hasActiveAssignment(ctx, tx, requestID)
	if err != nil {
		return fmt.Errorf("assign request %d: %w", requestID, err)
	}
	if alreadyAssigned {
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit already-assigned request %d: %w", requestID, err)
		}
		committed = true
		return nil
	}

	sessionID, err := s.repository.insertMatchingSession(ctx, tx)
	if err != nil {
		return fmt.Errorf("assign request %d: %w", requestID, err)
	}
	if err := s.syncPoint.Arrive(ctx, workerID, BeforeInsertAssignment); err != nil {
		return fmt.Errorf("worker %q sync-point %q: %w", workerID, BeforeInsertAssignment, err)
	}
	if err := s.repository.insertAssignment(ctx, tx, requestID, sessionID); err != nil {
		return fmt.Errorf("assign request %d: %w", requestID, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit assignment for request %d: %w", requestID, err)
	}
	committed = true

	return nil
}
