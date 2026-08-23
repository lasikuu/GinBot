package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/lasikuu/GinBot/internal/model"
	"github.com/lasikuu/GinBot/pkg/log"
	"github.com/lasikuu/GinBot/pkg/repost"
	"go.uber.org/zap"
	"google.golang.org/protobuf/types/known/structpb"
)

// PerceptualAlgoPHash64 is repost_fingerprint.algo for a 64-bit DCT pHash.
// The only value defined so far; the column exists so a future wider hash can
// be added as a new algo value rather than a new table.
const PerceptualAlgoPHash64 int32 = 1

// matchRepostExact backs MatchRepostBySourceKey and MatchRepostByContentHash:
// both are the same shape, an exact lookup on one identity column, excluding
// one author, oldest match first since the oldest is the true original.
//
// column is always one of the two constant names below, supplied internally
// by this file — never caller input — so building the query string with it is
// not an injection risk.
//
// The author exclusion is guarded by an explicit NULL check rather than being
// left to IS DISTINCT FROM alone. `NULL IS DISTINCT FROM NULL` is FALSE, so a
// bare predicate would drop every entry whose author is unknown as soon as the
// caller excludes nobody — and user_id is ON DELETE SET NULL, so entries do
// become authorless once an account goes. That contradicts the documented
// contract that an empty excludeUserID excludes nobody.
func matchRepostExact(ctx context.Context, instanceID int64, column string, value any, excludeUserID string) (*model.RepostEntry, error) {
	var row model.RepostEntry
	query := `SELECT ` + model.RepostEntryColumns + `
		 FROM repost_entry
		 WHERE instance_id = $1 AND ` + column + ` = $2
		   AND ($3::uuid IS NULL OR user_id IS DISTINCT FROM $3::uuid)
		 ORDER BY posted_at ASC
		 LIMIT 1`

	err := db().QueryRow(ctx, query, instanceID, value, nullStr(excludeUserID)).Scan(row.ScanTargets()...)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan repost entry exact match: %w", err)
	}

	return &row, nil
}

// MatchRepostBySourceKey returns the oldest live entry in instanceID whose
// source_key equals sourceKey and whose user_id is distinct from
// excludeUserID. An empty excludeUserID excludes nobody. ErrNotFound when
// there is none.
func MatchRepostBySourceKey(ctx context.Context, instanceID int64, sourceKey string, excludeUserID string) (*model.RepostEntry, error) {
	return matchRepostExact(ctx, instanceID, "source_key", sourceKey, excludeUserID)
}

// MatchRepostByContentHash is the same lookup on content_hash, catching any
// re-upload of identical bytes regardless of declared type.
func MatchRepostByContentHash(ctx context.Context, instanceID int64, contentHash []byte, excludeUserID string) (*model.RepostEntry, error) {
	return matchRepostExact(ctx, instanceID, "content_hash", contentHash, excludeUserID)
}

// PerceptualMatch is a pigeonhole candidate that bit_count verified.
type PerceptualMatch struct {
	Entry    *model.RepostEntry
	Distance int32
}

// MatchRepostByPerceptualHash runs the pigeonhole candidate query
// (docs/plans/wanha.md "Matching 3") and returns the closest verified match
// within maxDistance, preferring the oldest among equally close ones.
// ErrNotFound when there is none.
//
// The 8-way OR over c0..c7 is exact recall, not approximate: splitting phash
// into 8 disjoint 8-bit chunks guarantees that any true match within Hamming
// distance 7 shares at least one chunk exactly (pkg/repost.Chunks), so
// Postgres resolves this via a BitmapOr across the eight chunk indexes and
// bit_count then verifies and grades each candidate.
//
// The instance is asserted TWICE — once on the fingerprint in the CTE, which is
// what makes the chunk indexes selective, and again on the joined entry. The
// second is not redundant belt-and-braces: repost_fingerprint.instance_id is
// denormalised with no constraint tying it to its entry's instance, so without
// the outer predicate the whole of AC11's per-instance isolation would rest on
// one unconstrained column being written correctly. Cross-guild leakage is a
// privacy failure, not just a wrong answer, so it gets a second check.
func MatchRepostByPerceptualHash(ctx context.Context, instanceID int64, phash int64, chunks [8]int16, maxDistance int32, excludeUserID string) (*PerceptualMatch, error) {
	// bit_count is cast to bit(64) rather than applied to the bigint XOR
	// directly. Postgres defines bit_count only for bytea and bit — there is no
	// bit_count(bigint) — so the query as written in docs/plans/wanha.md fails
	// outright, and because a failed lookup here is logged and treated as "no
	// match", it would have failed silently: every perceptual detection lost,
	// with nothing but a log line to show for it.
	//
	// The distance is computed once, in the CTE, rather than repeated in the
	// SELECT list and the WHERE clause. A cast this easy to get wrong must not
	// exist in two places that can drift apart.
	query := `
		WITH candidates AS (
		    SELECT f.entry_id,
		           bit_count((f.phash # $11::bigint)::bit(64))::int AS distance
		    FROM repost_fingerprint f
		    WHERE f.instance_id = $1
		      AND f.algo = $2
		      AND ( f.c0 = $3 OR f.c1 = $4 OR f.c2 = $5 OR f.c3 = $6
		         OR f.c4 = $7 OR f.c5 = $8 OR f.c6 = $9 OR f.c7 = $10 )
		)
		SELECT ` + prefixed(model.RepostEntryColumns, "e") + `, c.distance
		FROM candidates c
		JOIN repost_entry e ON e.id = c.entry_id
		WHERE e.instance_id = $1
		  AND c.distance <= $12
		  AND ($13::uuid IS NULL OR e.user_id IS DISTINCT FROM $13::uuid)
		ORDER BY c.distance ASC, e.posted_at ASC
		LIMIT 1`

	var row model.RepostEntry
	var distance int32
	scanTargets := append(row.ScanTargets(), &distance)

	err := db().QueryRow(ctx, query,
		instanceID, PerceptualAlgoPHash64,
		chunks[0], chunks[1], chunks[2], chunks[3], chunks[4], chunks[5], chunks[6], chunks[7],
		phash, maxDistance, nullStr(excludeUserID),
	).Scan(scanTargets...)

	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan perceptual repost match: %w", err)
	}

	return &PerceptualMatch{Entry: &row, Distance: distance}, nil
}

