package fingerprint

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"math"
	"math/rand/v2"
	"testing"

	"github.com/lasikuu/GinBot/pkg/repost"
)

// This file measures the SELECTIVITY of the pigeonhole chunking, which is a
// different question from its correctness.
//
// The correctness guarantee — two hashes within Hamming distance 7 share at
// least one of the eight 8-bit chunks exactly — is proved by property test in
// pkg/repost and is not in question here. What has never been measured is how
// much work that guarantee costs at lookup time. The index design assumes each
// chunk column behaves roughly like a uniform 8-bit key, so a candidate set is
// about N/256 rows. That assumption is not obviously true:
// goimagehash.PerceptionHash thresholds each DCT coefficient against the
// MEDIAN of the coefficients, so the DC term — which is the largest by a wide
// margin and lands in chunk 0 — is above the median for very nearly every
// real image, and the neighbouring low-frequency coefficients are strongly
// correlated with each other. Chunk 0 therefore takes far fewer than 256
// distinct values in practice, and every lookup that ORs across the eight
// columns pays for it.
//
// THIS IS A REPRODUCIBLE LOWER-BOUND PROXY, NOT THE PRODUCTION NUMBER.
//
// The corpus is procedurally generated from a fixed seed so the measurement is
// deterministic and reviewable in CI, but synthetic gradients, checkerboards
// and noise are not photographs, screenshots and memes. Synthetic content is
// if anything MORE varied in its low-frequency structure than real traffic, so
// the distinct-value counts printed below are an optimistic bound: real
// content will do no better. The definitive number needs a sample of actual
// indexed content from a live instance, and nothing here should be quoted as
// though it were that.
//
// THE RESULT, so that it lives in the repository rather than only in a CI log:
//
//	292 images, 271 distinct hashes, seed 20260823.
//	A perfectly uniform 8-bit chunk at n=292 would reach ~174 distinct values
//	(not 256 — the birthday argument caps it) with a top-bin share near 0.4%.
//
//	  chunk  distinct  top value  top share  candidate set vs the N/256 design
//	  c0     91        128        8.6%       21.9x
//	  c1     132       42         5.8%       14.9x
//	  c2     133       0          8.2%       21.0x
//	  c3     141       0          5.5%       14.0x
//	  c4     133       0          8.6%       21.9x
//	  c5     140       0          6.2%       15.8x
//	  c6     146       0          9.9%       25.4x
//	  c7     149       0          6.5%       16.7x
//
// Two conclusions, and the second is not the one the investigation expected.
//
// First, the DC-bit story above is visible but is NOT the main effect. c0 is
// the least spread column by distinct count — 91 against c7's 149, about half
// the achievable spread — and its modal value is 128, i.e. the top bit set and
// the next seven clear, exactly what a DC coefficient that is always above the
// median produces. So the mechanism is real.
//
// Second, and more important: EVERY chunk is 14-25x worse than the uniform
// assumption, and by the measure that actually governs lookup cost — the share
// held by the most common value, which is the worst-case candidate set — c6 at
// 25.4x is the worst column, not c0. Fixing or dropping c0 would therefore not
// recover the design's assumed cost. Low-frequency DCT correlation is spread
// across the whole hash, not concentrated in one byte. The pigeonhole guarantee
// is unaffected and the index still beats a sequential scan; what is wrong is
// the "sub-millisecond, so retention-forever is free" reasoning built on N/256.
//
// The assertions below are of two kinds. The universal ones — values in range,
// one observation per image per chunk, no chunk above 256 — hold for any
// corpus. The golden ones pin the table above against this fixed seed, so that
// a goimagehash upgrade, a change to the decode path or an edit to the
// generators fails here and forces the measurement to be redone rather than
// leaving a stale table quoted as current. There is deliberately still no
// assertion that one chunk is more selective than another: that ordering is an
// artefact of the generators and pinning it would be a flake.
//
// It lives in pkg/repost/fingerprint rather than pkg/repost because it needs
// both the hashing path and repost.Chunks, and fingerprint may import repost
// while the reverse would be an import cycle.

// selectivitySeed is fixed so the reported table is reproducible run to run.
// A number that moves on its own is not a measurement.
const selectivitySeed = 20260823

// selectivityImageSize is the generated image edge length. PerceptionHash
// resizes to 64x64 internally, so anything comfortably above that is
// equivalent for the hash while keeping PNG encoding cheap.
const selectivityImageSize = 160

// ── Corpus generation ────────────────────────────────────────────────────────

