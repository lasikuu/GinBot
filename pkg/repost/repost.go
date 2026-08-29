// Package repost splits perceptual hashes into pigeonhole chunks and grades
// verified Hamming distances into confidence tiers. Deterministic, no I/O.
package repost

import (
	"math/bits"

	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
)

// MaxDistance is the widest Hamming distance the 8-chunk pigeonhole guarantee
// covers: with 8 disjoint 8-bit chunks, hashes differing in <= 7 bits always
// match exactly on at least one chunk column. Raising it past 7 loses recall.
const MaxDistance = 7

// ChunkCount is how many disjoint 8-bit chunks a 64-bit hash is split into.
const ChunkCount = 8

// Chunks splits hash into the ChunkCount disjoint 8-bit chunks the pigeonhole
// index is built on, index 0 being the most significant byte. Values are
// 0..255 as int16 to match the smallint columns pgx sends.
func Chunks(hash uint64) [ChunkCount]int16 {
	var chunks [ChunkCount]int16
	for i := range ChunkCount {
		shift := uint((ChunkCount - 1 - i) * 8)
		chunks[i] = int16((hash >> shift) & 0xFF)
	}
	return chunks
}

// Distance returns the Hamming distance between two hashes.
func Distance(a, b uint64) int {
	return bits.OnesCount64(a ^ b)
}

// Tiers holds the inclusive upper bound of each confidence tier, in Hamming
// distance.
type Tiers struct {
	Identical int
	High      int
	Probable  int
}

// DefaultTiers returns the starting boundaries: identical=0, high=3, probable=7.
func DefaultTiers() Tiers {
	return Tiers{Identical: 0, High: 3, Probable: MaxDistance}
}

// Normalise clamps t to [0, MaxDistance] and restores ascending order by
// widening the narrower bound, reporting whether anything had to change.
func (t Tiers) Normalise() (Tiers, bool) {
	out := t
	changed := false

	clamp := func(v int) (int, bool) {
		switch {
		case v < 0:
			return 0, true
		case v > MaxDistance:
			return MaxDistance, true
		default:
			return v, false
		}
	}

	var c bool
	if out.Identical, c = clamp(out.Identical); c {
		changed = true
	}
	if out.High, c = clamp(out.High); c {
		changed = true
	}
	if out.Probable, c = clamp(out.Probable); c {
		changed = true
	}

	if out.High < out.Identical {
		out.High = out.Identical
		changed = true
	}
	if out.Probable < out.High {
		out.Probable = out.High
		changed = true
	}

	return out, changed
}

// Grade maps a verified Hamming distance onto a confidence tier, returning
// REPOST_CONFIDENCE_UNSPECIFIED past t.Probable. Callers must Normalise t first.
func (t Tiers) Grade(distance int) pb.RepostConfidence {
	switch {
	case distance <= t.Identical:
		return pb.RepostConfidence_REPOST_CONFIDENCE_IDENTICAL
	case distance <= t.High:
		return pb.RepostConfidence_REPOST_CONFIDENCE_HIGH
	case distance <= t.Probable:
		return pb.RepostConfidence_REPOST_CONFIDENCE_PROBABLE
	default:
		return pb.RepostConfidence_REPOST_CONFIDENCE_UNSPECIFIED
	}
}
