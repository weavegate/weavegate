package lostupdate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// The workload values provide enough overlap to make lost updates observable.
const (
	workerCount         = 8
	incrementsPerWorker = 100
	readWriteDelay      = 2 * time.Millisecond
)

// TestLostUpdate demonstrates that concurrent unlocked read-modify-write
// transactions can lose updates against a shared MySQL/InnoDB row.
//
// The test starts MySQL 8.4, runs eight workers that each increment the same
// balance 100 times through dedicated connections, and compares the persisted
// balance with the expected total of 800. Each transaction performs an
// unlocked read, waits briefly to widen the overlap window, and writes the
// previously read value plus one.
//
// The vulnerable path passes only when the persisted balance is below 800 and
// the reported lost-update count is positive.
func TestLostUpdate(t *testing.T) {
	t.Run("vulnerable", func(t *testing.T) {
		db := startMySQL(t)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()

		started := time.Now()
		if err := runConcurrentIncrements(ctx, db); err != nil {
			t.Fatalf("run concurrent increments: %v", err)
		}
		elapsed := time.Since(started)

		var observed int
		if err := db.QueryRowContext(
			ctx,
			"SELECT balance FROM account WHERE id = 1",
		).Scan(&observed); err != nil {
			t.Fatalf("read final balance: %v", err)
		}

		expected := workerCount * incrementsPerWorker
		lost := expected - observed

		t.Logf(
			"LOSTUPDATE_RESULT path=vulnerable expected=%d observed=%d lost=%d elapsed=%s",
			expected,
			observed,
			lost,
			elapsed,
		)
		if lost <= 0 {
			t.Fatalf("expected at least one lost update, got %d", lost)
		}
	})
}

// runConcurrentIncrements coordinates workers that increment the same account row.
func runConcurrentIncrements(ctx context.Context, db *sql.DB) error {
	// Release workers together without adding a barrier after their reads.
	start := make(chan struct{})
	errs := make(chan error, workerCount)

	var workers sync.WaitGroup
	workers.Add(workerCount)

	for worker := 0; worker < workerCount; worker++ {
		go func(worker int) {
			defer workers.Done()

			// Keep one dedicated database connection for this worker's transactions.
			conn, err := db.Conn(ctx)
			if err != nil {
				errs <- fmt.Errorf("worker %d: acquire connection: %w", worker, err)
				return
			}
			defer conn.Close()

			select {
			case <-start:
			case <-ctx.Done():
				errs <- fmt.Errorf("worker %d: await start: %w", worker, ctx.Err())
				return
			}

			for increment := 0; increment < incrementsPerWorker; increment++ {
				if err := incrementWithoutLock(ctx, conn); err != nil {
					errs <- fmt.Errorf(
						"worker %d increment %d: %w",
						worker,
						increment,
						err,
					)
					return
				}
			}
		}(worker)
	}

	// Closing the start gate synchronizes launch only; it does not wait for reads.
	close(start)
	workers.Wait()
	close(errs)

	// Each worker reports at most one error, so collection cannot block the workers.
	var workerErrors []error
	for err := range errs {
		workerErrors = append(workerErrors, err)
	}

	return errors.Join(workerErrors...)
}

// incrementWithoutLock performs one vulnerable read-modify-write transaction.
func incrementWithoutLock(ctx context.Context, conn *sql.Conn) error {
	// Keep the read and stale-value write in one explicit transaction.
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()

	// Intentionally use a plain read without SELECT ... FOR UPDATE.
	var balance int
	if err := tx.QueryRowContext(
		ctx,
		"SELECT balance FROM account WHERE id = 1",
	).Scan(&balance); err != nil {
		return fmt.Errorf("read balance: %w", err)
	}

	// Widen the overlap window without acting as a deterministic sync-point.
	time.Sleep(readWriteDelay)

	// Write the previously read absolute value so concurrent updates can overwrite it.
	if _, err := tx.ExecContext(
		ctx,
		"UPDATE account SET balance = ? WHERE id = 1",
		balance+1,
	); err != nil {
		return fmt.Errorf("update balance: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}

	return nil
}