// gradientImage draws a linear luminance ramp at the given angle, with the
// ramp repeating `cycles` times across the image. Varying both gives a family
// of images whose low-frequency DCT structure genuinely differs, which is the
// part of the spectrum the pHash bits are drawn from.
func gradientImage(size int, angle, cycles float64) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	dx, dy := math.Cos(angle), math.Sin(angle)
	span := float64(size) * (math.Abs(dx) + math.Abs(dy))

	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			projection := (float64(x)*dx + float64(y)*dy) / span
			value := uint8(127.5 * (1 + math.Sin(2*math.Pi*cycles*projection)))
			img.Set(x, y, color.RGBA{R: value, G: value, B: value, A: 255})
		}
	}
	return img
}

// checkerImage draws a checkerboard of the given cell size, offset by phase.
func checkerImage(size, cell, phase int) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			on := (((x+phase)/cell)+((y+phase)/cell))%2 == 0
			value := uint8(24)
			if on {
				value = 232
			}
			img.Set(x, y, color.RGBA{R: value, G: value, B: value, A: 255})
		}
	}
	return img
}

// blockImage divides the image into a grid and fills each cell with a
// pseudo-random luminance. This is the closest synthetic analogue to a
// screenshot or a comic panel: large flat regions with hard edges.
func blockImage(size, cells int, rng *rand.Rand) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	values := make([]uint8, cells*cells)
	for i := range values {
		values[i] = uint8(rng.IntN(256))
	}

	cellSize := size / cells
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			cx := min(x/cellSize, cells-1)
			cy := min(y/cellSize, cells-1)
			value := values[cy*cells+cx]
			img.Set(x, y, color.RGBA{R: value, G: value, B: value, A: 255})
		}
	}
	return img
}

// noiseImage fills every pixel independently. Its DCT is flat, which is the
// opposite extreme from the gradients and the widest possible spread of
// high-frequency bits.
func noiseImage(size int, rng *rand.Rand) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			img.Set(x, y, color.RGBA{
				R: uint8(rng.IntN(256)),
				G: uint8(rng.IntN(256)),
				B: uint8(rng.IntN(256)),
				A: 255,
			})
		}
	}
	return img
}

// radialImage draws concentric rings around an off-centre origin.
func radialImage(size int, originX, originY, cycles float64) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			dx := float64(x)/float64(size) - originX
			dy := float64(y)/float64(size) - originY
			distance := math.Hypot(dx, dy)
			value := uint8(127.5 * (1 + math.Sin(2*math.Pi*cycles*distance)))
			img.Set(x, y, color.RGBA{R: value, G: value, B: value, A: 255})
		}
	}
	return img
}

// shifted rolls the image by (dx, dy), wrapping around. A shift is the
// cheapest real-world edit — a crop-and-repost that happens to preserve
// content — and it perturbs the phase of every DCT coefficient.
func shifted(src image.Image, dx, dy int) image.Image {
	bounds := src.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	out := image.NewRGBA(image.Rect(0, 0, width, height))

	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			sx := ((x+dx)%width + width) % width
			sy := ((y+dy)%height + height) % height
			out.Set(x, y, src.At(bounds.Min.X+sx, bounds.Min.Y+sy))
		}
	}
	return out
}

// cropped takes an inset sub-rectangle and rescales it back to full size,
// which is what a "screenshot of a screenshot" does to the spectrum.
func cropped(src image.Image, inset int) image.Image {
	bounds := src.Bounds().Inset(inset)
	if bounds.Dx() < 8 || bounds.Dy() < 8 {
		return src
	}

	sub := image.NewRGBA(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
	for y := 0; y < bounds.Dy(); y++ {
		for x := 0; x < bounds.Dx(); x++ {
			sub.Set(x, y, src.At(bounds.Min.X+x, bounds.Min.Y+y))
		}
	}
	return rescaled(sub, selectivityImageSize)
}

// rescaled resamples to a square of the given edge length by nearest
// neighbour. Nearest neighbour rather than a smooth filter on purpose: it
// keeps the high-frequency content that a smooth filter would remove, so the
// variant is a genuinely different image rather than a blurred copy.
func rescaled(src image.Image, size int) image.Image {
	bounds := src.Bounds()
	out := image.NewRGBA(image.Rect(0, 0, size, size))

	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			sx := bounds.Min.X + x*bounds.Dx()/size
			sy := bounds.Min.Y + y*bounds.Dy()/size
			out.Set(x, y, src.At(sx, sy))
		}
	}
	return out
}

