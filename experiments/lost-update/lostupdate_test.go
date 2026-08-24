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

// The read query is the only behavioral difference between the two paths.
const (
	vulnerableReadQuery = "SELECT balance FROM account WHERE id = 1"
	fixedReadQuery      = "SELECT balance FROM account WHERE id = 1 FOR UPDATE"
)

// workloadResult captures the expected and persisted outcome of one workload.
type workloadResult struct {
	expected int
	observed int
	lost     int
	elapsed  time.Duration
}

// TestLostUpdate compares unlocked and locking read-modify-write transactions
// against a shared MySQL/InnoDB row.
//
// Overview:
//
// Each path starts MySQL 8.4 and runs eight workers that increment the same
// balance 100 times through dedicated connections. The vulnerable path uses a
// plain read and must lose at least one of the 800 attempted increments. The
// fixed path uses SELECT ... FOR UPDATE to serialize each read-modify-write
// transaction and must persist all 800 increments without loss.
//
// Both paths report their expected, observed, lost, and elapsed values through
// a stable LOSTUPDATE_RESULT marker.
func TestLostUpdate(t *testing.T) {
	// Run the unlocked path and confirm that it loses updates.
	t.Run("vulnerable", func(t *testing.T) {
		result := runLostUpdateWorkload(t, vulnerableReadQuery)

		// Record the vulnerable result and enforce a positive lost count.
		t.Logf(
			"LOSTUPDATE_RESULT path=vulnerable expected=%d observed=%d lost=%d elapsed=%s",
			result.expected,
			result.observed,
			result.lost,
			result.elapsed,
		)
		if result.lost <= 0 {
			t.Fatalf("expected at least one lost update, got %d", result.lost)
		}
	})

	// Run the locking path and confirm that it preserves every update.
	t.Run("fixed", func(t *testing.T) {
		result := runLostUpdateWorkload(t, fixedReadQuery)

		// Record the fixed result and enforce a zero lost count.
		t.Logf(
			"LOSTUPDATE_RESULT path=fixed expected=%d observed=%d lost=%d elapsed=%s",
			result.expected,
			result.observed,
			result.lost,
			result.elapsed,
		)
		if result.lost != 0 {
			t.Fatalf("expected no lost updates, got %d", result.lost)
		}
	})
}

// runLostUpdateWorkload runs one path against a fresh database and measures it.
//
// It creates an isolated database, executes the shared workload with the given
// read query, reads the persisted balance, and returns the comparison result.
func runLostUpdateWorkload(t *testing.T, readQuery string) workloadResult {
	t.Helper()

	// Start with a clean account row and bound the workload duration.
	db := startMySQL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// Run the workers and measure only their concurrent SQL workload.
	started := time.Now()
	if err := runConcurrentIncrements(ctx, db, readQuery); err != nil {
		t.Fatalf("run concurrent increments: %v", err)
	}
	elapsed := time.Since(started)

	// Read the balance that MySQL actually persisted.
	var observed int
	if err := db.QueryRowContext(
		ctx,
		"SELECT balance FROM account WHERE id = 1",
	).Scan(&observed); err != nil {
		t.Fatalf("read final balance: %v", err)
	}

	// Compare the attempted increments with the persisted balance.
	expected := workerCount * incrementsPerWorker
	return workloadResult{
		expected: expected,
		observed: observed,
		lost:     expected - observed,
		elapsed:  elapsed,
	}
}

// runConcurrentIncrements coordinates workers using the supplied read query.
//
// It gives each worker a dedicated connection, releases the workers together,
// waits for all increments to finish, and combines any worker errors.
func runConcurrentIncrements(ctx context.Context, db *sql.DB, readQuery string) error {
	// Prepare a shared start gate and one buffered error slot per worker.
	start := make(chan struct{})
	errs := make(chan error, workerCount)

	var workers sync.WaitGroup
	workers.Add(workerCount)

	// Create the workers that will execute SQL concurrently.
	for worker := 0; worker < workerCount; worker++ {
		go func(worker int) {
			defer workers.Done()

			// Keep one dedicated connection for this worker's transactions.
			conn, err := db.Conn(ctx)
			if err != nil {
				errs <- fmt.Errorf("worker %d: acquire connection: %w", worker, err)
				return
			}
			defer func() { _ = conn.Close() }()

			// Wait until the parent releases every worker to start.
			select {
			case <-start:
			case <-ctx.Done():
				errs <- fmt.Errorf("worker %d: await start: %w", worker, ctx.Err())
				return
			}

			// Execute this worker's read-modify-write transactions.
			for increment := 0; increment < incrementsPerWorker; increment++ {
				if err := incrementBalance(ctx, conn, readQuery); err != nil {
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

	// Release the start gate and wait for every worker to finish.
	// This synchronizes launch only; it does not add a barrier after the reads.
	close(start)
	workers.Wait()
	close(errs)

	// Combine worker failures into one error for the test goroutine.
	// Each worker reports at most once, so the buffered channel cannot block it.
	var workerErrors []error
	for err := range errs {
		workerErrors = append(workerErrors, err)
	}

	return errors.Join(workerErrors...)
}

// incrementBalance performs one read-modify-write with the supplied read query.
//
// The query determines whether the read is vulnerable or locking; every other
// transaction step remains identical so the two paths can be compared fairly.
func incrementBalance(ctx context.Context, conn *sql.Conn, readQuery string) error {
	// Begin an explicit transaction that contains both read and write.
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()

	// Read with either plain SELECT or SELECT ... FOR UPDATE.
	var balance int
	if err := tx.QueryRowContext(
		ctx,
		readQuery,
	).Scan(&balance); err != nil {
		return fmt.Errorf("read balance: %w", err)
	}

	// Widen the overlap window without using a deterministic sync-point.
	time.Sleep(readWriteDelay)

	// Write the previously read value plus one back to the shared row.
	if _, err := tx.ExecContext(
		ctx,
		"UPDATE account SET balance = ? WHERE id = 1",
		balance+1,
	); err != nil {
		return fmt.Errorf("update balance: %w", err)
	}

	// Commit the write and release any row lock held by this transaction.
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}

	return nil
}
