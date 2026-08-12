package matchingsut

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

type variant string

const (
	variantVulnerable variant = "vulnerable"
	variantFixed      variant = "fixed"
)

type repository struct {
	requestQuery string
}

func newRepository(selected variant) (*repository, error) {
	query := `SELECT status
              FROM project_request
             WHERE id = ?
               AND status = 'ACTIVE'`
	switch selected {
	case variantVulnerable:
	case variantFixed:
		query += " FOR UPDATE"
	default:
		return nil, fmt.Errorf("create matching repository: unsupported variant %q", selected)
	}

	return &repository{requestQuery: query}, nil
}

func (r *repository) requestStatus(
	ctx context.Context,
	tx *sql.Tx,
	requestID int64,
) (string, error) {
	var status string
	err := tx.QueryRowContext(ctx, r.requestQuery, requestID).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		if statusErr := tx.QueryRowContext(
			ctx,
			"SELECT status FROM project_request WHERE id = ?",
			requestID,
		).Scan(&status); statusErr != nil {
			return "", fmt.Errorf("read project request %d: %w", requestID, statusErr)
		}
		return status, nil
	}
	if err != nil {
		return "", fmt.Errorf("read project request %d: %w", requestID, err)
	}

	return status, nil
}

func (*repository) hasActiveAssignment(
	ctx context.Context,
	tx *sql.Tx,
	requestID int64,
) (bool, error) {
	var assigned bool
	if err := tx.QueryRowContext(
		ctx,
		`SELECT EXISTS (
             SELECT 1
               FROM assignment
              WHERE project_request_id = ?
                AND status = 'ACTIVE'
         )`,
		requestID,
	).Scan(&assigned); err != nil {
		return false, fmt.Errorf("read active assignment for request %d: %w", requestID, err)
	}

	return assigned, nil
}

func (*repository) insertMatchingSession(
	ctx context.Context,
	tx *sql.Tx,
) (int64, error) {
	result, err := tx.ExecContext(
		ctx,
		"INSERT INTO matching_session (status) VALUES ('ACTIVE')",
	)
	if err != nil {
		return 0, fmt.Errorf("insert matching session: %w", err)
	}

	sessionID, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("read matching session ID: %w", err)
	}

	return sessionID, nil
}

func (*repository) insertAssignment(
	ctx context.Context,
	tx *sql.Tx,
	requestID int64,
	sessionID int64,
) error {
	if _, err := tx.ExecContext(
		ctx,
		`INSERT INTO assignment
            (project_request_id, matching_session_id, status)
         VALUES (?, ?, 'ACTIVE')`,
		requestID,
		sessionID,
	); err != nil {
		return fmt.Errorf("insert assignment for request %d: %w", requestID, err)
	}

	return nil
}
