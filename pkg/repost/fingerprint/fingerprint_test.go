package fingerprint

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/png"
	"math/rand/v2"
	"testing"

	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
	"github.com/lasikuu/GinBot/pkg/storage"
)

// ── Assumed symbols from pkg/repost/fingerprint (spec §3.4) ──────────────────
//
// Recorded here because these are the symbols the tests below depend on, so a
// change to any of them should be a deliberate decision rather than a surprise.
//
//	var ErrTooSmall = errors.New("image is below the minimum dimensions")
//	var ErrLowEntropy = errors.New("image entropy is below the floor")
//	var ErrNoDecoder = errors.New("no decoder is available for this content")
//
//	type Guards struct {
//		MinWidth   int
//		MinHeight  int
//		MinEntropy float64
//	}
//
//	func DefaultGuards() Guards
//	func ContentHash(content []byte) (sum []byte, hex string)
//	func Entropy(img image.Image) float64
//	func LookupFFmpeg() string
//	func Kind(mimeType string) pb.RepostKind
//
//	type Hasher struct { /* unexported */ }
//	func NewHasher(guards Guards, ffmpegPath string) *Hasher
//	func (h *Hasher) PerceptualHash(ctx context.Context, content []byte, mimeType string) (uint64, error)

// ── fixture builders: no binary fixtures are committed, everything below is
// generated programmatically ──────────────────────────────────────────────────

// encodePNG renders img as a PNG byte slice.
func encodePNG(t *testing.T, img image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode fixture png: %v", err)
	}
	return buf.Bytes()
}

// encodeGIF renders img as a genuine, decodable single-frame GIF byte slice
// (stdlib image/gif quantises img to a palette itself), so the GIF-specific
// tests exercise a real decode rather than mislabelled bytes of another
// format.
func encodeGIF(t *testing.T, img image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := gif.Encode(&buf, img, nil); err != nil {
		t.Fatalf("encode fixture gif: %v", err)
	}
	return buf.Bytes()
}

// solidImage is a single flat colour: the textbook near-zero-entropy case that
// both dHash and pHash historically collapsed on (docs/plans/wanha.md, W4).
func solidImage(width, height int, c color.Gray) *image.Gray {
	img := image.NewGray(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.SetGray(x, y, c)
		}
	}
	return img
}

// nearBlankImage is almost entirely one colour, with a small fraction of
// pixels perturbed. This is meant to still land well under the default 3.0-bit
// entropy floor while not being a literal single-colour fill, covering guards
// against near-blank images specifically (not just perfectly solid ones).
func nearBlankImage(width, height int) *image.Gray {
	img := image.NewGray(image.Rect(0, 0, width, height))
	rng := rand.New(rand.NewPCG(1, 1))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			v := uint8(10)
			// Roughly 1% of pixels perturbed to a different fixed value.
			if rng.IntN(100) == 0 {
				v = 250
			}
			img.SetGray(x, y, color.Gray{Y: v})
		}
	}
	return img
}

// uniformNoiseImage fills every pixel with an independent uniformly-random
// grey value, which is the textbook near-8-bit-entropy case.
func uniformNoiseImage(width, height int, seed uint64) *image.Gray {
	img := image.NewGray(image.Rect(0, 0, width, height))
	rng := rand.New(rand.NewPCG(seed, seed+1))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.SetGray(x, y, color.Gray{Y: uint8(rng.IntN(256))})
		}
	}
	return img
}

// structuredImage is a gradient overlaid with noise plus a few geometric
// shapes: high entropy, but not pure noise, so a perceptual hash of it is
// meaningful (a pHash of pure random noise carries no structure to be robust
// about). This is the base fixture confidence-tier tests build on.
func structuredImage(width, height int, seed uint64) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	rng := rand.New(rand.NewPCG(seed, seed+7))

	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			// Diagonal gradient base.
			base := uint8((x*255)/max(width, 1)+(y*255)/max(height, 1)) / 2
			noise := uint8(rng.IntN(40))
			img.Set(x, y, color.RGBA{R: base + noise, G: base, B: 255 - base, A: 255})
		}
	}

	// A few solid geometric shapes give the DCT some strong, stable low
	// frequency structure to lock onto, which is what makes recompression and
	// rescaling land close in Hamming distance.
	fillRect := func(x0, y0, x1, y1 int, c color.RGBA) {
		for y := y0; y < y1 && y < height; y++ {
			for x := x0; x < x1 && x < width; x++ {
				img.Set(x, y, c)
			}
		}
	}
	fillRect(width/8, height/8, width/3, height/3, color.RGBA{R: 20, G: 200, B: 20, A: 255})
	fillRect(width/2, height/2, width-width/8, height-height/8, color.RGBA{R: 200, G: 20, B: 200, A: 255})

	return img
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// ── Entropy ───────────────────────────────────────────────────────────────────

