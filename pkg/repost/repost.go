// Package repost implements the pure, offline half of WANHA repost detection:
// splitting a perceptual hash into pigeonhole chunks and grading a verified
// Hamming distance into a confidence tier. Everything here is deterministic
// and does no I/O — the database and network sides live in pkg/db and
// pkg/repost/fingerprint respectively.
package repost

import (
	"math/bits"

	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
)

// MaxDistance is the widest Hamming distance the 8-chunk pigeonhole guarantee
// covers.
//
// Splitting a 64-bit hash into 8 disjoint 8-bit chunks means that if two
// hashes differ in at most 7 bit positions, then by the pigeonhole principle
// at least one of the 8 chunks contains none of those differing bits and
// therefore matches exactly. So every true match within distance <= 7 is
// GUARANTEED to appear in a btree lookup on any one of the 8 chunk columns —
// no recall is lost, and bit_count then verifies and discards the false
// candidates. Raising this past 7 without adding chunks would silently break
// that guarantee: a match could differ in 8 bits with each chunk carrying
// exactly one of them, matching zero columns.
const MaxDistance = 7

// ChunkCount is how many disjoint 8-bit chunks a 64-bit hash is split into.
const ChunkCount = 8

// Chunks splits hash into the ChunkCount disjoint 8-bit chunks the pigeonhole
// index is built on. Index 0 is the most significant byte, matching how
// repost_fingerprint.c0..c7 are populated and queried in pkg/db.
//
// Values are 0..255, returned as int16 to match the smallint columns: a plain
// byte would work just as well, but int16 is what pgx sends for a smallint
// parameter without an explicit conversion at every call site.
func Chunks(hash uint64) [ChunkCount]int16 {
	var chunks [ChunkCount]int16
	for i := 0; i < ChunkCount; i++ {
		shift := uint((ChunkCount - 1 - i) * 8)
		chunks[i] = int16((hash >> shift) & 0xFF)
	}
	return chunks
}

// Distance returns the Hamming distance between two hashes: the number of
// bit positions at which they differ.
func Distance(a, b uint64) int {
	return bits.OnesCount64(a ^ b)
}

// Tiers holds the inclusive upper bound of each confidence tier, in Hamming
// distance. Boundaries are configuration (W9), not constants: they are
// starting points from the literature and are expected to be tuned against
// real traffic once the feature is live.
type Tiers struct {
	Identical int
	High      int
	Probable  int
}

// DefaultTiers returns the starting boundaries: identical=0, high=3,
// probable=7. See docs/plans/wanha.md "Confidence grading (W9)".
func DefaultTiers() Tiers {
	return Tiers{Identical: 0, High: 3, Probable: MaxDistance}
}

// Normalise clamps t into a monotonic, in-range set of boundaries, reporting
// whether anything had to change.
//
// A Probable boundary above MaxDistance would silently break the pigeonhole
// guarantee (see the Chunks comment) — a match beyond distance 7 can miss
// every chunk column and never reach the verifier at all, which reintroduces
// the false-negative side of a problem this design exists to close. Values
// out of order (e.g. High > Probable, from a hand-edited config) are
// reconciled by widening the narrower bound rather than rejecting the
// configuration outright: a misconfigured floor is still a working, if
// looser, floor.
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
// REPOST_CONFIDENCE_UNSPECIFIED when distance exceeds t.Probable — which
// means no match, since anything past the pigeonhole ceiling is never even
// evaluated. Callers should Normalise t once and cache the result, rather
// than passing a raw, unvalidated Tiers through here.
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
