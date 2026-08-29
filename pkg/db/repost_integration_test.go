//go:build integration

package db

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/lasikuu/GinBot/internal/model"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
	"github.com/lasikuu/GinBot/pkg/repost"
)

// createRepostInstance returns an instance whose cleanup cascades to its
// repost_entry and repost_fingerprint rows, plus a per-call unique suffix.
func createRepostInstance(t *testing.T, label string) (instanceID int64, suffix string) {
	t.Helper()
	suffix = time.Now().Format("150405.000000")
	im := meta(t, map[string]string{"guild_id": "repost-" + label + "-" + suffix})
	cleanupInstanceByMeta(t, im)

	id, err := CreateInstance(context.Background(), pb.Platform_PLATFORM_DISCORD, im, "general")
	if err != nil {
		t.Fatalf("CreateInstance: %v", err)
	}
	return id, suffix
}

func createRepostUser(t *testing.T, label string) string {
	t.Helper()
	ctx := context.Background()
	platformUID := "repost-user-" + label + "-" + time.Now().Format("150405.000000")
	userID, err := CreateUser(ctx, "repost-"+label, pb.Platform_PLATFORM_DISCORD, platformUID, nil, "en")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	cleanupUser(t, userID)
	return userID
}

func baseRepostParams(instanceID int64, sourceKey string) CreateRepostEntryParams {
	return CreateRepostEntryParams{
		InstanceID:   instanceID,
		Kind:         int32(pb.RepostKind_REPOST_KIND_LINK.Number()),
		SourceKey:    sourceKey,
		CanonicalURL: "https://example.com/" + sourceKey,
		MsgRef:       model.NewRepostMsgRef("instance-uid", "destination-uid", "message-uid", "author-uid"),
		PostedAt:     time.Now().UTC(),
	}
}

func TestCreateRepostEntryAndMatchBySourceKey(t *testing.T) {
	ctx := context.Background()
	instanceID, im := createRepostInstance(t, "sourcekey")
	sourceKey := "youtube:sourcekey-" + im

	entryID, err := CreateRepostEntry(ctx, baseRepostParams(instanceID, sourceKey))
	if err != nil {
		t.Fatalf("CreateRepostEntry: %v", err)
	}
	if entryID == 0 {
		t.Fatal("CreateRepostEntry returned id 0")
	}

	got, err := MatchRepostBySourceKey(ctx, instanceID, sourceKey, "")
	if err != nil {
		t.Fatalf("MatchRepostBySourceKey: %v", err)
	}
	if got.ID != entryID {
		t.Errorf("matched entry id = %d, want %d", got.ID, entryID)
	}
	if got.SourceKey == nil || *got.SourceKey != sourceKey {
		t.Errorf("SourceKey = %v, want %q", got.SourceKey, sourceKey)
	}
	if got.InstanceID != instanceID {
		t.Errorf("InstanceID = %d, want %d", got.InstanceID, instanceID)
	}

	ref := got.MessageRef()
	if ref.GetMessageUid() != "message-uid" {
		t.Errorf("MessageRef().MessageUid = %q, want %q", ref.GetMessageUid(), "message-uid")
	}
}

