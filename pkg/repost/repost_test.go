package repost

import (
	"math/bits"
	"math/rand/v2"
	"strconv"
	"testing"

	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
)

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

func TestChunksIndexZeroIsMostSignificantByte(t *testing.T) {
	hash := uint64(0xAB23456789ABCD01)

	chunks := Chunks(hash)
	if chunks[0] != 0xAB {
		t.Errorf("Chunks(...)[0] = %#x, want 0xab (most significant byte)", chunks[0])
	}
	if chunks[7] != 0x01 {
		t.Errorf("Chunks(...)[7] = %#x, want 0x01 (least significant byte)", chunks[7])
	}
}

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
		{1, 2, 2},
	}

	for _, tt := range tests {
		if got := Distance(tt.a, tt.b); got != tt.want {
			t.Errorf("Distance(%#x, %#x) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
		if got := Distance(tt.b, tt.a); got != tt.want {
			t.Errorf("Distance(%#x, %#x) [swapped] = %d, want %d", tt.b, tt.a, got, tt.want)
		}
	}
}

// TestDistanceAgreesWithOnesCountXOR pins Distance to the same function
// Postgres bit_count(a # b) computes; the Postgres half is in pkg/db.
func TestDistanceAgreesWithOnesCountXOR(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))

	for range 500 {
		a := rng.Uint64()
		b := rng.Uint64()

		want := bits.OnesCount64(a ^ b)
		if got := Distance(a, b); got != want {
			t.Fatalf("Distance(%#x, %#x) = %d, want %d (bits.OnesCount64 of the XOR)", a, b, got, want)
		}
	}
}

// pigeonholeSeed is fixed so a counter-example reproduces exactly.
const pigeonholeSeed = 20260823

// flipBits returns hash with exactly k distinct random bit positions flipped.
func flipBits(rng *rand.Rand, hash uint64, k int) uint64 {
	positions := rng.Perm(64)[:k]
	for _, p := range positions {
		hash ^= 1 << uint(p)
	}
	return hash
}

// sharesAChunk mirrors the SQL candidate-set query's eight-way chunk OR.
func sharesAChunk(a, b uint64) bool {
	ca, cb := Chunks(a), Chunks(b)
	for i := range ca {
		if ca[i] == cb[i] {
			return true
		}
	}
	return false
}

// TestPigeonholeGuaranteeWithinDistanceSeven asserts the property the SQL
// candidate-set query depends on: hashes within distance 7 share a chunk.
func TestPigeonholeGuaranteeWithinDistanceSeven(t *testing.T) {
	rng := rand.New(rand.NewPCG(pigeonholeSeed, pigeonholeSeed))

	const trialsPerDistance = 200

	for k := 0; k <= MaxDistance; k++ {
		k := k
		t.Run(distanceLabel(k), func(t *testing.T) {
			for trial := range trialsPerDistance {
				original := rng.Uint64()
				flipped := flipBits(rng, original, k)

				if got := Distance(original, flipped); got != k {
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

// TestPigeonholeCandidatesAboveSevenAreNotGuaranteed: the verifier, not the
// chunk match, rejects a too-distant pair.
func TestPigeonholeCandidatesAboveSevenAreNotGuaranteed(t *testing.T) {
	rng := rand.New(rand.NewPCG(pigeonholeSeed, pigeonholeSeed+1))

	for _, k := range []int{8, 16, 32} {
		original := rng.Uint64()
		flipped := flipBits(rng, original, k)

		t.Run(distanceLabel(k), func(t *testing.T) {
			distance := Distance(original, flipped)
			if distance != k {
				t.Fatalf("fixture distance = %d, want exactly %d; flipBits is not flipping distinct positions", distance, k)
			}

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
			if got.Identical > got.High || got.High > got.Probable {
				t.Errorf("Normalise(%+v) = %+v, not monotonic", tt.in, got)
			}
			if got.Probable > MaxDistance {
				t.Errorf("Normalise(%+v) = %+v, Probable exceeds MaxDistance", tt.in, got)
			}
		})
	}
}

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

// Package-level so the compiler cannot prove the results unused.
var (
	benchmarkChunkSink    [ChunkCount]int16
	benchmarkDistanceSink int
)

// benchmarkHashes varies the input so constant folding cannot flatter the numbers.
var benchmarkHashes = [...]uint64{
	0,
	^uint64(0),
	0x0123456789ABCDEF,
	0xFEDCBA9876543210,
	0xAAAAAAAAAAAAAAAA,
	0x5555555555555555,
	0x00FF00FF00FF00FF,
	1 << 63,
}

func BenchmarkChunks(b *testing.B) {
	i := 0
	for b.Loop() {
		benchmarkChunkSink = Chunks(benchmarkHashes[i%len(benchmarkHashes)])
		i++
	}
}

func BenchmarkDistance(b *testing.B) {
	i := 0
	for b.Loop() {
		a := benchmarkHashes[i%len(benchmarkHashes)]
		c := benchmarkHashes[(i+3)%len(benchmarkHashes)]
		benchmarkDistanceSink = Distance(a, c)
		i++
	}
}
