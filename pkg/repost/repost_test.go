package repost

import (
	"math/bits"
	"math/rand/v2"
	"strconv"
	"testing"

	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/proto"
)

// ── Assumed symbols from pkg/repost (spec §3.2) ───────────────────────────────
//
// Recorded because these are the symbols the tests below depend on, so a change
// to any of them is a deliberate decision rather than a surprise.
//
//	const MaxDistance = 7
//	const ChunkCount = 8
//
//	func Chunks(hash uint64) [ChunkCount]int16
//	func Distance(a, b uint64) int
//
//	type Tiers struct {
//		Identical int
//		High      int
//		Probable  int
//	}
//
//	func DefaultTiers() Tiers
//	func (t Tiers) Normalise() (Tiers, bool)
//	func (t Tiers) Grade(distance int) pb.RepostConfidence

// ── Chunks ────────────────────────────────────────────────────────────────────

// TestChunksRoundTripsTheHash: reassembling the eight 8-bit chunks, most
// significant first (index 0), must reproduce the original 64-bit hash
// exactly. If this does not hold the pigeonhole index is indexing something
// other than the hash it claims to.
func TestChunksRoundTripsTheHash(t *testing.T) {
	hashes := []uint64{
		0,
		^uint64(0),
		0x0123456789ABCDEF,
		0xFEDCBA9876543210,
		1,
		1 << 63,
	}

	for _, hash := range hashes {
		chunks := Chunks(hash)

		if len(chunks) != ChunkCount {
			t.Fatalf("Chunks(%#x) returned %d chunks, want %d", hash, len(chunks), ChunkCount)
		}

		var rebuilt uint64
		for _, c := range chunks {
			if c < 0 || c > 255 {
				t.Errorf("Chunks(%#x) chunk = %d, want 0..255", hash, c)
			}
			rebuilt = (rebuilt << 8) | uint64(c)
		}

		if rebuilt != hash {
			t.Errorf("Chunks(%#x) round-tripped to %#x", hash, rebuilt)
		}
	}
}

// TestChunksIndexZeroIsMostSignificantByte pins the documented byte order
// directly, since the pigeonhole SQL query binds c0..c7 positionally and a
// silently reversed order would still round-trip in the test above but bind
// the wrong chunk to the wrong indexed column in production.
func TestChunksIndexZeroIsMostSignificantByte(t *testing.T) {
	// Byte 0 (MSB) = 0xAB, byte 7 (LSB) = 0x01.
	hash := uint64(0xAB23456789ABCD01)

	chunks := Chunks(hash)
	if chunks[0] != 0xAB {
		t.Errorf("Chunks(...)[0] = %#x, want 0xab (most significant byte)", chunks[0])
	}
	if chunks[7] != 0x01 {
		t.Errorf("Chunks(...)[7] = %#x, want 0x01 (least significant byte)", chunks[7])
	}
}

// ── Distance ──────────────────────────────────────────────────────────────────

// TestDistanceKnownValues pins Distance against hand-computed Hamming
// distances and against Go's own bits.OnesCount64 of the XOR, which is the
// textbook Hamming-distance definition and the same computation Postgres's
// bit_count(a # b) performs.
func TestDistanceKnownValues(t *testing.T) {
	tests := []struct {
		a, b uint64
		want int
	}{
		{0, 0, 0},
		{0, ^uint64(0), 64},
		{0b0000, 0b0001, 1},
		{0b0000, 0b1111, 4},
		{0xFF00FF00FF00FF00, 0xFF00FF00FF00FF00, 0},
		{1, 2, 2}, // 0b01 vs 0b10
	}

	for _, tt := range tests {
		if got := Distance(tt.a, tt.b); got != tt.want {
			t.Errorf("Distance(%#x, %#x) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
		// Distance must be symmetric.
		if got := Distance(tt.b, tt.a); got != tt.want {
			t.Errorf("Distance(%#x, %#x) [swapped] = %d, want %d", tt.b, tt.a, got, tt.want)
		}
	}
}

// TestDistanceAgreesWithOnesCountXOR is the Go-side half of the "bit_count
// agreement" test guidance: Distance must equal bits.OnesCount64(a^b) for a
// spread of random pairs, since that is exactly what Postgres's
// bit_count(a # b) computes and the whole pigeonhole guarantee depends on the
// two being the same function. (The Postgres-side half needs a live database
// and lives in pkg/db's integration suite.)
func TestDistanceAgreesWithOnesCountXOR(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))

	for i := 0; i < 500; i++ {
		a := rng.Uint64()
		b := rng.Uint64()

		want := bits.OnesCount64(a ^ b)
		if got := Distance(a, b); got != want {
			t.Fatalf("Distance(%#x, %#x) = %d, want %d (bits.OnesCount64 of the XOR)", a, b, got, want)
		}
	}
}