// buildSelectivityCorpus assembles a few hundred images with genuine
// structural variety, then derives shifted, cropped and rescaled variants of
// every one of them.
func buildSelectivityCorpus() []image.Image {
	rng := rand.New(rand.NewPCG(selectivitySeed, selectivitySeed))
	const size = selectivityImageSize

	var base []image.Image

	// Gradients: 16 angles x 4 spatial frequencies.
	for angleStep := 0; angleStep < 16; angleStep++ {
		angle := float64(angleStep) * math.Pi / 16
		for _, cycles := range []float64{0.5, 1, 2.5, 6} {
			base = append(base, gradientImage(size, angle, cycles))
		}
	}

	// Checkerboards across five scales, both phases.
	for _, cell := range []int{2, 5, 10, 20, 40} {
		for _, phase := range []int{0, 3} {
			base = append(base, checkerImage(size, cell, phase))
		}
	}

	// Blocky, screenshot-like content at three grid resolutions.
	for _, cells := range []int{4, 8, 16} {
		for i := 0; i < 12; i++ {
			base = append(base, blockImage(size, cells, rng))
		}
	}

	// Flat-spectrum noise.
	for i := 0; i < 24; i++ {
		base = append(base, noiseImage(size, rng))
	}

	// Radial rings from several origins.
	for _, origin := range [][2]float64{{0.5, 0.5}, {0.2, 0.7}, {0.8, 0.3}, {0.1, 0.1}} {
		for _, cycles := range []float64{2, 5, 9} {
			base = append(base, radialImage(size, origin[0], origin[1], cycles))
		}
	}

	// One derived variant per base image, rotating through the three edits so
	// each kind is represented without tripling the corpus.
	corpus := make([]image.Image, 0, len(base)*2)
	corpus = append(corpus, base...)
	for i, img := range base {
		switch i % 3 {
		case 0:
			corpus = append(corpus, shifted(img, 11, 7))
		case 1:
			corpus = append(corpus, cropped(img, size/8))
		default:
			corpus = append(corpus, rescaled(img, size*3/4))
		}
	}

	return corpus
}

// ── The measurement ──────────────────────────────────────────────────────────
//
// The corpus is rendered to PNG through encodePNG (declared in
// fingerprint_test.go, not redeclared here) and hashed through the exported
// PerceptualHash, so the decode bound and the guards are exercised exactly as
// they are during ingest rather than via a shortcut into hashImage.

// chunkStats is one chunk column's observed distribution.
type chunkStats struct {
	distinct  int
	topValue  int16
	topCount  int
	topShare  float64
	totalSeen int
}

// goldenChunkDistinct is the distinct-value count per chunk observed for the
// selectivitySeed corpus, and is the table recorded at the top of this file.
//
// It is pinned, not merely printed. The whole point of the measurement is that
// somebody later reads those numbers and reasons about index cost from them, so
// the failure that matters is not "the numbers are bad" — they are, knowingly —
// but "the numbers no longer describe what the code does". A goimagehash bump,
// a change to the resize or decode path, or an edit to the generators all move
// these, and all invalidate the conclusions written above.
var goldenChunkDistinct = [repost.ChunkCount]int{91, 132, 133, 141, 133, 140, 146, 149}

// goldenDistinctHashes is the number of distinct hashes the corpus yields.
const goldenDistinctHashes = 271

