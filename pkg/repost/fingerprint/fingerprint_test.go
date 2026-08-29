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

func encodePNG(t *testing.T, img image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode fixture png: %v", err)
	}
	return buf.Bytes()
}

func encodeGIF(t *testing.T, img image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := gif.Encode(&buf, img, nil); err != nil {
		t.Fatalf("encode fixture gif: %v", err)
	}
	return buf.Bytes()
}

// solidImage is a single flat colour: the zero-entropy case.
func solidImage(width, height int, c color.Gray) *image.Gray {
	img := image.NewGray(image.Rect(0, 0, width, height))
	for y := range height {
		for x := range width {
			img.SetGray(x, y, c)
		}
	}
	return img
}

// nearBlankImage perturbs roughly 1% of pixels, landing under the 3.0-bit
// entropy floor without being a literal single-colour fill.
func nearBlankImage(width, height int) *image.Gray {
	img := image.NewGray(image.Rect(0, 0, width, height))
	rng := rand.New(rand.NewPCG(1, 1))
	for y := range height {
		for x := range width {
			v := uint8(10)
			if rng.IntN(100) == 0 {
				v = 250
			}
			img.SetGray(x, y, color.Gray{Y: v})
		}
	}
	return img
}

// uniformNoiseImage is the near-8-bit-entropy case.
func uniformNoiseImage(width, height int, seed uint64) *image.Gray {
	img := image.NewGray(image.Rect(0, 0, width, height))
	rng := rand.New(rand.NewPCG(seed, seed+1))
	for y := range height {
		for x := range width {
			img.SetGray(x, y, color.Gray{Y: uint8(rng.IntN(256))})
		}
	}
	return img
}

// structuredImage has high entropy but low-frequency structure for pHash.
func structuredImage(width, height int, seed uint64) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	rng := rand.New(rand.NewPCG(seed, seed+7))

	for y := range height {
		for x := range width {
			base := uint8((x*255)/max(width, 1)+(y*255)/max(height, 1)) / 2
			noise := uint8(rng.IntN(40))
			img.Set(x, y, color.RGBA{R: base + noise, G: base, B: 255 - base, A: 255})
		}
	}

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

func TestEntropyOfAUniformFillIsZero(t *testing.T) {
	img := solidImage(64, 64, color.Gray{Y: 128})

	got := Entropy(img)
	if got != 0 {
		t.Errorf("Entropy(solid fill) = %v, want 0", got)
	}
}

// TestEntropyOfUniformNoiseIsNearEight: log2(256) = 8 is the histogram maximum.
func TestEntropyOfUniformNoiseIsNearEight(t *testing.T) {
	img := uniformNoiseImage(256, 256, 42)

	got := Entropy(img)
	if got < 7.9 || got > 8.0 {
		t.Errorf("Entropy(uniform noise) = %v, want in [7.9, 8.0]", got)
	}
}

func TestEntropyIsMonotonicBetweenSolidAndNoisy(t *testing.T) {
	blank := Entropy(nearBlankImage(128, 128))
	noisy := Entropy(uniformNoiseImage(128, 128, 7))

	if !(blank < noisy) {
		t.Errorf("Entropy(near-blank) = %v, Entropy(noisy) = %v; want near-blank strictly lower", blank, noisy)
	}
}

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

// TestPerceptualHashRejectsBelowMinimumDimensions uses high-entropy content so
// only the size guard can be what refuses it.
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

func TestPerceptualHashHasNoDecoderForNonVisualContent(t *testing.T) {
	guards := DefaultGuards()
	hasher := NewHasher(guards, "")

	garbage := []byte("not an image or a video, just some file bytes")

	_, err := hasher.PerceptualHash(context.Background(), garbage, "audio/mpeg")
	if !errors.Is(err, ErrNoDecoder) {
		t.Errorf("PerceptualHash(audio content) err = %v, want ErrNoDecoder", err)
	}
}

func TestPerceptualHashDecodesARealGIFAndAppliesTheSameGuards(t *testing.T) {
	guards := DefaultGuards()
	hasher := NewHasher(guards, "")

	t.Run("below minimum dimensions is refused", func(t *testing.T) {
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

// bombPNG builds a valid-header PNG declaring width x height but carrying
// almost no pixel data.
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

// TestPerceptualHashRefusesADecompressionBomb asserts the refusal happens on
// the declared dimensions; a guard that checked after decode would exhaust the
// machine rather than fail here.
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

func TestPerceptualHashAcceptsAnImageWithinMaxPixels(t *testing.T) {
	hasher := NewHasher(DefaultGuards(), "")

	content := encodePNG(t, structuredImage(256, 256, 11))
	if _, err := hasher.PerceptualHash(context.Background(), content, "image/png"); err != nil {
		t.Errorf("PerceptualHash on an ordinary 256x256 image: %v", err)
	}
}

// TestPerceptualHashRefusesNonsensicalDeclaredDimensions covers the overflow
// guard: width*height is only safe to compute once both factors are positive.
func TestPerceptualHashRefusesNonsensicalDeclaredDimensions(t *testing.T) {
	hasher := NewHasher(DefaultGuards(), "")

	bomb := bombPNG(t, 1<<31, 1<<31)
	if _, err := hasher.PerceptualHash(context.Background(), bomb, "image/png"); err == nil {
		t.Error("PerceptualHash accepted a header declaring absurd dimensions; want an error")
	}
}
