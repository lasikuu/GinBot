//go:build integration

// Integration tests for the repost/WANHA surface, driven through the bufconn
// harness with the real interceptor chain and a real database.
//
//	docker compose -f docker-compose.psql.yml up -d
//	go test -tags=integration -race -count=1 ./pkg/grpc/server/...
//
// Reuses requireDatabase, uniqueUID, registeredCaller, withOriginResolver,
// cleanupInstanceRows (from reminder_integration_test.go / user_integration_test.go)
// and repostHarness's helpers from repost_test.go (repostCtx is NOT reused —
// every test here needs its OWN unique origin, since the whole point of half
// of these tests is what happens across two different origins or two
// different authors) — none are redeclared here except where noted.
package server

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/lasikuu/GinBot/pkg/db"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
	"github.com/lasikuu/GinBot/pkg/grpc/callermeta"
	"github.com/lasikuu/GinBot/pkg/repost"
	"github.com/lasikuu/GinBot/pkg/repost/fingerprint"
	"github.com/lasikuu/GinBot/pkg/repost/urlnorm"
	"github.com/lasikuu/GinBot/pkg/storage"
	"google.golang.org/protobuf/types/known/structpb"
)

// liveRepostHarness wires both the caller resolver and the origin resolver to
// the real database, exactly as liveTriggerHarness does: CheckRepost resolves
// its calling instance from origin metadata, which needs
// interceptor.NewOriginUnaryInterceptor to have actually bootstrapped the
// instance row.
func liveRepostHarness(t *testing.T, opts ...harnessOption) (*harness, *pgxpool.Pool) {
	t.Helper()
	pool := requireDatabase(t)
	allOpts := append([]harnessOption{
		withResolver(db.GetUserByPlatformUID),
		withOriginResolver(db.GetOrCreateDestinationByMeta),
	}, opts...)
	return newHarness(t, allOpts...), pool
}

// liveRepostCtx attaches caller identity and a caller-chosen origin, in that
// order. Every test in this file builds its own origin rather than reusing
// repostCtx's fixed one, because the behaviour under test is often defined by
// the relationship BETWEEN two calls' origins or authors.
func liveRepostCtx(platformUID string, origin callermeta.Origin) context.Context {
	return callermeta.NewOutgoingOrigin(callerCtx(pb.Platform_PLATFORM_DISCORD, platformUID), origin)
}