func TestMatchRepostBySourceKeyReturnsNotFoundForAnUnseenKey(t *testing.T) {
	ctx := context.Background()
	instanceID, im := createRepostInstance(t, "sourcekey-miss")

	_, err := MatchRepostBySourceKey(ctx, instanceID, "youtube:never-posted-"+im, "")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestCreateRepostEntryAndMatchByContentHash(t *testing.T) {
	ctx := context.Background()
	instanceID, im := createRepostInstance(t, "contenthash")
	contentHash := []byte("fixture-hash-" + im)

	params := CreateRepostEntryParams{
		InstanceID:   instanceID,
		Kind:         int32(pb.RepostKind_REPOST_KIND_FILE.Number()),
		ContentHash:  contentHash,
		CanonicalURL: "", // no canonical URL for a raw attachment
		MsgRef:       model.NewRepostMsgRef("i", "d", "m-"+im, "a"),
		PostedAt:     time.Now().UTC(),
	}

	entryID, err := CreateRepostEntry(ctx, params)
	if err != nil {
		t.Fatalf("CreateRepostEntry: %v", err)
	}

	got, err := MatchRepostByContentHash(ctx, instanceID, contentHash, "")
	if err != nil {
		t.Fatalf("MatchRepostByContentHash: %v", err)
	}
	if got.ID != entryID {
		t.Errorf("matched entry id = %d, want %d", got.ID, entryID)
	}
}

func TestMatchRepostByContentHashReturnsNotFoundForUnseenBytes(t *testing.T) {
	ctx := context.Background()
	instanceID, im := createRepostInstance(t, "contenthash-miss")

	_, err := MatchRepostByContentHash(ctx, instanceID, []byte("never-uploaded-"+im), "")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestMatchExcludesTheSameAuthorButAnEmptyExcludeIDMatchesEveryone(t *testing.T) {
	ctx := context.Background()
	instanceID, im := createRepostInstance(t, "exclude")
	authorID := createRepostUser(t, "exclude-author")
	otherID := createRepostUser(t, "exclude-other")
	sourceKey := "youtube:exclude-" + im

	params := baseRepostParams(instanceID, sourceKey)
	params.UserID = authorID
	if _, err := CreateRepostEntry(ctx, params); err != nil {
		t.Fatalf("CreateRepostEntry: %v", err)
	}

	if _, err := MatchRepostBySourceKey(ctx, instanceID, sourceKey, authorID); !errors.Is(err, ErrNotFound) {
		t.Errorf("excluding the entry's own author: err = %v, want ErrNotFound", err)
	}
	if _, err := MatchRepostBySourceKey(ctx, instanceID, sourceKey, otherID); err != nil {
		t.Errorf("excluding a DIFFERENT user must still find the match: %v", err)
	}
	if _, err := MatchRepostBySourceKey(ctx, instanceID, sourceKey, ""); err != nil {
		t.Errorf("an empty excludeUserID must exclude nobody: %v", err)
	}
}

func TestMatchIsScopedPerInstance(t *testing.T) {
	ctx := context.Background()
	instanceAID, imA := createRepostInstance(t, "scope-a")
	instanceBID, _ := createRepostInstance(t, "scope-b")
	sourceKey := "youtube:scoped-" + imA

	if _, err := CreateRepostEntry(ctx, baseRepostParams(instanceAID, sourceKey)); err != nil {
		t.Fatalf("CreateRepostEntry: %v", err)
	}

	if _, err := MatchRepostBySourceKey(ctx, instanceAID, sourceKey, ""); err != nil {
		t.Fatalf("expected a match in the entry's own instance: %v", err)
	}
	if _, err := MatchRepostBySourceKey(ctx, instanceBID, sourceKey, ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("a source key from instance A matched in instance B: err = %v, want ErrNotFound", err)
	}
}

// buildChunks mirrors repost.Chunks: the candidate query selects on exactly
// these chunks.
func buildChunks(hash uint64) [8]int16 {
	return repost.Chunks(hash)
}

func createPerceptualEntry(t *testing.T, instanceID int64, phash uint64, postedAt time.Time, suffix string) int64 {
	t.Helper()
	ctx := context.Background()

	ph := int64(phash)
	params := CreateRepostEntryParams{
		InstanceID:   instanceID,
		Kind:         int32(pb.RepostKind_REPOST_KIND_IMAGE.Number()),
		SourceKey:    "", // identity for an image comes from content_hash/phash, not a link
		ContentHash:  []byte("phash-fixture-" + suffix),
		CanonicalURL: "",
		MsgRef:       model.NewRepostMsgRef("i", "d", "m-"+suffix, "a"),
		PostedAt:     postedAt,
		PHash:        &ph,
	}

	id, err := CreateRepostEntry(ctx, params)
	if err != nil {
		t.Fatalf("CreateRepostEntry (perceptual fixture): %v", err)
	}
	return id
}

func TestMatchRepostByPerceptualHashReturnsTheClosestMatch(t *testing.T) {
	ctx := context.Background()
	instanceID, im := createRepostInstance(t, "phash-closest")

	const base uint64 = 0x0F0F0F0F0F0F0F0F
	far := base ^ 0x1F
	near := base ^ 0x03

	now := time.Now().UTC()
	farID := createPerceptualEntry(t, instanceID, far, now.Add(-time.Hour), im+"-far")
	nearID := createPerceptualEntry(t, instanceID, near, now.Add(-time.Minute), im+"-near")

	match, err := MatchRepostByPerceptualHash(ctx, instanceID, int64(base), buildChunks(base), int32(repost.MaxDistance), "")
	if err != nil {
		t.Fatalf("MatchRepostByPerceptualHash: %v", err)
	}
	if match.Entry.ID != nearID {
		t.Errorf("matched entry id = %d, want the closer one (%d, far was %d)", match.Entry.ID, nearID, farID)
	}
	wantDistance := int32(repost.Distance(base, near))
	if match.Distance != wantDistance {
		t.Errorf("Distance = %d, want %d", match.Distance, wantDistance)
	}
}

func TestMatchRepostByPerceptualHashPrefersTheOldestOnATie(t *testing.T) {
	ctx := context.Background()
	instanceID, im := createRepostInstance(t, "phash-oldest")

	// Not a const: int64() of a constant with the top bit set overflows at compile
	// time, unlike the same conversion on a runtime uint64.
	base := uint64(0xAAAAAAAAAAAAAAAA)
	// Equidistant from base, so ordering must fall back to posted_at ASC.
	sibling1 := base ^ 0x01
	sibling2 := base ^ 0x02

	now := time.Now().UTC()
	olderID := createPerceptualEntry(t, instanceID, sibling1, now.Add(-48*time.Hour), im+"-older")
	newerID := createPerceptualEntry(t, instanceID, sibling2, now.Add(-1*time.Hour), im+"-newer")

	match, err := MatchRepostByPerceptualHash(ctx, instanceID, int64(base), buildChunks(base), int32(repost.MaxDistance), "")
	if err != nil {
		t.Fatalf("MatchRepostByPerceptualHash: %v", err)
	}
	if match.Entry.ID != olderID {
		t.Errorf("matched entry id = %d, want the OLDER of the two equally-close entries (%d); newer was %d",
			match.Entry.ID, olderID, newerID)
	}
}

func TestMatchRepostByPerceptualHashReturnsNotFoundBeyondMaxDistance(t *testing.T) {
	ctx := context.Background()
	instanceID, _ := createRepostInstance(t, "phash-toofar")

	const base uint64 = 0x1111111111111111
	tooFar := ^base // maximally different: distance 64

	createPerceptualEntry(t, instanceID, tooFar, time.Now().UTC(), "toofar")

	_, err := MatchRepostByPerceptualHash(ctx, instanceID, int64(base), buildChunks(base), int32(repost.MaxDistance), "")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound for a candidate beyond MaxDistance", err)
	}
}

// The fixture is built to be retrieved and then rejected: c0 matches base so the
// candidate CTE returns it, but 12 differing bits exceed MaxDistance. That is
// what exercises the bit_count verification step rather than the candidate CTE.
func TestMatchRepostByPerceptualHashRejectsACandidateItRetrieved(t *testing.T) {
	ctx := context.Background()
	instanceID, _ := createRepostInstance(t, "phash-verified-reject")

	const base uint64 = 0x1122334455667788

	// Flip bits outside the most significant byte so chunk c0 still matches
	// exactly and the candidate query cannot miss this row.
	retrievedButTooFar := base ^ 0x0000_0F0F_0F0F_0000
	distance := repost.Distance(base, retrievedButTooFar)
	if distance <= repost.MaxDistance {
		t.Fatalf("fixture distance %d is within MaxDistance %d; it would not exercise rejection", distance, repost.MaxDistance)
	}
	baseChunks := buildChunks(base)
	farChunks := buildChunks(retrievedButTooFar)
	if baseChunks[0] != farChunks[0] {
		t.Fatalf("fixture does not share chunk c0 with base, so it would never be retrieved as a candidate")
	}

	createPerceptualEntry(t, instanceID, retrievedButTooFar, time.Now().UTC(), "verified-reject")

	_, err := MatchRepostByPerceptualHash(ctx, instanceID, int64(base), baseChunks, int32(repost.MaxDistance), "")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound: the candidate was retrieved by the chunk index and must be rejected by the bit_count verification at distance %d",
			err, distance)
	}
}

