//go:build integration

package db

import (
	"context"
	"os"
	"testing"
	"time"
)

// Runs the operator script verbatim; its UPDATE is table-wide by design.
const backfillScriptPath = "../../cmd/ginbot-migrate/backfill_original_filename.sql"

func runBackfillScript(t *testing.T, ctx context.Context) {
	t.Helper()

	script, err := os.ReadFile(backfillScriptPath)
	if err != nil {
		t.Fatalf("read backfill script %s: %v", backfillScriptPath, err)
	}
	if _, err := db().Exec(ctx, string(script)); err != nil {
		t.Fatalf("run backfill script: %v", err)
	}
}

// TestBackfillOriginalFilenameScriptIsIdempotentAndDeterministic mirrors what
// cmd/ginbot-migrate actually leaves behind: a migration_id_map row per
// legacy id, several of which may point at the same new file row once
// content-hash dedupe collapsed them.
func TestBackfillOriginalFilenameScriptIsIdempotentAndDeterministic(t *testing.T) {
	ctx := context.Background()

	// The script's UPDATE is table-wide, so migration_id_map must not outlive the
	// test: left behind, the next run would backfill unrelated rows from it.
	var preexisting bool
	if err := db().QueryRow(ctx,
		`SELECT to_regclass('public.migration_id_map') IS NOT NULL`,
	).Scan(&preexisting); err != nil {
		t.Fatalf("probe for migration_id_map: %v", err)
	}
	if preexisting {
		t.Skip("migration_id_map already exists; refusing to backfill a real cmd/ginbot-migrate run")
	}

	if _, err := db().Exec(ctx,
		`CREATE TABLE migration_id_map (
			entity text NOT NULL, legacy_id text NOT NULL, new_id text NOT NULL,
			PRIMARY KEY (entity, legacy_id))`,
	); err != nil {
		t.Fatalf("create scratch migration_id_map: %v", err)
	}
	t.Cleanup(func() {
		if _, err := db().Exec(context.Background(), `DROP TABLE IF EXISTS migration_id_map`); err != nil {
			t.Errorf("drop scratch migration_id_map: %v", err)
		}
	})

	suffix := time.Now().Format("150405.000000000")

	dedupedHash := "backfill-deduped-" + suffix
	dedupedID, _, err := GetOrCreateFileByHash(ctx, dedupedHash, "trigger/bf/"+dedupedHash, "image/png", 10, "")
	if err != nil {
		t.Fatalf("create deduped file: %v", err)
	}

	namedHash := "backfill-named-" + suffix
	namedID, _, err := GetOrCreateFileByHash(ctx, namedHash, "trigger/bf/"+namedHash, "image/png", 10, "already-named.png")
	if err != nil {
		t.Fatalf("create already-named file: %v", err)
	}

	t.Cleanup(func() {
		cleanupCtx := context.Background()
		if _, err := db().Exec(cleanupCtx,
			`DELETE FROM migration_id_map WHERE entity = 'file' AND legacy_id LIKE $1`, "backfill-%-"+suffix,
		); err != nil {
			t.Errorf("cleanup migration_id_map: %v", err)
		}
		for _, id := range []string{dedupedID, namedID} {
			if _, err := db().Exec(cleanupCtx, `DELETE FROM file WHERE id = $1`, id); err != nil {
				t.Errorf("cleanup file %s: %v", id, err)
			}
		}
	})

	// Two legacy names deduped onto dedupedID; DISTINCT ON (new_id) ORDER BY
	// new_id, legacy_id must deterministically pick the alphabetically first.
	rows := []struct{ legacy, newID string }{
		{"backfill-zzz-legacy-" + suffix, dedupedID},
		{"backfill-aaa-legacy-" + suffix, dedupedID},
		{"backfill-named-legacy-" + suffix, namedID},
	}
	for _, r := range rows {
		if _, err := db().Exec(ctx,
			`INSERT INTO migration_id_map (entity, legacy_id, new_id) VALUES ('file', $1, $2)`,
			r.legacy, r.newID,
		); err != nil {
			t.Fatalf("seed migration_id_map row %q: %v", r.legacy, err)
		}
	}

	runBackfillScript(t, ctx)

	deduped, err := GetFile(ctx, dedupedID)
	if err != nil {
		t.Fatalf("GetFile(deduped) after backfill: %v", err)
	}
	wantDeduped := "backfill-aaa-legacy-" + suffix
	if deduped.OriginalFilename != wantDeduped {
		t.Errorf("deduped file original_filename = %q, want %q (the alphabetically first legacy name)",
			deduped.OriginalFilename, wantDeduped)
	}

	named, err := GetFile(ctx, namedID)
	if err != nil {
		t.Fatalf("GetFile(named) after backfill: %v", err)
	}
	if named.OriginalFilename != "already-named.png" {
		t.Errorf("already-named file original_filename = %q, want it left alone at %q (only empty names are filled)",
			named.OriginalFilename, "already-named.png")
	}

	runBackfillScript(t, ctx)

	dedupedAgain, err := GetFile(ctx, dedupedID)
	if err != nil {
		t.Fatalf("GetFile(deduped) after second backfill run: %v", err)
	}
	if dedupedAgain.OriginalFilename != wantDeduped {
		t.Errorf("a second run changed the deduped file's original_filename to %q, want it unchanged at %q (idempotent)",
			dedupedAgain.OriginalFilename, wantDeduped)
	}
}
