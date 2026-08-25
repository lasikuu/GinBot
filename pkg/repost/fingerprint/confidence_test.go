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

// This file covers confidence tiering (docs/plans/wanha.md W9) and, most
// importantly, acceptance criterion 9: an unrelated image must NOT be flagged
// at all. The previous implementation's whole failure mode was false
// positives — roughly 8 in 10 by the author's own estimate — so this is the
// highest-value test in the phase.
//
// Every fixture is generated in-process (gradients, shapes, JPEG re-encoding,
// a manual nearest-neighbour resize); nothing binary is committed.
//
// Note on scope, stated honestly rather than tested around: pHash — like
// every global perceptual hash — fails on crops by design (docs/plans/wanha.md
// W11, ADR-0005's documented downside). No crop fixture is exercised here
// because a "must not match" assertion on a crop would be trivially true for
// the wrong reason, and a "must match" assertion would contradict the design.
// Tiled crop tolerance is explicitly out of scope for this phase.

// distinctImage renders visually distinct content per variant: a different
// gradient axis, colour scheme and shape layout each time, so that pHash's DCT
// sees genuinely different low-frequency structure rather than near-identical
// images that merely differ in a noise seed. That distinction matters here
// specifically: two images sharing the same gradient formula would be a weak,
// unrepresentative test of "unrelated content must not match".
func distinctImage(width, height, variant int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, width, height))

	// Seeded per variant, so the image is reproducible run to run but the
	// noise pattern differs between variants. The noise is not cosmetic: an
	// earlier version of this fixture picked R/G/B weights that happened to
	// cancel out almost exactly in ITU-R luma (0.299R + 0.587G + 0.114B), so
	// the "horizontal gradient" variant scored well under the entropy floor
	// and PerceptualHash rejected it with ErrLowEntropy — a fixture defect,
	// not a guard defect. Mixing genuine per-pixel noise into every channel
	// makes the luma histogram wide regardless of which channel weights a
	// given gradient formula happens to pick.
	rng := rand.New(rand.NewPCG(uint64(variant)*7919+1, uint64(variant)*104729+7))

	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
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

	// A couple of solid blocks per variant, placed differently so the DCT's
	// low-frequency structure genuinely differs between variants rather than
	// only the fine texture.
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

// encodeJPEG re-encodes img as a JPEG at the given quality, simulating the
// recompression a repost commonly undergoes (re-uploaded through a platform
// that transcodes, saved and re-shared, and so on).
func encodeJPEG(t *testing.T, img image.Image, quality int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality}); err != nil {
		t.Fatalf("encode fixture jpeg: %v", err)
	}
	return buf.Bytes()
}

// resizeBox produces a genuinely rescaled copy of img using box-filter
// (area-average) downsampling, simulating a re-upload at a different
// resolution.
//
// Not nearest-neighbour: nearest-neighbour resampling of a noisy image
// aliases badly — it literally throws most source pixels away — which
// manufactures a harsher, less realistic distortion than any real image
// editor or platform re-encoder produces when it shrinks an image (almost
// all of them average or otherwise low-pass filter before subsampling,
// precisely to avoid this). A box filter is the simplest resize that does
// the same, and confirms the tier assertion is testing recompression
// robustness rather than an artifact of a specific, unrealistic downscaler.
func resizeBox(img *image.RGBA, newWidth, newHeight int) *image.RGBA {
	bounds := img.Bounds()
	srcW, srcH := bounds.Dx(), bounds.Dy()
	out := image.NewRGBA(image.Rect(0, 0, newWidth, newHeight))

	for y := 0; y < newHeight; y++ {
		y0 := y * srcH / newHeight
		y1 := (y + 1) * srcH / newHeight
		if y1 <= y0 {
			y1 = y0 + 1
		}
		for x := 0; x < newWidth; x++ {
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

// watermark overlays a solid, semi-opaque block in one corner, simulating a
// heavier edit: a watermark, logo or caption bar burned into a re-share.
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

// hashOrFatal is a small helper so every fixture case reports a clear failure
// location if the guards unexpectedly reject a fixture that is supposed to
// pass them.
func hashOrFatal(t *testing.T, hasher *Hasher, content []byte, mimeType string) uint64 {
	t.Helper()
	got, err := hasher.PerceptualHash(context.Background(), content, mimeType)
	if err != nil {
		t.Fatalf("PerceptualHash: unexpected error: %v", err)
	}
	return got
}

// ── Acceptance criterion 8: recompression and rescaling grade IDENTICAL/HIGH ─

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

// TestWatermarkedImageIsStillCloserThanAnUnrelatedImage covers the
// "heavier edit" case in the tiering regression net. It intentionally does not
// pin an exact tier for the watermark, because the precise distance a given
// watermark produces depends on its size and placement, which is a test
// fixture detail, not a specified contract. What IS specified, and asserted
// here, is the relative property the whole tiering scheme depends on: a
// partially-edited repost must read as more similar to its original than a
// wholly unrelated image does.
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

// ── Acceptance criterion 9: an unrelated image is NOT flagged ────────────────
//
// This is the single most important test in the phase. The old implementation
// failed here roughly 8 times in 10.

// TestUnrelatedImagesAreNotFlagged builds several visually distinct, high
// entropy, well-above-minimum-size images and asserts every pairwise distance
// exceeds MaxDistance — the exact threshold the pigeonhole index and the
// confidence tiers are built around. Every measured distance is logged so the
// margin against the threshold is on record for the next time these
// thresholds are tuned.
func TestUnrelatedImagesAreNotFlagged(t *testing.T) {
	guards := DefaultGuards()
	hasher := NewHasher(guards, "")
	tiers := repost.DefaultTiers()

	size := guards.MinWidth * 3
	const fixtureCount = 5

	hashes := make([]uint64, fixtureCount)
	for i := 0; i < fixtureCount; i++ {
		img := distinctImage(size, size, i)
		hashes[i] = hashOrFatal(t, hasher, encodePNG(t, img), "image/png")
	}

	minDistance := -1
	for i := 0; i < fixtureCount; i++ {
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
