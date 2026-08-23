//go:build integration

// Integration test for the repost migration's Down block specifically.
//
//	docker compose -f docker-compose.psql.yml up -d
//	go test -tags=integration -race -count=1 ./pkg/db/...
//
// Motivation: a migration Down block that only works against an EMPTY table is
// a defect class that has already shipped in this repository once
// (docs/plans/phases/phase-5-wanha.md, "Also verify the migration's Down block
// works with rows present in both new tables").
//
// This test runs against a THROWAWAY DATABASE of its own, created and dropped
// per run, rather than against the shared test database. That is not
// fastidiousness: goose.Down drops repost_entry and repost_fingerprint, and
// `go test ./...` runs packages CONCURRENTLY in separate processes against one
// Postgres. Against the shared database this test pulled those tables out from
// under pkg/grpc/server's repost integration tests mid-run, whose inserts are
// deliberately best-effort and swallowed — so the damage surfaced as an
// intermittent "0 matches" three packages away, with no error anywhere
// pointing back here. Isolating the destructive operation is the only fix that
// keeps the assertion intact.
package db

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver
	"github.com/lasikuu/GinBot/internal/config"
	"github.com/pressly/goose/v3"
)

// repostMigrationVersion is the goose version of 20260823170000_repost.sql.
const repostMigrationVersion int64 = 20260823170000

// migrationTestDSN builds a connection string for dbName on the configured
// server, mirroring how InitDB assembles its own URI.
func migrationTestDSN(dbName string) string {
	dsn := url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(config.Options.DB.Username, config.Options.DB.Password),
		Host:   net.JoinHostPort(config.Options.DB.Host, strconv.Itoa(int(config.Options.DB.Port))),
		Path:   dbName,
	}
	return dsn.String()
}

// newMigrationTestDatabase creates an empty database, migrates it to the latest
// version, and returns a handle on it. The database is dropped on cleanup.
func newMigrationTestDatabase(t *testing.T) *sql.DB {
	t.Helper()
	ctx := context.Background()

	// Lowercase and punctuation-free so it needs no quoting as an identifier.
	name := fmt.Sprintf("ginbot_migtest_%d", time.Now().UnixNano())

	// CREATE DATABASE cannot run inside a transaction, so it goes through the
	// shared pool's plain Exec. This is the ONLY thing this test does to the
	// shared database.
	if _, err := db().Exec(ctx, `CREATE DATABASE `+name); err != nil {
		t.Fatalf("create throwaway database %s: %v", name, err)
	}

	handle, err := sql.Open("pgx", migrationTestDSN(name))
	if err != nil {
		t.Fatalf("open throwaway database %s: %v", name, err)
	}

	t.Cleanup(func() {
		// The handle has to go first: DROP DATABASE refuses while a session is
		// connected. FORCE covers a connection the pool has not yet released.
		if err := handle.Close(); err != nil {
			t.Errorf("close throwaway database handle: %v", err)
		}
		if _, err := db().Exec(context.Background(), `DROP DATABASE IF EXISTS `+name+` WITH (FORCE)`); err != nil {
			t.Errorf("drop throwaway database %s: %v", name, err)
		}
	})

	// goose.SetBaseFS and goose.SetDialect are already configured by TestMain's
	// call to EnsureLatestVersion in db_integration_test.go.
	if err := goose.Up(handle, "migrations"); err != nil {
		t.Fatalf("migrate throwaway database %s up: %v", name, err)
	}

	return handle
}