// ── Pigeonhole correctness (property test) ───────────────────────────────────
//
// pigeonholeSeed is fixed so a failure is reproducible: the property under
// test is the correctness claim the whole index design rests on (ADR-0005,
// docs/plans/wanha.md "why this is exact, not approximate"), so a flake here
// is never something to shrug off and rerun — it has to point at the same
// counter-example every time until fixed.
const pigeonholeSeed = 20260823

// flipBits returns hash with exactly k distinct random bit positions flipped.
func flipBits(rng *rand.Rand, hash uint64, k int) uint64 {
	positions := rng.Perm(64)[:k]
	for _, p := range positions {
		hash ^= 1 << uint(p)
	}
	return hash
}

// sharesAChunk reports whether a and b share at least one of the eight
// pigeonhole chunks exactly, which is what the SQL candidate-set query tests
// via 8-way OR.
func sharesAChunk(a, b uint64) bool {
	ca, cb := Chunks(a), Chunks(b)
	for i := range ca {
		if ca[i] == cb[i] {
			return true
		}
	}
	return false
}

// TestPigeonholeGuaranteeWithinDistanceSeven is the property test the design
// document calls out by name: split a 64-bit hash into 8 disjoint 8-bit
// chunks, and two hashes differing in at most 7 bit positions MUST share at
// least one chunk exactly. If this ever fails, the SQL candidate-set query can
// silently miss a true match — a false negative that no amount of query
// testing against Postgres would catch, because the query is only as correct
// as this claim.
//
// Seed: pigeonholeSeed, fixed above, so a failing case reproduces exactly.
func TestPigeonholeGuaranteeWithinDistanceSeven(t *testing.T) {
	rng := rand.New(rand.NewPCG(pigeonholeSeed, pigeonholeSeed))

	const trialsPerDistance = 200

	for k := 0; k <= MaxDistance; k++ {
		k := k
		t.Run(distanceLabel(k), func(t *testing.T) {
			for trial := 0; trial < trialsPerDistance; trial++ {
				original := rng.Uint64()
				flipped := flipBits(rng, original, k)

				if got := Distance(original, flipped); got != k {
					// flipBits can flip the same bit twice only if k > 64,
					// which cannot happen here (k <= MaxDistance == 7), but
					// this pins the fixture itself is honest about the
					// distance it claims to construct.
					t.Fatalf("fixture distance = %d, want %d (original=%#x flipped=%#x)",
						got, k, original, flipped)
				}

				if !sharesAChunk(original, flipped) {
					t.Fatalf("pigeonhole guarantee violated at distance %d: "+
						"original=%#x flipped=%#x share no chunk (seed=%d, trial=%d)",
						k, original, flipped, pigeonholeSeed, trial)
				}
			}
		})
	}
}

// TestPigeonholeCandidatesAboveSevenAreNotGuaranteed is the other half of the
// property test: past the pigeonhole ceiling the guarantee no longer holds, so
// the verifier (Distance <= MaxDistance) is what has to reject a too-distant
// pair — the chunk match is not a safety net there. This does not assert that
// EVERY distance-8+ pair fails to share a chunk (some will, by chance; the
// guarantee is a floor at <=7, not a ceiling at >7) — it asserts that the
// verifier, not the pigeonhole match, is what draws the line.
func TestPigeonholeCandidatesAboveSevenAreNotGuaranteed(t *testing.T) {
	rng := rand.New(rand.NewPCG(pigeonholeSeed, pigeonholeSeed+1))

	for _, k := range []int{8, 16, 32} {
		original := rng.Uint64()
		flipped := flipBits(rng, original, k)

		t.Run(distanceLabel(k), func(t *testing.T) {
			// Asserted rather than used as a condition. Wrapping the real
			// assertion in `if Distance(...) > MaxDistance` made this test pass
			// silently whenever the fixture was wrong — exactly the failure
			// mode a property test exists to catch. flipBits flips k DISTINCT
			// positions, so the distance is k by construction, and if that ever
			// stops being true this should fail rather than skip.
			distance := Distance(original, flipped)
			if distance != k {
				t.Fatalf("fixture distance = %d, want exactly %d; flipBits is not flipping distinct positions", distance, k)
			}

			// Regardless of whether they happen to share a chunk, the verifier
			// must be the thing that refuses this pair.
			if got := DefaultTiers().Grade(distance); got != pb.RepostConfidence_REPOST_CONFIDENCE_UNSPECIFIED {
				t.Errorf("a distance-%d pair (over MaxDistance=%d) graded as %v, want UNSPECIFIED",
					k, MaxDistance, got)
			}
		})
	}
}