// TestEntropyOfAUniformFillIsZero: a single-colour histogram has exactly one
// non-empty bin, so the Shannon entropy is exactly 0 bits.
func TestEntropyOfAUniformFillIsZero(t *testing.T) {
	img := solidImage(64, 64, color.Gray{Y: 128})

	got := Entropy(img)
	if got != 0 {
		t.Errorf("Entropy(solid fill) = %v, want 0", got)
	}
}

// TestEntropyOfUniformNoiseIsNearEight: independent uniform 8-bit samples
// spread across all 256 bins roughly equally, which is the maximum-entropy
// case for an 8-bit histogram (log2(256) = 8).
func TestEntropyOfUniformNoiseIsNearEight(t *testing.T) {
	img := uniformNoiseImage(256, 256, 42)

	got := Entropy(img)
	if got < 7.9 || got > 8.0 {
		t.Errorf("Entropy(uniform noise) = %v, want in [7.9, 8.0]", got)
	}
}

// TestEntropyIsMonotonicBetweenSolidAndNoisy: a near-blank image (mostly one
// colour) must read as lower entropy than a fully random one, so the guard's
// floor has something meaningful to sit between.
func TestEntropyIsMonotonicBetweenSolidAndNoisy(t *testing.T) {
	blank := Entropy(nearBlankImage(128, 128))
	noisy := Entropy(uniformNoiseImage(128, 128, 7))

	if !(blank < noisy) {
		t.Errorf("Entropy(near-blank) = %v, Entropy(noisy) = %v; want near-blank strictly lower", blank, noisy)
	}
}

// ── DefaultGuards ─────────────────────────────────────────────────────────────

func TestDefaultGuards(t *testing.T) {
	got := DefaultGuards()
	if got.MinWidth != 128 {
		t.Errorf("DefaultGuards().MinWidth = %d, want 128", got.MinWidth)
	}
	if got.MinHeight != 128 {
		t.Errorf("DefaultGuards().MinHeight = %d, want 128", got.MinHeight)
	}
	if got.MinEntropy != 3.0 {
		t.Errorf("DefaultGuards().MinEntropy = %v, want 3.0", got.MinEntropy)
	}
}

// ── ContentHash ───────────────────────────────────────────────────────────────

func TestContentHashMatchesSHA256(t *testing.T) {
	content := []byte("some arbitrary fixture bytes for hashing")

	wantSum := sha256.Sum256(content)
	wantHex := hex.EncodeToString(wantSum[:])

	gotSum, gotHex := ContentHash(content)

	if !bytes.Equal(gotSum, wantSum[:]) {
		t.Errorf("ContentHash sum = %x, want %x", gotSum, wantSum)
	}
	if gotHex != wantHex {
		t.Errorf("ContentHash hex = %q, want %q", gotHex, wantHex)
	}
}

func TestContentHashIsDeterministic(t *testing.T) {
	content := []byte("determinism check")

	sum1, hex1 := ContentHash(content)
	sum2, hex2 := ContentHash(content)

	if !bytes.Equal(sum1, sum2) || hex1 != hex2 {
		t.Error("ContentHash returned different results for identical input")
	}
}

// ── Kind ──────────────────────────────────────────────────────────────────────