// TestRepostMigrationDownWorksWithRowsPresentInBothTables asserts that stepping
// the repost migration down succeeds — and the schema comes back up cleanly
// afterwards — even when repost_entry AND repost_fingerprint both hold rows at
// the moment Down runs.
//
// It assumes the repost migration is the LATEST one, since goose.Down steps
// down exactly one version with no way to name a migration by feature. True as
// of this phase; a later migration added after it must update this test.
func TestRepostMigrationDownWorksWithRowsPresentInBothTables(t *testing.T) {
	ctx := context.Background()
	handle := newMigrationTestDatabase(t)

	// goose.Down steps down exactly ONE version and cannot be pointed at a
	// migration by name, so this test is only testing the repost migration for
	// as long as the repost migration is the newest one. Asserted rather than
	// assumed: without this, the first migration added after it silently
	// repoints the whole test at unrelated DDL while still passing green.
	version, err := goose.GetDBVersion(handle)
	if err != nil {
		t.Fatalf("read goose version: %v", err)
	}
	if version != repostMigrationVersion {
		t.Fatalf("latest migration is %d, not the repost migration (%d); goose.Down would step down the wrong one, so this test must be updated to target it explicitly",
			version, repostMigrationVersion)
	}

	// Seeded with raw SQL rather than through CreateRepostEntry, because that
	// writes through the package-level pool, which points at the shared
	// database rather than this one.
	var instanceID int64
	if err := handle.QueryRowContext(ctx,
		`INSERT INTO instance (platform_enum, instance_meta)
		 VALUES (2, '{"instance_uid":"migration-downup"}'::jsonb) RETURNING id`,
	).Scan(&instanceID); err != nil {
		t.Fatalf("seed instance: %v", err)
	}

	var entryID int64
	if err := handle.QueryRowContext(ctx,
		`INSERT INTO repost_entry (instance_id, kind, content_hash, msg_ref, posted_at)
		 VALUES ($1, 2, $2, '{"message_uid":"m"}'::jsonb, NOW()) RETURNING id`,
		instanceID, []byte("migration-fixture"),
	).Scan(&entryID); err != nil {
		t.Fatalf("seed repost_entry: %v", err)
	}

	if _, err := handle.ExecContext(ctx,
		`INSERT INTO repost_fingerprint (entry_id, instance_id, algo, region, phash,
		     c0, c1, c2, c3, c4, c5, c6, c7)
		 VALUES ($1, $2, 1, 0, $3, 1, 2, 3, 4, 5, 6, 7, 8)`,
		entryID, instanceID, int64(0x0102030405060708),
	); err != nil {
		t.Fatalf("seed repost_fingerprint: %v", err)
	}

	// Both tables must genuinely hold rows, or the Down below proves nothing.
	for _, table := range []string{"repost_entry", "repost_fingerprint"} {
		var rows int
		if err := handle.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table).Scan(&rows); err != nil {
			t.Fatalf("count %s before Down: %v", table, err)
		}
		if rows == 0 {
			t.Fatalf("fixture setup left %s empty; the Down below would not be exercising anything", table)
		}
	}

	if err := goose.Down(handle, "migrations"); err != nil {
		t.Fatalf("goose.Down with rows present in repost_entry and repost_fingerprint: %v", err)
	}

	// The tables must actually be GONE — this migration's Down block drops
	// them. A row-count-only assertion would pass against a Down block that
	// merely emptied them, which is not what the migration file says.
	for _, table := range []string{"repost_entry", "repost_fingerprint"} {
		var exists bool
		if err := handle.QueryRowContext(ctx,
			`SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = $1)`, table,
		).Scan(&exists); err != nil {
			t.Fatalf("check %s existence after Down: %v", table, err)
		}
		if exists {
			t.Errorf("%s still exists after goose.Down; the migration's Down block did not drop it", table)
		}
	}

	// The Down block also restores the legacy `linked` table it dropped on the
	// way up. Asserting it comes back is what makes the Down a real inverse
	// rather than a one-way demolition.
	var linkedExists bool
	if err := handle.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = 'linked')`,
	).Scan(&linkedExists); err != nil {
		t.Fatalf("check linked existence after Down: %v", err)
	}
	if !linkedExists {
		t.Error("linked was not recreated by the migration's Down block")
	}

	if err := goose.Up(handle, "migrations"); err != nil {
		t.Fatalf("goose.Up to restore the schema after Down: %v", err)
	}

	// The seeded row went with the dropped table; that is expected and not
	// under test. What matters is that the table is back and queryable.
	var afterUp int
	if err := handle.QueryRowContext(ctx, `SELECT COUNT(*) FROM repost_entry`).Scan(&afterUp); err != nil {
		t.Fatalf("repost_entry is not queryable after goose.Up restored it: %v", err)
	}
	if afterUp != 0 {
		t.Errorf("repost_entry has %d row(s) after a drop-and-recreate cycle, want 0", afterUp)
	}
}