// Exercises a BitmapOr branch other than c0: every other fixture here differs
// only in the low byte, so an index on c0 alone would pass all of them.
func TestMatchRepostByPerceptualHashMatchesOnAChunkOtherThanTheFirst(t *testing.T) {
	ctx := context.Background()
	instanceID, _ := createRepostInstance(t, "phash-midchunk")

	const base uint64 = 0x1122334455667788

	// c4 is left untouched, so it is the only chunk that can match.
	near := base ^ 0x0100_0100_0100_0000
	distance := repost.Distance(base, near)
	if distance > repost.MaxDistance {
		t.Fatalf("fixture distance %d exceeds MaxDistance %d", distance, repost.MaxDistance)
	}

	baseChunks := buildChunks(base)
	nearChunks := buildChunks(near)
	shared := 0
	for i := range baseChunks {
		if baseChunks[i] == nearChunks[i] {
			shared++
			if i == 0 {
				t.Fatalf("fixture still shares c0, so it does not exercise another branch")
			}
		}
	}
	if shared == 0 {
		t.Fatal("fixture shares no chunk at all, which would violate the pigeonhole guarantee")
	}

	entryID := createPerceptualEntry(t, instanceID, near, time.Now().UTC(), "midchunk")

	match, err := MatchRepostByPerceptualHash(ctx, instanceID, int64(base), baseChunks, int32(repost.MaxDistance), "")
	if err != nil {
		t.Fatalf("MatchRepostByPerceptualHash: %v", err)
	}
	if match.Entry.ID != entryID {
		t.Errorf("matched entry id = %d, want %d", match.Entry.ID, entryID)
	}
	if match.Distance != int32(distance) {
		t.Errorf("distance = %d, want %d", match.Distance, distance)
	}
}