// TestKindClassifiesEveryAllowedMIMEType covers every MIME type the fetcher
// allows (storage.AllowedMIMETypes), plus one it does not recognise at all.
//
// image/gif is deliberately expected to classify as REPOST_KIND_VIDEO, not
// REPOST_KIND_IMAGE: repost.proto documents REPOST_KIND_VIDEO as "Video or
// animated GIF, fingerprinted on its first frame" — GIF rides the video path
// because it may be animated, even though its MIME type is image/*.
func TestKindClassifiesEveryAllowedMIMEType(t *testing.T) {
	tests := map[string]pb.RepostKind{
		"image/png":  pb.RepostKind_REPOST_KIND_IMAGE,
		"image/jpeg": pb.RepostKind_REPOST_KIND_IMAGE,
		"image/webp": pb.RepostKind_REPOST_KIND_IMAGE,
		"image/gif":  pb.RepostKind_REPOST_KIND_VIDEO,
		"video/mp4":  pb.RepostKind_REPOST_KIND_VIDEO,
		"video/webm": pb.RepostKind_REPOST_KIND_VIDEO,
		"audio/mpeg": pb.RepostKind_REPOST_KIND_FILE,
		"audio/ogg":  pb.RepostKind_REPOST_KIND_FILE,
		"audio/wave": pb.RepostKind_REPOST_KIND_FILE,
		"application/unknown-type-nobody-registered": pb.RepostKind_REPOST_KIND_FILE,
	}

	// Every type storage actually allows through the fetcher must be covered
	// above, so a new allowed type cannot silently fall through unclassified.
	for _, allowed := range storage.AllowedMIMETypes() {
		if _, covered := tests[allowed]; !covered {
			t.Errorf("storage.AllowedMIMETypes() includes %q, which this test table does not cover", allowed)
		}
	}

	for mimeType, want := range tests {
		t.Run(mimeType, func(t *testing.T) {
			if got := Kind(mimeType); got != want {
				t.Errorf("Kind(%q) = %v, want %v", mimeType, got, want)
			}
		})
	}
}

// ── PerceptualHash guards ─────────────────────────────────────────────────────

// TestPerceptualHashRejectsBelowMinimumDimensions is AC10's guard: a tiny,
// high-entropy image must still be refused for perceptual matching on size
// alone. High entropy is used deliberately so this cannot be passing only
// because the fixture also happens to trip the entropy floor.
func TestPerceptualHashRejectsBelowMinimumDimensions(t *testing.T) {
	guards := DefaultGuards()
	hasher := NewHasher(guards, "")

	tiny := uniformNoiseImage(guards.MinWidth-1, guards.MinHeight-1, 3)
	content := encodePNG(t, tiny)

	_, err := hasher.PerceptualHash(context.Background(), content, "image/png")
	if !errors.Is(err, ErrTooSmall) {
		t.Errorf("PerceptualHash(tiny noisy image) err = %v, want ErrTooSmall", err)
	}
}

// TestPerceptualHashRejectsSolidColourImage is AC10's other guard, and the
// direct regression test for the old implementation's biggest blind spot
// (docs/plans/wanha.md W4): a flat-colour image at or above the minimum size
// must be excluded from perceptual matching by the entropy floor.
func TestPerceptualHashRejectsSolidColourImage(t *testing.T) {
	guards := DefaultGuards()
	hasher := NewHasher(guards, "")

	solid := solidImage(guards.MinWidth*2, guards.MinHeight*2, color.Gray{Y: 200})
	content := encodePNG(t, solid)

	_, err := hasher.PerceptualHash(context.Background(), content, "image/png")
	if !errors.Is(err, ErrLowEntropy) {
		t.Errorf("PerceptualHash(solid colour image) err = %v, want ErrLowEntropy", err)
	}
}

// TestPerceptualHashRejectsNearBlankImage: almost-but-not-quite solid is the
// case that actually mattered in practice — a screenshot with a large flat
// background and a little content is not a mathematically perfect single
// colour, so the guard has to be an entropy floor and not merely a
// single-colour special case.
func TestPerceptualHashRejectsNearBlankImage(t *testing.T) {
	guards := DefaultGuards()
	hasher := NewHasher(guards, "")

	blank := nearBlankImage(guards.MinWidth*2, guards.MinHeight*2)
	content := encodePNG(t, blank)

	_, err := hasher.PerceptualHash(context.Background(), content, "image/png")
	if !errors.Is(err, ErrLowEntropy) {
		t.Errorf("PerceptualHash(near-blank image) err = %v, want ErrLowEntropy", err)
	}
}