// TestPigeonholeChunkSelectivityIsMeasuredAndReported computes the per-chunk
// distribution described at the top of this file, prints it, and pins it.
//
// Run it on its own to read the table, which CI also does in its own step
// because `go test` discards t.Log output from a passing test unless -v is set:
//
//	go test -v -run TestPigeonholeChunkSelectivity ./pkg/repost/fingerprint/
func TestPigeonholeChunkSelectivityIsMeasuredAndReported(t *testing.T) {
	corpus := buildSelectivityCorpus()

	// A corpus that silently shrank would make every number below meaningless
	// while still passing, so its size is a precondition rather than an
	// observation.
	if len(corpus) < 200 {
		t.Fatalf("corpus is %d images, want at least 200 for the distribution to mean anything", len(corpus))
	}

	// Guards are deliberately opened up rather than left at DefaultGuards.
	// MinEntropy would reject the flat checkerboards and MinWidth/MinHeight
	// the rescaled variants — both are INGEST POLICY about what is worth
	// indexing, and excluding them here would bias the measurement toward
	// exactly the busy, high-entropy images that spread best. The hashing path
	// itself is unchanged.
	hasher := NewHasher(Guards{MinWidth: 1, MinHeight: 1, MinEntropy: 0}, "")
	ctx := context.Background()

	counts := make([]map[int16]int, repost.ChunkCount)
	for i := range counts {
		counts[i] = make(map[int16]int)
	}
	distinctHashes := make(map[uint64]struct{}, len(corpus))

	for i, img := range corpus {
		hash, err := hasher.PerceptualHash(ctx, encodePNG(t, img), "image/png")
		if err != nil {
			t.Fatalf("PerceptualHash of corpus image %d: %v", i, err)
		}

		distinctHashes[hash] = struct{}{}

		for chunk, value := range repost.Chunks(hash) {
			counts[chunk][value]++
		}
	}

	// Not vacuous: if every generator had produced the same flat image — a
	// plausible bug in this file, not in the code under test — every chunk
	// would report exactly one distinct value and the assertions below would
	// still pass.
	if len(distinctHashes) < 16 {
		t.Fatalf("the corpus produced only %d distinct perceptual hashes, so it is degenerate and the per-chunk numbers below describe the fixtures rather than the chunking",
			len(distinctHashes))
	}

	stats := make([]chunkStats, repost.ChunkCount)
	for chunk, histogram := range counts {
		s := chunkStats{distinct: len(histogram)}
		for value, seen := range histogram {
			s.totalSeen += seen
			if seen > s.topCount || (seen == s.topCount && value < s.topValue) {
				s.topValue, s.topCount = value, seen
			}
		}
		if s.totalSeen > 0 {
			s.topShare = float64(s.topCount) / float64(s.totalSeen)
		}
		stats[chunk] = s
	}

	t.Logf("pHash chunk selectivity over %d procedurally generated images (%d distinct hashes), seed %d",
		len(corpus), len(distinctHashes), selectivitySeed)
	t.Log("chunk 0 is the most significant byte, which carries the DC term and the lowest-frequency coefficients")

	// Calibration, without which the table below is unreadable. At this sample
	// size a PERFECTLY uniform chunk cannot fill all 256 bins — by the
	// birthday argument it reaches only about the figure below — and its most
	// common bin still holds several samples purely by chance. Comparing a
	// chunk against 256 rather than against these baselines would make every
	// chunk look concentrated.
	expectedDistinct := 256 * (1 - math.Pow(255.0/256.0, float64(len(corpus))))
	t.Logf("baseline for a perfectly uniform 8-bit chunk at n=%d: about %.0f distinct values, "+
		"and a most-common-value share around %.1f%% (the 1/256 mean, inflated by small-sample noise)",
		len(corpus), expectedDistinct, 100.0/256.0)

	t.Log("  chunk  distinct/256  most common value  share held by it  implied candidate set vs the N/256 design")
	for chunk, s := range stats {
		t.Log(formatChunkStats(chunk, s))
	}

	// Universal invariants: true of any corpus, so they cannot become flaky as
	// the generators change.
	for chunk, s := range stats {
		if s.distinct < 1 {
			t.Errorf("chunk %d has %d distinct values, want at least 1", chunk, s.distinct)
		}
		if s.distinct > 256 {
			t.Errorf("chunk %d has %d distinct values, want at most 256; an 8-bit chunk cannot exceed that",
				chunk, s.distinct)
		}
		if s.totalSeen != len(corpus) {
			t.Errorf("chunk %d was observed %d times, want once per image (%d)", chunk, s.totalSeen, len(corpus))
		}
		for value := range counts[chunk] {
			if value < 0 || value > 255 {
				t.Errorf("chunk %d produced the value %d, want 0..255; repost_fingerprint.c%d is a smallint column and the pigeonhole lookup binds it positionally",
					chunk, value, chunk)
			}
		}
	}

	// Golden values: these pin the recorded table rather than the code under
	// test. Failing here does not mean the index broke — it means the
	// measurement at the top of this file is now describing something that no
	// longer exists, and the conclusions drawn from it have to be redone.
	if len(distinctHashes) != goldenDistinctHashes {
		t.Errorf("corpus yields %d distinct hashes, want %d — %s",
			len(distinctHashes), goldenDistinctHashes, staleMeasurementHint)
	}
	for chunk, s := range stats {
		if s.distinct != goldenChunkDistinct[chunk] {
			t.Errorf("chunk %d has %d distinct values, want the recorded %d — %s",
				chunk, s.distinct, goldenChunkDistinct[chunk], staleMeasurementHint)
		}
	}
}

// staleMeasurementHint is appended to every golden failure, because the useful
// response to one is to re-measure and rewrite the table, not to edit the
// constant until it passes.
const staleMeasurementHint = "the hashing path or the corpus changed, so the selectivity table in this file's " +
	"doc comment is stale: re-run with -v, replace the table and the goldens together, and re-check the " +
	"conclusions drawn from it"

// formatChunkStats renders one row of the reported table. The last column
// converts the observed concentration into the number the index design cares
// about: how many times larger a candidate set is than the N/256 the uniform
// assumption predicts.
func formatChunkStats(chunk int, s chunkStats) string {
	inflation := s.topShare * 256

	return fmt.Sprintf("  c%-6d %-13d %-19d %-17s %.1fx",
		chunk, s.distinct, s.topValue,
		fmt.Sprintf("%.1f%%", s.topShare*100),
		inflation)
}