// The pigeonhole verification assumes Postgres bit_count(a # b) and
// repost.Distance are the same function.
func TestBitCountAgreesWithGoSideDistance(t *testing.T) {
	pairs := []struct{ a, b uint64 }{
		{0, 0},
		{0, ^uint64(0)},
		{0x0F0F0F0F0F0F0F0F, 0xF0F0F0F0F0F0F0F0},
		{1, 2},
		{0x8000000000000000, 0x0000000000000001}, // exercises the sign bit as a bigint
		{0xDEADBEEFDEADBEEF, 0xC0FFEEC0FFEEC0FF},
	}

	for _, p := range pairs {
		want := repost.Distance(p.a, p.b)

		// Postgres has no bit_count(bigint), only bytea and bit, so this must stay
		// the same expression MatchRepostByPerceptualHash uses.
		var got int
		if err := db().QueryRow(context.Background(),
			`SELECT bit_count(($1::bigint # $2::bigint)::bit(64))::int`, int64(p.a), int64(p.b),
		).Scan(&got); err != nil {
			t.Fatalf("bit_count query for (%#x, %#x): %v", p.a, p.b, err)
		}

		if got != want {
			t.Errorf("bit_count(%#x # %#x) = %d, repost.Distance = %d; these must agree", p.a, p.b, got, want)
		}
	}
}

func TestFingerprintRowIsWrittenOnlyWhenPHashIsSet(t *testing.T) {
	ctx := context.Background()
	instanceID, im := createRepostInstance(t, "fingerprint-optional")

	withoutPHash := baseRepostParams(instanceID, "youtube:no-phash-"+im)
	entryID, err := CreateRepostEntry(ctx, withoutPHash)
	if err != nil {
		t.Fatalf("CreateRepostEntry (no phash): %v", err)
	}

	var count int
	if err := db().QueryRow(ctx,
		`SELECT COUNT(*) FROM repost_fingerprint WHERE entry_id = $1`, entryID,
	).Scan(&count); err != nil {
		t.Fatalf("count fingerprint rows: %v", err)
	}
	if count != 0 {
		t.Errorf("a repost_entry created with no PHash has %d repost_fingerprint rows, want 0", count)
	}
}