// TestPerceptualHashAcceptsAStructuredHighEntropyImage is the positive control
// for the two guard tests above: a large, richly structured image must clear
// both guards and produce a hash with no error.
func TestPerceptualHashAcceptsAStructuredHighEntropyImage(t *testing.T) {
	guards := DefaultGuards()
	hasher := NewHasher(guards, "")

	img := structuredImage(guards.MinWidth*2, guards.MinHeight*2, 99)
	content := encodePNG(t, img)

	_, err := hasher.PerceptualHash(context.Background(), content, "image/png")
	if err != nil {
		t.Errorf("PerceptualHash(structured high-entropy image) unexpected error: %v", err)
	}
}

// TestPerceptualHashIsDeterministicForIdenticalBytes: hashing the same content
// twice must produce the same hash, since the whole matching scheme depends on
// pHash being a pure function of the pixels.
func TestPerceptualHashIsDeterministicForIdenticalBytes(t *testing.T) {
	guards := DefaultGuards()
	hasher := NewHasher(guards, "")

	img := structuredImage(guards.MinWidth*2, guards.MinHeight*2, 123)
	content := encodePNG(t, img)

	first, err := hasher.PerceptualHash(context.Background(), content, "image/png")
	if err != nil {
		t.Fatalf("first PerceptualHash: %v", err)
	}
	second, err := hasher.PerceptualHash(context.Background(), content, "image/png")
	if err != nil {
		t.Fatalf("second PerceptualHash: %v", err)
	}

	if first != second {
		t.Errorf("PerceptualHash is not deterministic: %#x then %#x for identical bytes", first, second)
	}
}

// TestPerceptualHashHasNoDecoderForNonVisualContent: audio and generic file
// kinds are, by design (docs/plans/wanha.md W6), never perceptually hashed —
// only exact SHA-256 applies to them. Asking for a perceptual hash of such
// content must report ErrNoDecoder rather than guessing, hanging, or panicking.
func TestPerceptualHashHasNoDecoderForNonVisualContent(t *testing.T) {
	guards := DefaultGuards()
	hasher := NewHasher(guards, "")

	garbage := []byte("not an image or a video, just some file bytes")

	_, err := hasher.PerceptualHash(context.Background(), garbage, "audio/mpeg")
	if !errors.Is(err, ErrNoDecoder) {
		t.Errorf("PerceptualHash(audio content) err = %v, want ErrNoDecoder", err)
	}
}

// TestPerceptualHashDecodesARealGIFAndAppliesTheSameGuards covers the
// documented GIF path end to end with genuine GIF bytes (stdlib image/gif),
// not a mislabelled other format: a tiny GIF is still-guarded by size, and a
// large structured one decodes and hashes successfully, proving the GIF
// decode path is actually wired up rather than only reachable in theory.
func TestPerceptualHashDecodesARealGIFAndAppliesTheSameGuards(t *testing.T) {
	guards := DefaultGuards()
	hasher := NewHasher(guards, "")

	t.Run("below minimum dimensions is refused", func(t *testing.T) {
		// High-entropy content at a too-small size, so a failure here can only
		// be the size guard — an entropy-guard rejection would prove nothing
		// about the size check specifically.
		tiny := encodeGIF(t, structuredImage(guards.MinWidth-1, guards.MinHeight-1, 61))

		_, err := hasher.PerceptualHash(context.Background(), tiny, "image/gif")
		if !errors.Is(err, ErrTooSmall) {
			t.Errorf("PerceptualHash(tiny gif) err = %v, want ErrTooSmall", err)
		}
	})

	t.Run("a large structured first frame hashes successfully", func(t *testing.T) {
		large := encodeGIF(t, structuredImage(guards.MinWidth*2, guards.MinHeight*2, 55))

		_, err := hasher.PerceptualHash(context.Background(), large, "image/gif")
		if err != nil {
			t.Errorf("PerceptualHash(structured gif) unexpected error: %v", err)
		}
	})
}

// ── Decompression bombs (MaxPixels) ─────────────────────────────────────────

