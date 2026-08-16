package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/weavegate/weavegate/internal/fixture"
	"github.com/weavegate/weavegate/internal/report"
)

// newRunID builds a run ID in fixed-width-millisecond form (A-6):
// run_<YYYYMMDDTHHMMSS.mmmZ>_<8 hex>. The fixed millisecond width means
// lexicographic order is time order, so "most recent run" needs no tie-break.
func newRunID(now time.Time) (string, error) {
	suffix := make([]byte, 4)
	if _, err := rand.Read(suffix); err != nil {
		return "", fmt.Errorf("generate run ID suffix: %w", err)
	}
	return fmt.Sprintf("run_%s_%s", now.UTC().Format("20060102T150405.000Z"), hex.EncodeToString(suffix)), nil
}

// collectManifest gathers run metadata immediately after fixture
// provisioning (A-7). The report package itself never touches the database.
func collectManifest(
	ctx context.Context,
	db *fixture.DB,
	prepared fixture.Prepared,
	adapter, variant, runID string,
	startedAt time.Time,
) (report.Manifest, error) {
	var isolationLevel string
	if err := db.SQL.QueryRowContext(ctx, "SELECT @@global.transaction_isolation").Scan(&isolationLevel); err != nil {
		return report.Manifest{}, fmt.Errorf("read global transaction isolation: %w", err)
	}

	engine, err := readEngines(ctx, db)
	if err != nil {
		return report.Manifest{}, err
	}

	return report.Manifest{
		RunID:            runID,
		StartedAt:        startedAt,
		WeavegateVersion: version,
		SchemaVersion:    prepared.MigrationDigest(),
		SeedData:         prepared.SeedDigest(),
		IsolationLevel:   isolationLevel,
		Engine:           engine,
		Adapter:          adapter,
		Variant:          variant,
		Image:            prepared.Image(),
	}, nil
}

// readEngines reads the distinct storage engines actually in use by the
// provisioned schema's tables, rather than assuming InnoDB: nothing in
// config loading or fixture provisioning rejects a migration that declares
// a different engine, and storage-engine choice materially changes locking
// and concurrency behavior, so a constant here could mislabel the
// environment behind a verdict. Multiple distinct engines are joined so a
// mixed-engine schema is still reported truthfully rather than collapsed
// into one value.
func readEngines(ctx context.Context, db *fixture.DB) (string, error) {
	rows, err := db.SQL.QueryContext(ctx,
		"SELECT DISTINCT engine FROM information_schema.tables "+
			"WHERE table_schema = DATABASE() AND engine IS NOT NULL ORDER BY engine")
	if err != nil {
		return "", fmt.Errorf("read storage engines: %w", err)
	}
	defer rows.Close()

	var engines []string
	for rows.Next() {
		var engine string
		if err := rows.Scan(&engine); err != nil {
			return "", fmt.Errorf("read storage engines: %w", err)
		}
		engines = append(engines, engine)
	}
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("read storage engines: %w", err)
	}
	if len(engines) == 0 {
		return "", fmt.Errorf("read storage engines: no tables found in the provisioned schema")
	}
	return strings.Join(engines, ","), nil
}
