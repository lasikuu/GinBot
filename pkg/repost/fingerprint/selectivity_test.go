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

// Selectivity, not correctness: how many rows a chunk-column lookup actually
// returns. The index design assumes a uniform 8-bit key, i.e. N/256 rows.
//
// The selectivity table, measured over a synthetic corpus and so an optimistic
// bound on real traffic — 292 images, 271 distinct hashes, seed 20260823. A
// perfectly uniform chunk at n=292 reaches only ~174 distinct values (birthday
// argument), with a top-bin share near 0.4%:
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
// Every chunk is 14-25x worse than uniform, and the worst by top share is c6,
// not the DC-carrying c0, so low-frequency DCT correlation is spread across
// the whole hash. The pigeonhole guarantee is unaffected; the "sub-millisecond
// so retention-forever is free" reasoning built on N/256 is not.

// selectivitySeed is fixed so the reported table is reproducible.
const selectivitySeed = 20260823

// selectivityImageSize is the generated image edge length, comfortably above
// the 64x64 PerceptionHash resizes to internally.
const selectivityImageSize = 160

// gradientImage draws a linear luminance ramp at the given angle, repeating
// cycles times across the image.
func gradientImage(size int, angle, cycles float64) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	dx, dy := math.Cos(angle), math.Sin(angle)
	span := float64(size) * (math.Abs(dx) + math.Abs(dy))

	for y := range size {
		for x := range size {
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
	for y := range size {
		for x := range size {
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

// blockImage fills a grid of cells with pseudo-random luminance: the synthetic
// analogue of a screenshot, large flat regions with hard edges.
func blockImage(size, cells int, rng *rand.Rand) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	values := make([]uint8, cells*cells)
	for i := range values {
		values[i] = uint8(rng.IntN(256))
	}

	cellSize := size / cells
	for y := range size {
		for x := range size {
			cx := min(x/cellSize, cells-1)
			cy := min(y/cellSize, cells-1)
			value := values[cy*cells+cx]
			img.Set(x, y, color.RGBA{R: value, G: value, B: value, A: 255})
		}
	}
	return img
}

// noiseImage fills every pixel independently, giving a flat DCT.
func noiseImage(size int, rng *rand.Rand) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	for y := range size {
		for x := range size {
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
	for y := range size {
		for x := range size {
			dx := float64(x)/float64(size) - originX
			dy := float64(y)/float64(size) - originY
			distance := math.Hypot(dx, dy)
			value := uint8(127.5 * (1 + math.Sin(2*math.Pi*cycles*distance)))
			img.Set(x, y, color.RGBA{R: value, G: value, B: value, A: 255})
		}
	}
	return img
}

// shifted rolls the image by (dx, dy) with wraparound, perturbing the phase of
// every DCT coefficient.
func shifted(src image.Image, dx, dy int) image.Image {
	bounds := src.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	out := image.NewRGBA(image.Rect(0, 0, width, height))

	for y := range height {
		for x := range width {
			sx := ((x+dx)%width + width) % width
			sy := ((y+dy)%height + height) % height
			out.Set(x, y, src.At(bounds.Min.X+sx, bounds.Min.Y+sy))
		}
	}
	return out
}

// cropped insets a sub-rectangle and rescales it back to full size.
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

// rescaled resamples to a square by nearest neighbour, deliberately keeping
// the high-frequency content a smooth filter would remove.
func rescaled(src image.Image, size int) image.Image {
	bounds := src.Bounds()
	out := image.NewRGBA(image.Rect(0, 0, size, size))

	for y := range size {
		for x := range size {
			sx := bounds.Min.X + x*bounds.Dx()/size
			sy := bounds.Min.Y + y*bounds.Dy()/size
			out.Set(x, y, src.At(sx, sy))
		}
	}
	return out
}

func buildSelectivityCorpus() []image.Image {
	rng := rand.New(rand.NewPCG(selectivitySeed, selectivitySeed))
	const size = selectivityImageSize

	var base []image.Image

	// Gradients: 16 angles x 4 spatial frequencies.
	for angleStep := range 16 {
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
		for range 12 {
			base = append(base, blockImage(size, cells, rng))
		}
	}

	// Flat-spectrum noise.
	for range 24 {
		base = append(base, noiseImage(size, rng))
	}

	// Radial rings from several origins.
	for _, origin := range [][2]float64{{0.5, 0.5}, {0.2, 0.7}, {0.8, 0.3}, {0.1, 0.1}} {
		for _, cycles := range []float64{2, 5, 9} {
			base = append(base, radialImage(size, origin[0], origin[1], cycles))
		}
	}

	// One derived variant per base image, rotating through the three edits.
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

// chunkStats is one chunk column's observed distribution.
type chunkStats struct {
	distinct  int
	topValue  int16
	topCount  int
	topShare  float64
	totalSeen int
}

// goldenChunkDistinct pins the distinct-value counts in the table above, so a
// goimagehash, decode-path or generator change forces a re-measurement.
var goldenChunkDistinct = [repost.ChunkCount]int{91, 132, 133, 141, 133, 140, 146, 149}

// goldenDistinctHashes is the number of distinct hashes the corpus yields.
const goldenDistinctHashes = 271

// TestPigeonholeChunkSelectivityIsMeasuredAndReported computes, prints and
// pins the per-chunk distribution. Needs -v to read the table.
func TestPigeonholeChunkSelectivityIsMeasuredAndReported(t *testing.T) {
	corpus := buildSelectivityCorpus()

	if len(corpus) < 200 {
		t.Fatalf("corpus is %d images, want at least 200 for the distribution to mean anything", len(corpus))
	}

	// Guards opened up on purpose: they are ingest policy, and applying them
	// here would bias the measurement toward the images that spread best.
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

	// Guards against a degenerate corpus, under which every assertion below
	// would pass vacuously.
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

	// Calibration: at this sample size even a uniform chunk cannot fill all
	// 256 bins, so comparing against 256 would make every chunk look bad.
	expectedDistinct := 256 * (1 - math.Pow(255.0/256.0, float64(len(corpus))))
	t.Logf("baseline for a perfectly uniform 8-bit chunk at n=%d: about %.0f distinct values, "+
		"and a most-common-value share around %.1f%% (the 1/256 mean, inflated by small-sample noise)",
		len(corpus), expectedDistinct, 100.0/256.0)

	t.Log("  chunk  distinct/256  most common value  share held by it  implied candidate set vs the N/256 design")
	for chunk, s := range stats {
		t.Log(formatChunkStats(chunk, s))
	}

	// Universal invariants: true of any corpus.
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

	// Golden values pin the recorded table, not the index behaviour.
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

const staleMeasurementHint = "the hashing path or the corpus changed, so the selectivity table in this file's " +
	"doc comment is stale: re-run with -v, replace the table and the goldens together, and re-check the " +
	"conclusions drawn from it"

// formatChunkStats renders one table row; the last column is how many times
// larger a candidate set is than the N/256 the uniform assumption predicts.
func formatChunkStats(chunk int, s chunkStats) string {
	inflation := s.topShare * 256

	return fmt.Sprintf("  c%-6d %-13d %-19d %-17s %.1fx",
		chunk, s.distinct, s.topValue,
		fmt.Sprintf("%.1f%%", s.topShare*100),
		inflation)
}