func TestFingerprintRowContentsAndCascadeOnEntryDelete(t *testing.T) {
	ctx := context.Background()
	instanceID, im := createRepostInstance(t, "fingerprint-cascade")

	const hash uint64 = 0x1234567890ABCDEF
	ph := int64(hash)
	params := CreateRepostEntryParams{
		InstanceID:   instanceID,
		Kind:         int32(pb.RepostKind_REPOST_KIND_IMAGE.Number()),
		ContentHash:  []byte("fingerprint-cascade-" + im),
		CanonicalURL: "",
		MsgRef:       model.NewRepostMsgRef("i", "d", "m", "a"),
		PostedAt:     time.Now().UTC(),
		PHash:        &ph,
	}

	entryID, err := CreateRepostEntry(ctx, params)
	if err != nil {
		t.Fatalf("CreateRepostEntry: %v", err)
	}

	var algo, region int32
	var storedPHash int64
	var c0, c1, c2, c3, c4, c5, c6, c7 int16
	if err := db().QueryRow(ctx,
		`SELECT algo, region, phash, c0, c1, c2, c3, c4, c5, c6, c7
		 FROM repost_fingerprint WHERE entry_id = $1`, entryID,
	).Scan(&algo, &region, &storedPHash, &c0, &c1, &c2, &c3, &c4, &c5, &c6, &c7); err != nil {
		t.Fatalf("read fingerprint row: %v", err)
	}

	if algo != PerceptualAlgoPHash64 {
		t.Errorf("algo = %d, want %d (PerceptualAlgoPHash64)", algo, PerceptualAlgoPHash64)
	}
	if region != 0 {
		t.Errorf("region = %d, want 0 (whole frame, MVP)", region)
	}
	if storedPHash != ph {
		t.Errorf("stored phash = %d, want %d", storedPHash, ph)
	}

	wantChunks := repost.Chunks(hash)
	gotChunks := [8]int16{c0, c1, c2, c3, c4, c5, c6, c7}
	if gotChunks != wantChunks {
		t.Errorf("stored chunks = %v, want %v (repost.Chunks of the same hash)", gotChunks, wantChunks)
	}

	if _, err := db().Exec(ctx, `DELETE FROM repost_entry WHERE id = $1`, entryID); err != nil {
		t.Fatalf("delete repost_entry: %v", err)
	}
	var remaining int
	if err := db().QueryRow(ctx, `SELECT COUNT(*) FROM repost_fingerprint WHERE entry_id = $1`, entryID).Scan(&remaining); err != nil {
		t.Fatalf("count fingerprint rows after entry delete: %v", err)
	}
	if remaining != 0 {
		t.Errorf("%d repost_fingerprint row(s) survived their entry's deletion; ON DELETE CASCADE is not in effect", remaining)
	}
}

func TestListRepostRetentionsIgnoresNullRetention(t *testing.T) {
	ctx := context.Background()

	withRetentionID, _ := createRepostInstance(t, "retention-set")
	withoutRetentionID, _ := createRepostInstance(t, "retention-null")

	const days = 45
	if _, err := db().Exec(ctx, `UPDATE instance SET repost_retention_days = $1 WHERE id = $2`, days, withRetentionID); err != nil {
		t.Fatalf("set repost_retention_days: %v", err)
	}
	// withoutRetentionID is left at its default (NULL) deliberately.

	rows, err := ListRepostRetentions(ctx)
	if err != nil {
		t.Fatalf("ListRepostRetentions: %v", err)
	}

	var foundWithRetention, foundWithoutRetention bool
	for _, row := range rows {
		if row.InstanceID == withRetentionID {
			foundWithRetention = true
			if row.RetentionDays != days {
				t.Errorf("RetentionDays = %d, want %d", row.RetentionDays, days)
			}
		}
		if row.InstanceID == withoutRetentionID {
			foundWithoutRetention = true
		}
	}

	if !foundWithRetention {
		t.Error("ListRepostRetentions did not include an instance with a configured retention")
	}
	if foundWithoutRetention {
		t.Error("ListRepostRetentions included an instance with a NULL retention; retention defaults to forever (W1)")
	}
}

