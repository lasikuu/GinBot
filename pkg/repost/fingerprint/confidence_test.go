package fingerprint

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"math/rand/v2"
	"testing"

	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
	"github.com/lasikuu/GinBot/pkg/repost"
)

// Crops are out of scope: pHash, like every global perceptual hash, does not
// survive them by design (ADR-0005).

// distinctImage gives each variant a different gradient axis, palette and
// shape layout, so pHash's DCT sees genuinely different low-frequency
// structure rather than one image under a different noise seed.
func distinctImage(width, height, variant int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, width, height))

	// Per-pixel noise in every channel keeps the ITU-R luma histogram wide
	// whichever channel weights a variant's gradient picks, so no variant
	// falls under the entropy floor.
	rng := rand.New(rand.NewPCG(uint64(variant)*7919+1, uint64(variant)*104729+7))

	for y := range height {
		for x := range width {
			var base uint8
			switch variant % 5 {
			case 0: // diagonal gradient
				base = uint8((x*255)/width+(y*255)/height) / 2
			case 1: // horizontal gradient
				base = uint8((x * 255) / width)
			case 2: // vertical gradient, checkerboard tint
				b := uint8((y * 255) / height)
				if (x/16+y/16)%2 == 0 {
					b = 255 - b
				}
				base = b
			case 3: // radial-ish gradient from a corner
				dx, dy := x, height-y
				base = uint8(((dx + dy) * 255) / (width + height))
			default: // inverted diagonal
				base = uint8(255 - ((x*255)/width+(y*255)/height)/2)
			}

			noise := uint8(rng.IntN(60))
			r := base + noise
			g := 255 - base
			b := base/2 + noise/2
			img.Set(x, y, color.RGBA{R: r, G: g, B: b, A: 255})
		}
	}

	blockX, blockY := (width/6)*(1+variant%3), (height/6)*(1+(variant+1)%3)
	blockW, blockH := width/4, height/4
	blockColor := color.RGBA{R: uint8(40 * variant % 255), G: uint8(80 * variant % 255), B: uint8(160 * variant % 255), A: 255}
	for y := blockY; y < blockY+blockH && y < height; y++ {
		for x := blockX; x < blockX+blockW && x < width; x++ {
			img.Set(x, y, blockColor)
		}
	}

	return img
}

func encodeJPEG(t *testing.T, img image.Image, quality int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality}); err != nil {
		t.Fatalf("encode fixture jpeg: %v", err)
	}
	return buf.Bytes()
}

// resizeBox downsamples by area-average. Not nearest-neighbour: that aliases
// far harder than any real resizer, which low-pass filters before subsampling.
func resizeBox(img *image.RGBA, newWidth, newHeight int) *image.RGBA {
	bounds := img.Bounds()
	srcW, srcH := bounds.Dx(), bounds.Dy()
	out := image.NewRGBA(image.Rect(0, 0, newWidth, newHeight))

	for y := range newHeight {
		y0 := y * srcH / newHeight
		y1 := (y + 1) * srcH / newHeight
		if y1 <= y0 {
			y1 = y0 + 1
		}
		for x := range newWidth {
			x0 := x * srcW / newWidth
			x1 := (x + 1) * srcW / newWidth
			if x1 <= x0 {
				x1 = x0 + 1
			}

			var rSum, gSum, bSum, count int
			for sy := y0; sy < y1; sy++ {
				for sx := x0; sx < x1; sx++ {
					r, g, b, _ := img.At(bounds.Min.X+sx, bounds.Min.Y+sy).RGBA()
					rSum += int(r >> 8)
					gSum += int(g >> 8)
					bSum += int(b >> 8)
					count++
				}
			}
			out.Set(x, y, color.RGBA{
				R: uint8(rSum / count), G: uint8(gSum / count), B: uint8(bSum / count), A: 255,
			})
		}
	}

	return out
}

// watermark burns a solid block into one corner: the heavier-edit case.
func watermark(img *image.RGBA) *image.RGBA {
	bounds := img.Bounds()
	out := image.NewRGBA(bounds)
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			out.Set(x, y, img.At(x, y))
		}
	}

	w, h := bounds.Dx(), bounds.Dy()
	markW, markH := w/5, h/8
	mark := color.RGBA{R: 255, G: 255, B: 255, A: 255}
	for y := h - markH; y < h; y++ {
		for x := w - markW; x < w; x++ {
			out.Set(x, y, mark)
		}
	}

	return out
}

func hashOrFatal(t *testing.T, hasher *Hasher, content []byte, mimeType string) uint64 {
	t.Helper()
	got, err := hasher.PerceptualHash(context.Background(), content, mimeType)
	if err != nil {
		t.Fatalf("PerceptualHash: unexpected error: %v", err)
	}
	return got
}