// CreateRepostEntryParams describes one entry to remember.
type CreateRepostEntryParams struct {
	InstanceID    int64
	DestinationID *int64
	// UserID is the GinBot user_account UUID, or "" when unknown.
	UserID string
	Kind   int32
	// SourceKey and CanonicalURL are set for a link, empty otherwise.
	SourceKey    string
	CanonicalURL string
	// ContentHash is the raw SHA-256 digest, nil for a link.
	ContentHash []byte
	MsgRef      *structpb.Struct
	PostedAt    time.Time
	// PHash, when non-nil, writes one repost_fingerprint row for region 0 in
	// the same transaction as the entry.
	PHash *int64
}

// CreateRepostEntry inserts one entry, and its fingerprint when PHash is set,
// in a single transaction — a fingerprint without its entry, or an entry
// silently missing its fingerprint, would both corrupt the index.
func CreateRepostEntry(ctx context.Context, params CreateRepostEntryParams) (int64, error) {
	tx, err := db().Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin repost entry insert: %w", err)
	}
	defer func() {
		if err := tx.Rollback(ctx); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
			log.Z.Warn("failed to roll back repost entry insert", zap.Error(err))
		}
	}()

	var entryID int64
	if err := tx.QueryRow(ctx,
		`INSERT INTO repost_entry
		     (instance_id, destination_id, user_id, kind, source_key, canonical_url, content_hash, msg_ref, posted_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		 RETURNING id`,
		params.InstanceID, params.DestinationID, nullStr(params.UserID), params.Kind,
		nullStr(params.SourceKey), nullStr(params.CanonicalURL), params.ContentHash, params.MsgRef, params.PostedAt.UTC(),
	).Scan(&entryID); err != nil {
		return 0, fmt.Errorf("insert repost entry: %w", err)
	}

	if params.PHash != nil {
		chunks := repost.Chunks(uint64(*params.PHash))
		if _, err := tx.Exec(ctx,
			`INSERT INTO repost_fingerprint
			     (entry_id, instance_id, algo, region, phash, c0, c1, c2, c3, c4, c5, c6, c7)
			 VALUES ($1, $2, $3, 0, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
			entryID, params.InstanceID, PerceptualAlgoPHash64, *params.PHash,
			chunks[0], chunks[1], chunks[2], chunks[3], chunks[4], chunks[5], chunks[6], chunks[7],
		); err != nil {
			return 0, fmt.Errorf("insert repost fingerprint: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit repost entry insert: %w", err)
	}

	return entryID, nil
}

// RepostRetention is an instance with a finite retention window.
type RepostRetention struct {
	InstanceID    int64
	RetentionDays int32
}

// ListRepostRetentions returns every live instance with a non-null
// repost_retention_days. A NULL keeps everything, which is the default (W1)
// and is therefore never returned here: nothing to sweep for it.
func ListRepostRetentions(ctx context.Context) ([]RepostRetention, error) {
	rows, err := db().Query(ctx,
		`SELECT id, repost_retention_days
		 FROM instance
		 WHERE deleted = FALSE AND repost_retention_days IS NOT NULL`,
	)
	if err != nil {
		return nil, fmt.Errorf("query repost retentions: %w", err)
	}
	defer rows.Close()

	var retentions []RepostRetention
	for rows.Next() {
		var r RepostRetention
		if err := rows.Scan(&r.InstanceID, &r.RetentionDays); err != nil {
			return nil, fmt.Errorf("scan repost retention: %w", err)
		}
		retentions = append(retentions, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate repost retentions: %w", err)
	}

	return retentions, nil
}

// DeleteRepostEntriesBefore removes at most limit entries in instanceID
// posted before before, reporting how many went. Fingerprints cascade via
// repost_fingerprint's ON DELETE CASCADE foreign key.
func DeleteRepostEntriesBefore(ctx context.Context, instanceID int64, before time.Time, limit int64) (int64, error) {
	tag, err := db().Exec(ctx,
		`DELETE FROM repost_entry
		  WHERE id IN (
		      SELECT id FROM repost_entry
		       WHERE instance_id = $1 AND posted_at < $2
		       ORDER BY posted_at ASC
		       LIMIT $3
		  )`,
		instanceID, before.UTC(), limit,
	)
	if err != nil {
		return 0, fmt.Errorf("delete repost entries: %w", err)
	}

	return tag.RowsAffected(), nil
}