// bombPNG builds a minimal, VALID-header PNG that declares width x height but
// carries almost no pixel data.
//
// This is the whole attack: the compressed file is tiny, so the byte cap on the
// fetch path (storage.MaxFileBytes) never sees a problem, but Go's PNG decoder
// allocates width*height*4 the moment it has parsed IHDR — before reading any
// pixel data and before returning the "not enough pixel data" error. A 68-byte
// file declaring 30000x30000 allocates ~3.4 GiB, and an allocation failure in
// Go is a FATAL runtime error, so the gRPC recovery interceptor cannot contain
// it: the process dies.
func bombPNG(t *testing.T, width, height uint32) []byte {
	t.Helper()

	chunk := func(typ string, data []byte) []byte {
		var b bytes.Buffer
		if err := binary.Write(&b, binary.BigEndian, uint32(len(data))); err != nil {
			t.Fatalf("write chunk length: %v", err)
		}
		b.WriteString(typ)
		b.Write(data)
		sum := crc32.NewIEEE()
		sum.Write([]byte(typ))
		sum.Write(data)
		if err := binary.Write(&b, binary.BigEndian, sum.Sum32()); err != nil {
			t.Fatalf("write chunk crc: %v", err)
		}
		return b.Bytes()
	}

	var ihdr bytes.Buffer
	if err := binary.Write(&ihdr, binary.BigEndian, width); err != nil {
		t.Fatalf("write width: %v", err)
	}
	if err := binary.Write(&ihdr, binary.BigEndian, height); err != nil {
		t.Fatalf("write height: %v", err)
	}
	// Bit depth 8, colour type 6 (RGBA), no compression/filter/interlace.
	ihdr.Write([]byte{8, 6, 0, 0, 0})

	var png bytes.Buffer
	png.Write([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'})
	png.Write(chunk("IHDR", ihdr.Bytes()))
	png.Write(chunk("IDAT", []byte{0x78, 0x9c, 0x01, 0x00, 0x00, 0xff, 0xff, 0x00, 0x00, 0x00, 0x01}))
	png.Write(chunk("IEND", nil))

	return png.Bytes()
}

// TestPerceptualHashRefusesADecompressionBomb is a security test, not a
// robustness nicety: without the MaxPixels check this input takes the process
// down, and it is reachable by any guild member uploading a tiny file to an
// allow-listed CDN.
//
// It asserts the refusal happens on the DECLARED dimensions, i.e. before the
// pixel buffer is allocated. If the guard regressed to checking after decode,
// this test would not fail — it would exhaust the machine.
func TestPerceptualHashRefusesADecompressionBomb(t *testing.T) {
	hasher := NewHasher(DefaultGuards(), "")

	// 900 megapixels declared, ~68 bytes on the wire.
	bomb := bombPNG(t, 30000, 30000)
	if len(bomb) > 512 {
		t.Fatalf("crafted bomb is %d bytes; it is meant to be tiny, or it proves nothing about the byte cap", len(bomb))
	}

	config, _, err := image.DecodeConfig(bytes.NewReader(bomb))
	if err != nil {
		t.Fatalf("crafted bomb has an unparseable header, so it would be rejected for the wrong reason: %v", err)
	}
	if int64(config.Width)*int64(config.Height) <= MaxPixels {
		t.Fatalf("crafted bomb declares %dx%d, which is within MaxPixels %d", config.Width, config.Height, MaxPixels)
	}

	if _, err := hasher.PerceptualHash(context.Background(), bomb, "image/png"); !errors.Is(err, ErrTooManyPixels) {
		t.Errorf("PerceptualHash err = %v, want ErrTooManyPixels", err)
	}
}

// TestPerceptualHashAcceptsAnImageWithinMaxPixels is the other half: the guard
// must not be so eager that it rejects ordinary content.
func TestPerceptualHashAcceptsAnImageWithinMaxPixels(t *testing.T) {
	hasher := NewHasher(DefaultGuards(), "")

	content := encodePNG(t, structuredImage(256, 256, 11))
	if _, err := hasher.PerceptualHash(context.Background(), content, "image/png"); err != nil {
		t.Errorf("PerceptualHash on an ordinary 256x256 image: %v", err)
	}
}

// TestPerceptualHashRefusesNonsensicalDeclaredDimensions covers the overflow
// guard: the width*height product is only safe to compute once both factors are
// known to be positive.
func TestPerceptualHashRefusesNonsensicalDeclaredDimensions(t *testing.T) {
	hasher := NewHasher(DefaultGuards(), "")

	// Declared 0 height. image.DecodeConfig accepts some degenerate headers,
	// and whichever way it goes this must not panic or hash anything.
	bomb := bombPNG(t, 1<<31, 1<<31)
	if _, err := hasher.PerceptualHash(context.Background(), bomb, "image/png"); err == nil {
		t.Error("PerceptualHash accepted a header declaring absurd dimensions; want an error")
	}
}