func TestRecompressedImageGradesIdenticalOrHigh(t *testing.T) {
	guards := DefaultGuards()
	hasher := NewHasher(guards, "")
	tiers := repost.DefaultTiers()

	size := guards.MinWidth * 3
	original := distinctImage(size, size, 0)

	originalHash := hashOrFatal(t, hasher, encodePNG(t, original), "image/png")
	recompressedHash := hashOrFatal(t, hasher, encodeJPEG(t, original, 75), "image/jpeg")

	distance := repost.Distance(originalHash, recompressedHash)
	t.Logf("original vs recompressed (JPEG q75): distance = %d", distance)

	confidence := tiers.Grade(distance)
	if confidence != pb.RepostConfidence_REPOST_CONFIDENCE_IDENTICAL && confidence != pb.RepostConfidence_REPOST_CONFIDENCE_HIGH {
		t.Errorf("recompressed image graded %v (distance %d), want IDENTICAL or HIGH", confidence, distance)
	}
}

func TestRescaledImageGradesIdenticalOrHigh(t *testing.T) {
	guards := DefaultGuards()
	hasher := NewHasher(guards, "")
	tiers := repost.DefaultTiers()

	size := guards.MinWidth * 3
	original := distinctImage(size, size, 1)
	rescaled := resizeBox(original, guards.MinWidth+16, guards.MinHeight+16)

	originalHash := hashOrFatal(t, hasher, encodePNG(t, original), "image/png")
	rescaledHash := hashOrFatal(t, hasher, encodePNG(t, rescaled), "image/png")

	distance := repost.Distance(originalHash, rescaledHash)
	t.Logf("original vs rescaled (%dx%d -> %dx%d): distance = %d", size, size, guards.MinWidth+16, guards.MinHeight+16, distance)

	confidence := tiers.Grade(distance)
	if confidence != pb.RepostConfidence_REPOST_CONFIDENCE_IDENTICAL && confidence != pb.RepostConfidence_REPOST_CONFIDENCE_HIGH {
		t.Errorf("rescaled image graded %v (distance %d), want IDENTICAL or HIGH", confidence, distance)
	}
}

// TestWatermarkedImageIsStillCloserThanAnUnrelatedImage asserts only the
// relative property; the exact distance depends on the fixture's mark size.
func TestWatermarkedImageIsStillCloserThanAnUnrelatedImage(t *testing.T) {
	guards := DefaultGuards()
	hasher := NewHasher(guards, "")

	size := guards.MinWidth * 3
	original := distinctImage(size, size, 2)
	marked := watermark(original)
	unrelated := distinctImage(size, size, 3)

	originalHash := hashOrFatal(t, hasher, encodePNG(t, original), "image/png")
	markedHash := hashOrFatal(t, hasher, encodePNG(t, marked), "image/png")
	unrelatedHash := hashOrFatal(t, hasher, encodePNG(t, unrelated), "image/png")

	watermarkDistance := repost.Distance(originalHash, markedHash)
	unrelatedDistance := repost.Distance(originalHash, unrelatedHash)

	t.Logf("original vs watermarked: distance = %d", watermarkDistance)
	t.Logf("original vs unrelated:   distance = %d", unrelatedDistance)

	if watermarkDistance >= unrelatedDistance {
		t.Errorf("watermarked distance (%d) is not closer than an unrelated image's distance (%d)",
			watermarkDistance, unrelatedDistance)
	}
}

// TestUnrelatedImagesAreNotFlagged asserts every pairwise distance between
// distinct fixtures exceeds MaxDistance, and logs the margins for tuning.
func TestUnrelatedImagesAreNotFlagged(t *testing.T) {
	guards := DefaultGuards()
	hasher := NewHasher(guards, "")
	tiers := repost.DefaultTiers()

	size := guards.MinWidth * 3
	const fixtureCount = 5

	hashes := make([]uint64, fixtureCount)
	for i := range fixtureCount {
		img := distinctImage(size, size, i)
		hashes[i] = hashOrFatal(t, hasher, encodePNG(t, img), "image/png")
	}

	minDistance := -1
	for i := range fixtureCount {
		for j := i + 1; j < fixtureCount; j++ {
			distance := repost.Distance(hashes[i], hashes[j])
			t.Logf("unrelated pair (variant %d, variant %d): distance = %d (MaxDistance = %d)",
				i, j, distance, repost.MaxDistance)

			if minDistance == -1 || distance < minDistance {
				minDistance = distance
			}

			if distance <= repost.MaxDistance {
				t.Errorf("unrelated variants %d and %d landed at distance %d, within MaxDistance (%d) — would be FLAGGED as a repost",
					i, j, distance, repost.MaxDistance)
			}
			if confidence := tiers.Grade(distance); confidence != pb.RepostConfidence_REPOST_CONFIDENCE_UNSPECIFIED {
				t.Errorf("unrelated variants %d and %d graded %v, want UNSPECIFIED (no match)", i, j, confidence)
			}
		}
	}

	t.Logf("smallest measured distance among %d unrelated fixture pairs: %d (MaxDistance = %d)",
		fixtureCount, minDistance, repost.MaxDistance)
}
