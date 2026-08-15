package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
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
	spec fixture.FixtureSpec,
	adapter, variant, runID string,
	startedAt time.Time,
) (report.Manifest, error) {
	schemaVersion, err := hashMigrations(spec.Migrations)
	if err != nil {
		return report.Manifest{}, fmt.Errorf("compute schema version: %w", err)
	}
	seedVersion, err := hashFile(spec.Seed)
	if err != nil {
		return report.Manifest{}, fmt.Errorf("compute seed data version: %w", err)
	}

	var isolationLevel string
	if err := db.SQL.QueryRowContext(ctx, "SELECT @@global.transaction_isolation").Scan(&isolationLevel); err != nil {
		return report.Manifest{}, fmt.Errorf("read global transaction isolation: %w", err)
	}

	return report.Manifest{
		RunID:            runID,
		StartedAt:        startedAt,
		WeavegateVersion: version,
		SchemaVersion:    schemaVersion,
		SeedData:         seedVersion,
		IsolationLevel:   isolationLevel,
		Engine:           "InnoDB",
		Adapter:          adapter,
		Variant:          variant,
		Image:            spec.Image,
	}, nil
}

// hashMigrations hashes "<basename>\n"+content for every *.sql file in
// directory, sorted by filename, so the boundary between files is
// unambiguous. It returns the first 12 hex characters of the sha256 sum.
func hashMigrations(directory string) (string, error) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return "", fmt.Errorf("read migration directory %q: %w", directory, err)
	}

	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".sql" {
			continue
		}
		names = append(names, entry.Name())
	}
	sort.Strings(names)

	hasher := sha256.New()
	for _, name := range names {
		content, err := os.ReadFile(filepath.Join(directory, name))
		if err != nil {
			return "", fmt.Errorf("read migration file %q: %w", name, err)
		}
		fmt.Fprintf(hasher, "%s\n", name)
		hasher.Write(content)
	}
	return hex.EncodeToString(hasher.Sum(nil))[:12], nil
}

func hashFile(path string) (string, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read file %q: %w", path, err)
	}
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])[:12], nil
}