func distanceLabel(k int) string {
	return "distance=" + strconv.Itoa(k)
}

// ── Tiers ─────────────────────────────────────────────────────────────────────

// TestDefaultTiersBoundaries pins the documented starting boundaries: 0, 3, 7.
func TestDefaultTiersBoundaries(t *testing.T) {
	got := DefaultTiers()
	if got.Identical != 0 {
		t.Errorf("DefaultTiers().Identical = %d, want 0", got.Identical)
	}
	if got.High != 3 {
		t.Errorf("DefaultTiers().High = %d, want 3", got.High)
	}
	if got.Probable != 7 {
		t.Errorf("DefaultTiers().Probable = %d, want 7", got.Probable)
	}
}

// TestGradeBoundaries covers every documented tier edge: 0 is IDENTICAL, 1 and
// 3 bound HIGH, 4 and 7 bound PROBABLE, and anything past Probable is
// UNSPECIFIED (no match) — the exact rule that keeps a heavily-edited image
// from being reported as any kind of repost at all.
func TestGradeBoundaries(t *testing.T) {
	tiers := DefaultTiers()

	tests := []struct {
		distance int
		want     pb.RepostConfidence
	}{
		{0, pb.RepostConfidence_REPOST_CONFIDENCE_IDENTICAL},
		{1, pb.RepostConfidence_REPOST_CONFIDENCE_HIGH},
		{3, pb.RepostConfidence_REPOST_CONFIDENCE_HIGH},
		{4, pb.RepostConfidence_REPOST_CONFIDENCE_PROBABLE},
		{7, pb.RepostConfidence_REPOST_CONFIDENCE_PROBABLE},
		{8, pb.RepostConfidence_REPOST_CONFIDENCE_UNSPECIFIED},
		{64, pb.RepostConfidence_REPOST_CONFIDENCE_UNSPECIFIED},
	}

	for _, tt := range tests {
		if got := tiers.Grade(tt.distance); got != tt.want {
			t.Errorf("DefaultTiers().Grade(%d) = %v, want %v", tt.distance, got, tt.want)
		}
	}
}

// TestNormaliseRefusesAProbableBoundaryAboveMaxDistance is the sharpest edge in
// the whole tiering scheme: the pigeonhole index only guarantees recall up to
// MaxDistance (7). A Probable boundary configured above that would silently
// promise matches the SQL candidate-set query cannot actually find — a false
// sense of coverage that quietly loses recall in production. Normalise must
// refuse to let that boundary through unclamped.
func TestNormaliseRefusesAProbableBoundaryAboveMaxDistance(t *testing.T) {
	bad := Tiers{Identical: 0, High: 3, Probable: MaxDistance + 5}

	got, changed := bad.Normalise()

	if !changed {
		t.Error("Normalise() reported no change for a Probable boundary above MaxDistance")
	}
	if got.Probable > MaxDistance {
		t.Errorf("Normalise().Probable = %d, want <= MaxDistance (%d)", got.Probable, MaxDistance)
	}
}

// TestNormaliseEnforcesMonotonicity: Identical <= High <= Probable must hold
// after normalisation, however the input was scrambled, because Grade's
// if/else-style boundary checks assume that ordering.
func TestNormaliseEnforcesMonotonicity(t *testing.T) {
	tests := []struct {
		name string
		in   Tiers
	}{
		{"high below identical", Tiers{Identical: 5, High: 2, Probable: 7}},
		{"probable below high", Tiers{Identical: 0, High: 6, Probable: 3}},
		{"all inverted", Tiers{Identical: 7, High: 3, Probable: 0}},
		{"negative identical", Tiers{Identical: -3, High: 3, Probable: 7}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, changed := tt.in.Normalise()
			if !changed {
				t.Errorf("Normalise() on out-of-order input %+v reported no change", tt.in)
			}
			if !(got.Identical <= got.High && got.High <= got.Probable) {
				t.Errorf("Normalise(%+v) = %+v, not monotonic", tt.in, got)
			}
			if got.Probable > MaxDistance {
				t.Errorf("Normalise(%+v) = %+v, Probable exceeds MaxDistance", tt.in, got)
			}
		})
	}
}

// TestNormaliseLeavesAnAlreadyValidTiersUnchanged: applying Normalise to
// DefaultTiers() must be a no-op, so configuration that is already sane is not
// perturbed by validation on every load.
func TestNormaliseLeavesAnAlreadyValidTiersUnchanged(t *testing.T) {
	valid := DefaultTiers()

	got, changed := valid.Normalise()
	if changed {
		t.Errorf("Normalise() changed an already-valid Tiers %+v into %+v", valid, got)
	}
	if got != valid {
		t.Errorf("Normalise() = %+v, want the input unchanged: %+v", got, valid)
	}
}
