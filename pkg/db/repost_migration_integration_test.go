//go:build integration

// This test uses a throwaway database of its own: goose.Down drops
// repost_entry and repost_fingerprint, and `go test ./...` runs packages
// concurrently against one Postgres.
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

const repostMigrationVersion int64 = 20260823170000

func migrationTestDSN(dbName string) string {
	dsn := url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(config.Options.DB.Username, config.Options.DB.Password),
		Host:   net.JoinHostPort(config.Options.DB.Host, strconv.Itoa(int(config.Options.DB.Port))),
		Path:   dbName,
	}
	return dsn.String()
}

func newMigrationTestDatabase(t *testing.T) *sql.DB {
	t.Helper()
	ctx := context.Background()

	name := fmt.Sprintf("ginbot_migtest_%d", time.Now().UnixNano())

	// CREATE DATABASE cannot run inside a transaction, so it goes through the
	// package pool's plain Exec.
	if _, err := db().Exec(ctx, `CREATE DATABASE `+name); err != nil {
		t.Fatalf("create throwaway database %s: %v", name, err)
	}

	handle, err := sql.Open("pgx", migrationTestDSN(name))
	if err != nil {
		t.Fatalf("open throwaway database %s: %v", name, err)
	}

	t.Cleanup(func() {
		// DROP DATABASE refuses while a session is connected, so the handle closes
		// first; FORCE covers a connection it has not yet released.
		if err := handle.Close(); err != nil {
			t.Errorf("close throwaway database handle: %v", err)
		}
		if _, err := db().Exec(context.Background(), `DROP DATABASE IF EXISTS `+name+` WITH (FORCE)`); err != nil {
			t.Errorf("drop throwaway database %s: %v", name, err)
		}
	})

	if err := goose.Up(handle, "migrations"); err != nil {
		t.Fatalf("migrate throwaway database %s up: %v", name, err)
	}

	return handle
}

// goose.Down steps down exactly one version and cannot be pointed at a migration
// by name, so DownTo first makes the repost migration the newest applied one.
func TestRepostMigrationDownWorksWithRowsPresentInBothTables(t *testing.T) {
	ctx := context.Background()
	handle := newMigrationTestDatabase(t)

	if err := goose.DownTo(handle, "migrations", repostMigrationVersion); err != nil {
		t.Fatalf("step down the migrations newer than the repost one (%d): %v", repostMigrationVersion, err)
	}

	// The Down below is only meaningful if it lands on the repost migration; a
	// DownTo that silently did nothing would still pass green.
	version, err := goose.GetDBVersion(handle)
	if err != nil {
		t.Fatalf("read goose version: %v", err)
	}
	if version != repostMigrationVersion {
		t.Fatalf("after DownTo the latest migration is %d, want the repost migration (%d); goose.Down would step down the wrong one",
			version, repostMigrationVersion)
	}

	// Raw SQL rather than CreateRepostEntry, which writes through the package
	// pool and so would target the wrong database.
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

	// The tables must be gone, not merely emptied: a row-count assertion would
	// pass against a Down block that only deleted rows.
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

	// The Down block restores the legacy `linked` table it dropped on the way up,
	// which is what makes it an inverse rather than a demolition.
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

	var afterUp int
	if err := handle.QueryRowContext(ctx, `SELECT COUNT(*) FROM repost_entry`).Scan(&afterUp); err != nil {
		t.Fatalf("repost_entry is not queryable after goose.Up restored it: %v", err)
	}
	if afterUp != 0 {
		t.Errorf("repost_entry has %d row(s) after a drop-and-recreate cycle, want 0", afterUp)
	}
}
