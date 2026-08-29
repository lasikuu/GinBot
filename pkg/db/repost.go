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
const PerceptualAlgoPHash64 int32 = 1

// matchRepostExact interpolates column, which is always an internal constant and
// never caller input. The explicit NULL guard on the exclusion is required:
// `NULL IS DISTINCT FROM NULL` is FALSE, which would drop authorless entries.
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

// MatchRepostBySourceKey returns the oldest matching entry, or ErrNotFound. An
// empty excludeUserID excludes nobody.
func MatchRepostBySourceKey(ctx context.Context, instanceID int64, sourceKey string, excludeUserID string) (*model.RepostEntry, error) {
	return matchRepostExact(ctx, instanceID, "source_key", sourceKey, excludeUserID)
}

func MatchRepostByContentHash(ctx context.Context, instanceID int64, contentHash []byte, excludeUserID string) (*model.RepostEntry, error) {
	return matchRepostExact(ctx, instanceID, "content_hash", contentHash, excludeUserID)
}

type PerceptualMatch struct {
	Entry    *model.RepostEntry
	Distance int32
}

// MatchRepostByPerceptualHash returns the closest match within maxDistance,
// oldest first among ties, or ErrNotFound. The instance is re-asserted on the
// joined entry: repost_fingerprint.instance_id is denormalised and unconstrained,
// and cross-instance leakage is a privacy failure rather than a wrong answer.
func MatchRepostByPerceptualHash(ctx context.Context, instanceID int64, phash int64, chunks [8]int16, maxDistance int32, excludeUserID string) (*PerceptualMatch, error) {
	// Postgres has no bit_count(bigint), only bytea and bit, hence the bit(64)
	// cast; a failed lookup here is swallowed as "no match", so it would be silent.
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

// CreateRepostEntry writes the entry and its fingerprint in one transaction:
// either half alone corrupts the index.
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

type RepostRetention struct {
	InstanceID    int64
	RetentionDays int32
}

// ListRepostRetentions skips instances with a NULL repost_retention_days, the
// default, which keeps everything.
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

// DeleteRepostEntriesBefore removes at most limit entries and reports how many
// went. Fingerprints follow via ON DELETE CASCADE.
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