func TestDeleteRepostEntriesBeforeRespectsCutoffAndLimit(t *testing.T) {
	ctx := context.Background()
	instanceID, im := createRepostInstance(t, "retention-sweep")

	now := time.Now().UTC()
	for i := 0; i < 3; i++ {
		params := baseRepostParams(instanceID, "youtube:old-"+im+"-"+strconv.Itoa(i))
		params.PostedAt = now.Add(-100 * 24 * time.Hour) // well past any reasonable cutoff
		if _, err := CreateRepostEntry(ctx, params); err != nil {
			t.Fatalf("CreateRepostEntry (old #%d): %v", i, err)
		}
	}

	freshParams := baseRepostParams(instanceID, "youtube:fresh-"+im)
	freshParams.PostedAt = now
	freshID, err := CreateRepostEntry(ctx, freshParams)
	if err != nil {
		t.Fatalf("CreateRepostEntry (fresh): %v", err)
	}

	cutoff := now.Add(-30 * 24 * time.Hour)

	// Limit is below the eligible count, so it is proven enforced rather than
	// accepted as an unused parameter.
	deleted, err := DeleteRepostEntriesBefore(ctx, instanceID, cutoff, 2)
	if err != nil {
		t.Fatalf("DeleteRepostEntriesBefore: %v", err)
	}
	if deleted != 2 {
		t.Fatalf("DeleteRepostEntriesBefore returned %d, want 2 (the limit)", deleted)
	}

	var remainingTotal int
	if err := db().QueryRow(ctx, `SELECT COUNT(*) FROM repost_entry WHERE instance_id = $1`, instanceID).Scan(&remainingTotal); err != nil {
		t.Fatalf("count remaining repost_entry rows: %v", err)
	}
	if remainingTotal != 2 { // 3 old - 2 deleted + 1 fresh
		t.Errorf("remaining repost_entry rows = %d, want 2 (1 old survivor + the fresh one)", remainingTotal)
	}

	var freshStillPresent bool
	if err := db().QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM repost_entry WHERE id = $1)`, freshID).Scan(&freshStillPresent); err != nil {
		t.Fatalf("check fresh entry survival: %v", err)
	}
	if !freshStillPresent {
		t.Error("DeleteRepostEntriesBefore deleted an entry newer than its cutoff")
	}
}

func TestGetDestinationIDByMeta(t *testing.T) {
	ctx := context.Background()
	instanceID, im := createRepostInstance(t, "destmeta")

	destinationMeta := meta(t, map[string]string{"channel_id": "destmeta-" + im})
	destinationID, err := CreateDestination(ctx, instanceID, destinationMeta)
	if err != nil {
		t.Fatalf("CreateDestination: %v", err)
	}

	got, err := GetDestinationIDByMeta(ctx, instanceID, destinationMeta)
	if err != nil {
		t.Fatalf("GetDestinationIDByMeta: %v", err)
	}
	if got != destinationID {
		t.Errorf("GetDestinationIDByMeta = %d, want %d", got, destinationID)
	}
}

func TestGetDestinationIDByMetaReturnsNotFoundForUnknownMeta(t *testing.T) {
	ctx := context.Background()
	instanceID, im := createRepostInstance(t, "destmeta-miss")

	unknownMeta := meta(t, map[string]string{"channel_id": "never-created-" + im})
	_, err := GetDestinationIDByMeta(ctx, instanceID, unknownMeta)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestGetDestinationIDByMetaIsScopedToInstance(t *testing.T) {
	ctx := context.Background()
	instanceAID, imA := createRepostInstance(t, "destmeta-scope-a")
	instanceBID, _ := createRepostInstance(t, "destmeta-scope-b")

	destinationMeta := meta(t, map[string]string{"channel_id": "destmeta-scope-" + imA})
	if _, err := CreateDestination(ctx, instanceAID, destinationMeta); err != nil {
		t.Fatalf("CreateDestination: %v", err)
	}

	if _, err := GetDestinationIDByMeta(ctx, instanceBID, destinationMeta); !errors.Is(err, ErrNotFound) {
		t.Errorf("looking up instance A's destination under instance B: err = %v, want ErrNotFound", err)
	}
}