// countRepostEntries counts repost_entry rows scoped to one instance, so a
// test can assert an edit truly inserted nothing rather than merely trusting
// the RPC's response shape.
func countRepostEntries(t *testing.T, pool *pgxpool.Pool, instanceMeta *structpb.Struct) int {
	t.Helper()
	var count int
	if err := pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM repost_entry e JOIN instance i ON e.instance_id = i.id WHERE i.instance_meta = $1`,
		instanceMeta,
	).Scan(&count); err != nil {
		t.Fatalf("count repost_entry rows: %v", err)
	}
	return count
}

// ── Excluded / unsupported / refused candidates: skipped, not failed ────────
//
// These need a real database even though the CANDIDATE itself never reaches
// one, because CheckRepost resolves the calling instance from origin metadata
// before it looks at any candidate at all (see repost_test.go's note on this).

func TestCheckRepostSkipsAnExcludedLinkWithoutError(t *testing.T) {
	h, pool := liveRepostHarness(t)

	platformUID, _ := registeredCaller(t, h, pool, "repost-excluded")
	suffix := uniqueUID("excluded")
	origin := callermeta.Origin{InstanceUID: "repost-excluded-instance-" + suffix, DestinationUID: "repost-excluded-dest-" + suffix}
	cleanupInstanceRows(t, pool, origin.InstanceMeta())
	ctx := liveRepostCtx(platformUID, origin)

	resp, err := h.Repost.CheckRepost(ctx, baseCheckRepostReq(
		linkCandidate("https://discord.com/channels/1/2/3"),
	))
	if err != nil {
		t.Fatalf("CheckRepost: unexpected error for an excluded link: %v", err)
	}
	if len(resp.GetMatches()) != 0 {
		t.Errorf("CheckRepost returned %d matches for an excluded link, want 0", len(resp.GetMatches()))
	}
	if got := countRepostEntries(t, pool, origin.InstanceMeta()); got != 0 {
		t.Errorf("an excluded link was indexed anyway: %d repost_entry rows", got)
	}
}

func TestCheckRepostSkipsAnUnsupportedURLWithoutError(t *testing.T) {
	h, pool := liveRepostHarness(t)

	platformUID, _ := registeredCaller(t, h, pool, "repost-unsupported")
	suffix := uniqueUID("unsupported")
	origin := callermeta.Origin{InstanceUID: "repost-unsupported-instance-" + suffix, DestinationUID: "repost-unsupported-dest-" + suffix}
	cleanupInstanceRows(t, pool, origin.InstanceMeta())
	ctx := liveRepostCtx(platformUID, origin)

	resp, err := h.Repost.CheckRepost(ctx, baseCheckRepostReq(
		linkCandidate("not-a-url-at-all"),
	))
	if err != nil {
		t.Fatalf("CheckRepost: unexpected error for an unparseable link: %v", err)
	}
	if len(resp.GetMatches()) != 0 {
		t.Errorf("CheckRepost returned %d matches for an unparseable link, want 0", len(resp.GetMatches()))
	}
}

func TestCheckRepostSkipsARefusedAttachmentFetchWithoutError(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("the media server was contacted; a host-not-allowed fetch must be refused before any request is sent")
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	fetcher := storage.NewFetcher(server.Client().Transport, []string{}, 0) // allow-lists nothing
	repostServer := newRepostServerForTest(fetcher, fingerprint.NewHasher(fingerprint.DefaultGuards(), ""), urlnorm.New(nil), repost.DefaultTiers())

	h, pool := liveRepostHarness(t, withRepostServer(repostServer))

	platformUID, _ := registeredCaller(t, h, pool, "repost-refused-fetch")
	suffix := uniqueUID("refusedfetch")
	origin := callermeta.Origin{InstanceUID: "repost-refused-instance-" + suffix, DestinationUID: "repost-refused-dest-" + suffix}
	cleanupInstanceRows(t, pool, origin.InstanceMeta())
	ctx := liveRepostCtx(platformUID, origin)

	resp, err := h.Repost.CheckRepost(ctx, baseCheckRepostReq(
		imageCandidate(server.URL+"/whatever.png"),
	))
	if err != nil {
		t.Fatalf("CheckRepost: unexpected error for a refused attachment fetch: %v", err)
	}
	if len(resp.GetMatches()) != 0 {
		t.Errorf("CheckRepost returned %d matches for a refused attachment fetch, want 0", len(resp.GetMatches()))
	}
	if got := countRepostEntries(t, pool, origin.InstanceMeta()); got != 0 {
		t.Errorf("a refused attachment fetch was indexed anyway: %d repost_entry rows", got)
	}
}

// ── Link matching, same-author exclusion, edits (AC 7, 12, 13) ──────────────

// TestRepostLinkPostedTwiceByDifferentAuthorsMatchesOnSecondPost is the base
// positive case every other test here is a variation of: the same canonical
// link posted a second time, by a DIFFERENT author, must match with
// REPOST_CONFIDENCE_IDENTICAL (a canonical-link match is never graded by
// distance) and carry the first post's own message reference back.
func TestRepostLinkPostedTwiceByDifferentAuthorsMatchesOnSecondPost(t *testing.T) {
	h, pool := liveRepostHarness(t)

	uidA, _ := registeredCaller(t, h, pool, "repost-link-a")
	uidB, _ := registeredCaller(t, h, pool, "repost-link-b")
	suffix := uniqueUID("linktwice")
	origin := callermeta.Origin{InstanceUID: "repost-link-instance-" + suffix, DestinationUID: "repost-link-dest-" + suffix}
	cleanupInstanceRows(t, pool, origin.InstanceMeta())

	link := "https://example.com/unique-article-" + suffix
	authorRefA := "author-ref-a-" + suffix

	ctxA := liveRepostCtx(uidA, origin)
	firstResp, err := h.Repost.CheckRepost(ctxA, pb.CheckRepostReq_builder{
		Candidates: []*pb.RepostCandidate{linkCandidate(link)},
		MessageUid: strPtr("msg-a-" + suffix),
		AuthorUid:  &authorRefA,
	}.Build())
	if err != nil {
		t.Fatalf("first CheckRepost: %v", err)
	}
	if len(firstResp.GetMatches()) != 0 {
		t.Fatalf("first post already matched something: %+v", firstResp.GetMatches())
	}

	ctxB := liveRepostCtx(uidB, origin)
	secondResp, err := h.Repost.CheckRepost(ctxB, pb.CheckRepostReq_builder{
		Candidates: []*pb.RepostCandidate{linkCandidate(link)},
		MessageUid: strPtr("msg-b-" + suffix),
		AuthorUid:  strPtr("author-ref-b-" + suffix),
	}.Build())
	if err != nil {
		t.Fatalf("second CheckRepost: %v", err)
	}

	matches := secondResp.GetMatches()
	if len(matches) != 1 {
		t.Fatalf("second post got %d matches, want exactly 1: %+v", len(matches), matches)
	}
	match := matches[0]
	if match.GetCandidateIndex() != 0 {
		t.Errorf("candidate_index = %d, want 0", match.GetCandidateIndex())
	}
	if match.GetKind() != pb.RepostKind_REPOST_KIND_LINK {
		t.Errorf("kind = %v, want REPOST_KIND_LINK", match.GetKind())
	}
	if match.GetConfidence() != pb.RepostConfidence_REPOST_CONFIDENCE_IDENTICAL {
		t.Errorf("confidence = %v, want REPOST_CONFIDENCE_IDENTICAL (a canonical-link match)", match.GetConfidence())
	}
	if got := match.GetOriginalRef().GetMessageUid(); got != "msg-a-"+suffix {
		t.Errorf("original_ref.message_uid = %q, want %q (the FIRST post)", got, "msg-a-"+suffix)
	}
	if got := match.GetOriginalRef().GetAuthorUid(); got != authorRefA {
		t.Errorf("original_ref.author_uid = %q, want %q", got, authorRefA)
	}
	if got := match.GetOriginalRef().GetInstanceUid(); got != origin.InstanceUID {
		t.Errorf("original_ref.instance_uid = %q, want %q", got, origin.InstanceUID)
	}
}

// TestRepostSameAuthorRepostingThemselvesDoesNotMatch is AC12, the
// regression test for the exact false-positive class that motivated the
// "excludeUserID" design (docs/plans/wanha.md matching queries): the SAME
// caller posting the same link twice must never see it flagged as a repost of
// themselves.
func TestRepostSameAuthorRepostingThemselvesDoesNotMatch(t *testing.T) {
	h, pool := liveRepostHarness(t)

	platformUID, _ := registeredCaller(t, h, pool, "repost-self")
	suffix := uniqueUID("selfrepost")
	origin := callermeta.Origin{InstanceUID: "repost-self-instance-" + suffix, DestinationUID: "repost-self-dest-" + suffix}
	cleanupInstanceRows(t, pool, origin.InstanceMeta())

	link := "https://example.com/self-repost-" + suffix
	ctx := liveRepostCtx(platformUID, origin)

	firstResp, err := h.Repost.CheckRepost(ctx, pb.CheckRepostReq_builder{
		Candidates: []*pb.RepostCandidate{linkCandidate(link)},
		MessageUid: strPtr("msg-first-" + suffix),
		AuthorUid:  strPtr("author-" + suffix),
	}.Build())
	if err != nil {
		t.Fatalf("first CheckRepost: %v", err)
	}
	if len(firstResp.GetMatches()) != 0 {
		t.Fatalf("first post already matched: %+v", firstResp.GetMatches())
	}

	secondResp, err := h.Repost.CheckRepost(ctx, pb.CheckRepostReq_builder{
		Candidates: []*pb.RepostCandidate{linkCandidate(link)},
		MessageUid: strPtr("msg-second-" + suffix),
		AuthorUid:  strPtr("author-" + suffix),
	}.Build())
	if err != nil {
		t.Fatalf("second (self-repost) CheckRepost: %v", err)
	}
	if len(secondResp.GetMatches()) != 0 {
		t.Errorf("the same author reposting their own link matched: %+v", secondResp.GetMatches())
	}
}

// TestRepostEditMatchesButDoesNotInsert is AC13. The assertion that actually
// catches the self-match bug the design calls out (W8) is the unchanged row
// count, not merely that the RPC returned successfully.
func TestRepostEditMatchesButDoesNotInsert(t *testing.T) {
	h, pool := liveRepostHarness(t)

	uidA, _ := registeredCaller(t, h, pool, "repost-edit-a")
	uidB, _ := registeredCaller(t, h, pool, "repost-edit-b")
	suffix := uniqueUID("editmatch")
	origin := callermeta.Origin{InstanceUID: "repost-edit-instance-" + suffix, DestinationUID: "repost-edit-dest-" + suffix}
	cleanupInstanceRows(t, pool, origin.InstanceMeta())

	link := "https://example.com/edit-target-" + suffix

	ctxA := liveRepostCtx(uidA, origin)
	_, err := h.Repost.CheckRepost(ctxA, pb.CheckRepostReq_builder{
		Candidates: []*pb.RepostCandidate{linkCandidate(link)},
		MessageUid: strPtr("msg-original-" + suffix),
		AuthorUid:  strPtr("author-a-" + suffix),
	}.Build())
	if err != nil {
		t.Fatalf("original CheckRepost: %v", err)
	}

	countAfterOriginal := countRepostEntries(t, pool, origin.InstanceMeta())
	if countAfterOriginal != 1 {
		t.Fatalf("repost_entry count after the original post = %d, want 1", countAfterOriginal)
	}

	// B edits their own, separate message to add the same link. edit=true must
	// still MATCH (B is not A, so same-author exclusion does not apply) but
	// must NOT insert a second row.
	ctxB := liveRepostCtx(uidB, origin)
	editResp, err := h.Repost.CheckRepost(ctxB, pb.CheckRepostReq_builder{
		Candidates: []*pb.RepostCandidate{linkCandidate(link)},
		MessageUid: strPtr("msg-edited-" + suffix),
		AuthorUid:  strPtr("author-b-" + suffix),
		Edit:       boolPtr(true),
	}.Build())
	if err != nil {
		t.Fatalf("edit CheckRepost: %v", err)
	}
	if len(editResp.GetMatches()) != 1 {
		t.Errorf("edit that reposts an existing link got %d matches, want 1", len(editResp.GetMatches()))
	}

	countAfterEdit := countRepostEntries(t, pool, origin.InstanceMeta())
	if countAfterEdit != countAfterOriginal {
		t.Errorf("repost_entry count changed from %d to %d after an edit; edits must never insert", countAfterOriginal, countAfterEdit)
	}
}

// TestRepostEditOfAnUnseenLinkStillDoesNotInsert covers the other half of
// AC13: an edit that finds NO match must still not seed the index, or the
// message would become able to match itself on a later, unrelated lookup.
func TestRepostEditOfAnUnseenLinkStillDoesNotInsert(t *testing.T) {
	h, pool := liveRepostHarness(t)

	platformUID, _ := registeredCaller(t, h, pool, "repost-edit-unseen")
	suffix := uniqueUID("editunseen")
	origin := callermeta.Origin{InstanceUID: "repost-edit-unseen-instance-" + suffix, DestinationUID: "repost-edit-unseen-dest-" + suffix}
	cleanupInstanceRows(t, pool, origin.InstanceMeta())

	ctx := liveRepostCtx(platformUID, origin)
	_, err := h.Repost.CheckRepost(ctx, pb.CheckRepostReq_builder{
		Candidates: []*pb.RepostCandidate{linkCandidate("https://example.com/never-seen-before-" + suffix)},
		MessageUid: strPtr("msg-" + suffix),
		AuthorUid:  strPtr("author-" + suffix),
		Edit:       boolPtr(true),
	}.Build())
	if err != nil {
		t.Fatalf("CheckRepost: %v", err)
	}

	if got := countRepostEntries(t, pool, origin.InstanceMeta()); got != 0 {
		t.Errorf("an edit with no match still inserted %d repost_entry row(s)", got)
	}
}

// ── Per-instance isolation (AC 11) ───────────────────────────────────────────

// TestRepostMatchingIsScopedPerInstance: the exact same link, posted by two
// DIFFERENT authors in two DIFFERENT instances (guilds), must never match
// across them. Using different authors as well as different instances is
// deliberate: if this test used the same author it would be indistinguishable
// from the same-author-exclusion test above, and would not actually prove
// instance scoping is what is doing the work.
func TestRepostMatchingIsScopedPerInstance(t *testing.T) {
	h, pool := liveRepostHarness(t)

	uidA, _ := registeredCaller(t, h, pool, "repost-scope-a")
	uidB, _ := registeredCaller(t, h, pool, "repost-scope-b")
	suffix := uniqueUID("scoped")

	originA := callermeta.Origin{InstanceUID: "repost-scope-a-instance-" + suffix, DestinationUID: "repost-scope-a-dest-" + suffix}
	originB := callermeta.Origin{InstanceUID: "repost-scope-b-instance-" + suffix, DestinationUID: "repost-scope-b-dest-" + suffix}
	cleanupInstanceRows(t, pool, originA.InstanceMeta())
	cleanupInstanceRows(t, pool, originB.InstanceMeta())

	link := "https://example.com/cross-guild-" + suffix

	_, err := h.Repost.CheckRepost(liveRepostCtx(uidA, originA), pb.CheckRepostReq_builder{
		Candidates: []*pb.RepostCandidate{linkCandidate(link)},
		MessageUid: strPtr("msg-guild-a-" + suffix),
		AuthorUid:  strPtr("author-a-" + suffix),
	}.Build())
	if err != nil {
		t.Fatalf("guild A CheckRepost: %v", err)
	}

	respB, err := h.Repost.CheckRepost(liveRepostCtx(uidB, originB), pb.CheckRepostReq_builder{
		Candidates: []*pb.RepostCandidate{linkCandidate(link)},
		MessageUid: strPtr("msg-guild-b-" + suffix),
		AuthorUid:  strPtr("author-b-" + suffix),
	}.Build())
	if err != nil {
		t.Fatalf("guild B CheckRepost: %v", err)
	}

	if len(respB.GetMatches()) != 0 {
		t.Errorf("a link posted in guild A matched in guild B: %+v", respB.GetMatches())
	}
}

// ── Perceptual image matching (AC 8, 9) end to end through the RPC ──────────

// repostFixtureImage renders a deterministic, structured, high-entropy image:
// a gradient plus a couple of solid shapes whose layout depends on variant, so
// two different variants are genuinely visually distinct rather than only
// differing in noise. Mirrors the fixture-building rationale in
// pkg/repost/fingerprint's own tests, duplicated here (rather than imported,
// since it is unexported there) because this test needs the same property:
// content real enough for a DCT-based perceptual hash to be meaningful about.
func repostFixtureImage(size, variant int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, size, size))

	// Seeded per variant and mixed into every channel: a pure gradient with a
	// naive R/G/B weight choice can land almost exactly on ITU-R luma's zero
	// point (0.299R + 0.587G + 0.114B), collapsing entropy and tripping
	// ErrLowEntropy regardless of the guard being correct. See the identical
	// note in pkg/repost/fingerprint/confidence_test.go's distinctImage,
	// which hit exactly this.
	rng := rand.New(rand.NewPCG(uint64(variant)*7919+1, uint64(variant)*104729+7))

	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			var base uint8
			if variant%2 == 0 {
				base = uint8((x*255)/size+(y*255)/size) / 2
			} else {
				base = uint8((y * 255) / size)
			}

			noise := uint8(rng.IntN(60))
			r := base + noise
			g := 255 - base
			b := base/2 + noise/2
			img.Set(x, y, color.RGBA{R: r, G: g, B: b, A: 255})
		}
	}

	blockX, blockY := (size/6)*(1+variant%3), (size/6)*(1+(variant+1)%3)
	blockSize := size / 4
	mark := color.RGBA{R: uint8(40 * variant % 255), G: uint8(90 * variant % 255), B: uint8(180 * variant % 255), A: 255}
	for y := blockY; y < blockY+blockSize && y < size; y++ {
		for x := blockX; x < blockX+blockSize && x < size; x++ {
			img.Set(x, y, mark)
		}
	}

	return img
}

func encodeRepostPNG(t *testing.T, img image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png fixture: %v", err)
	}
	return buf.Bytes()
}

func encodeRepostJPEG(t *testing.T, img image.Image, quality int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality}); err != nil {
		t.Fatalf("encode jpeg fixture: %v", err)
	}
	return buf.Bytes()
}

// repostImageServer starts an httptest TLS server serving fixed bodies at
// fixed paths. Use hostOnly(t, server.URL) to get its hostname for a
// Fetcher's allow-list.
func repostImageServer(t *testing.T, bodies map[string][]byte) *httptest.Server {
	t.Helper()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := bodies[r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if _, err := w.Write(body); err != nil {
			t.Errorf("image server write for %s: %v", r.URL.Path, err)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

// TestRepostRecompressedImageGradesIdenticalOrHigh is AC8, exercised through
// the full RPC: fetch, guard, fingerprint, index, look up.
func TestRepostRecompressedImageGradesIdenticalOrHigh(t *testing.T) {
	guards := fingerprint.DefaultGuards()
	size := guards.MinWidth * 3
	original := repostFixtureImage(size, 0)

	pngBody := encodeRepostPNG(t, original)
	jpegBody := encodeRepostJPEG(t, original, 75)

	server := repostImageServer(t, map[string][]byte{
		"/original.png":     pngBody,
		"/recompressed.jpg": jpegBody,
	})

	fetcher := storage.NewFetcher(server.Client().Transport, []string{hostOnly(t, server.URL)}, 0)
	repostServer := newRepostServerForTest(fetcher, fingerprint.NewHasher(guards, ""), urlnorm.New(nil), repost.DefaultTiers())

	h, pool := liveRepostHarness(t, withRepostServer(repostServer))

	uidA, _ := registeredCaller(t, h, pool, "repost-image-a")
	uidB, _ := registeredCaller(t, h, pool, "repost-image-b")
	suffix := uniqueUID("imgmatch")
	origin := callermeta.Origin{InstanceUID: "repost-image-instance-" + suffix, DestinationUID: "repost-image-dest-" + suffix}
	cleanupInstanceRows(t, pool, origin.InstanceMeta())

	_, err := h.Repost.CheckRepost(liveRepostCtx(uidA, origin), pb.CheckRepostReq_builder{
		Candidates: []*pb.RepostCandidate{imageCandidate(server.URL + "/original.png")},
		MessageUid: strPtr("msg-image-original-" + suffix),
		AuthorUid:  strPtr("author-a-" + suffix),
	}.Build())
	if err != nil {
		t.Fatalf("original image CheckRepost: %v", err)
	}

	resp, err := h.Repost.CheckRepost(liveRepostCtx(uidB, origin), pb.CheckRepostReq_builder{
		Candidates: []*pb.RepostCandidate{imageCandidate(server.URL + "/recompressed.jpg")},
		MessageUid: strPtr("msg-image-recompressed-" + suffix),
		AuthorUid:  strPtr("author-b-" + suffix),
	}.Build())
	if err != nil {
		t.Fatalf("recompressed image CheckRepost: %v", err)
	}

	matches := resp.GetMatches()
	if len(matches) != 1 {
		t.Fatalf("recompressed image got %d matches, want 1", len(matches))
	}
	confidence := matches[0].GetConfidence()
	t.Logf("recompressed image match: confidence=%v distance=%d", confidence, matches[0].GetDistance())
	if confidence != pb.RepostConfidence_REPOST_CONFIDENCE_IDENTICAL && confidence != pb.RepostConfidence_REPOST_CONFIDENCE_HIGH {
		t.Errorf("confidence = %v, want IDENTICAL or HIGH", confidence)
	}
}

// TestRepostUnrelatedImageProducesNoMatch is AC9 through the full RPC: two
// visually distinct images, fetched, guarded, hashed and indexed for real,
// must not match each other.
func TestRepostUnrelatedImageProducesNoMatch(t *testing.T) {
	guards := fingerprint.DefaultGuards()
	size := guards.MinWidth * 3

	server := repostImageServer(t, map[string][]byte{
		"/first.png":  encodeRepostPNG(t, repostFixtureImage(size, 0)),
		"/second.png": encodeRepostPNG(t, repostFixtureImage(size, 1)),
	})

	fetcher := storage.NewFetcher(server.Client().Transport, []string{hostOnly(t, server.URL)}, 0)
	repostServer := newRepostServerForTest(fetcher, fingerprint.NewHasher(guards, ""), urlnorm.New(nil), repost.DefaultTiers())

	h, pool := liveRepostHarness(t, withRepostServer(repostServer))

	uidA, _ := registeredCaller(t, h, pool, "repost-unrelated-a")
	uidB, _ := registeredCaller(t, h, pool, "repost-unrelated-b")
	suffix := uniqueUID("imgunrelated")
	origin := callermeta.Origin{InstanceUID: "repost-unrelated-instance-" + suffix, DestinationUID: "repost-unrelated-dest-" + suffix}
	cleanupInstanceRows(t, pool, origin.InstanceMeta())

	_, err := h.Repost.CheckRepost(liveRepostCtx(uidA, origin), pb.CheckRepostReq_builder{
		Candidates: []*pb.RepostCandidate{imageCandidate(server.URL + "/first.png")},
		MessageUid: strPtr("msg-unrelated-first-" + suffix),
		AuthorUid:  strPtr("author-a-" + suffix),
	}.Build())
	if err != nil {
		t.Fatalf("first image CheckRepost: %v", err)
	}

	resp, err := h.Repost.CheckRepost(liveRepostCtx(uidB, origin), pb.CheckRepostReq_builder{
		Candidates: []*pb.RepostCandidate{imageCandidate(server.URL + "/second.png")},
		MessageUid: strPtr("msg-unrelated-second-" + suffix),
		AuthorUid:  strPtr("author-b-" + suffix),
	}.Build())
	if err != nil {
		t.Fatalf("second image CheckRepost: %v", err)
	}

	if matches := resp.GetMatches(); len(matches) != 0 {
		t.Errorf("an unrelated image matched: %+v", matches)
	}
}

// hostOnly extracts the bare hostname from a URL, for the fetcher's allow-list.
func hostOnly(t *testing.T, rawURL string) string {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse %q: %v", rawURL, err)
	}
	return u.Hostname()
}

func strPtr(s string) *string { return &s }
func boolPtr(b bool) *bool    { return &b }
