// Package fingerprint computes an exact SHA-256 of the raw bytes and a
// perceptual DCT hash (ADR-0005) for images and video first frames.
package fingerprint

import (
	"bytes"
	"context"
	"crypto/sha256"
	hexenc "encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/color"
	_ "image/gif"  // registers the GIF decoder with image.Decode
	_ "image/jpeg" // registers the JPEG decoder with image.Decode
	_ "image/png"  // registers the PNG decoder with image.Decode
	"math"
	"os/exec"

	"github.com/corona10/goimagehash"
	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
	"github.com/lasikuu/GinBot/pkg/repost"
)

// ErrTooSmall reports an image below the minimum dimensions. Non-fatal to
// ingest: the content hash is still indexed.
var ErrTooSmall = errors.New("image is below the minimum dimensions")

// ErrLowEntropy reports a near-blank or solid-colour image, which hashes to
// near-identical values for unrelated content. Non-fatal to ingest.
var ErrLowEntropy = errors.New("image entropy is below the floor")

// ErrNoDecoder reports content nothing here can decode: image/webp, or a
// video/GIF with no ffmpeg configured. Non-fatal to ingest.
var ErrNoDecoder = errors.New("no decoder is available for this content")

// Guards bound which images are eligible for perceptual matching.
type Guards struct {
	MinWidth  int
	MinHeight int
	// MinEntropy is the Shannon entropy floor of the 8-bit greyscale
	// histogram, in bits, so the range is 0..8.
	MinEntropy float64
}

// DefaultGuards returns 128x128 and an entropy floor of 3.0 bits.
func DefaultGuards() Guards {
	return Guards{MinWidth: 128, MinHeight: 128, MinEntropy: 3.0}
}

// ContentHash returns the raw SHA-256 digest of content and its lowercase hex.
func ContentHash(content []byte) (sum []byte, hex string) {
	digest := sha256.Sum256(content)
	return digest[:], hexenc.EncodeToString(digest[:])
}

// Entropy returns the Shannon entropy in bits (0..8) of img's 8-bit greyscale
// histogram; a solid colour scores 0. Walks every pixel, so only pass images
// from decodeBounded.
func Entropy(img image.Image) float64 {
	bounds := img.Bounds()

	var histogram [256]int
	total := 0
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			gray := color.GrayModel.Convert(img.At(x, y)).(color.Gray)
			histogram[gray.Y]++
			total++
		}
	}
	if total == 0 {
		return 0
	}

	var entropy float64
	for _, count := range histogram {
		if count == 0 {
			continue
		}
		p := float64(count) / float64(total)
		entropy -= p * math.Log2(p)
	}

	return entropy
}

// LookupFFmpeg returns the path to an ffmpeg binary, or "" when none is on
// PATH. "" is not an error: video degrades to exact-hash-only.
func LookupFFmpeg() string {
	path, err := exec.LookPath("ffmpeg")
	if err != nil {
		return ""
	}
	return path
}

// Kind classifies a sniffed MIME type, selecting PerceptualHash's decode path.
func Kind(mimeType string) pb.RepostKind {
	return repost.Kind(mimeType)
}

// MaxPixels is a decompression-bomb bound, not a tuning knob: Go's PNG decoder
// allocates width*height*4 straight from the header, so the 8 MiB byte cap
// bounds nothing. 40 MP is far above anything worth fingerprinting.
const MaxPixels = 40 << 20

// ErrTooManyPixels reports declared dimensions exceeding MaxPixels. Non-fatal
// to ingest.
var ErrTooManyPixels = errors.New("image exceeds the maximum decoded pixel count")

// decodeBounded checks the header dimensions before any pixel buffer is
// allocated. It is the only route by which this package decodes fetched bytes.
func decodeBounded(content []byte) (image.Image, error) {
	config, _, err := image.DecodeConfig(bytes.NewReader(content))
	if err != nil {
		return nil, fmt.Errorf("decode image header: %w", err)
	}

	// Checked before multiplying so the product cannot overflow and pass.
	if config.Width <= 0 || config.Height <= 0 {
		return nil, fmt.Errorf("decode image header: nonsensical dimensions %dx%d", config.Width, config.Height)
	}
	if int64(config.Width)*int64(config.Height) > MaxPixels {
		return nil, ErrTooManyPixels
	}

	img, _, err := image.Decode(bytes.NewReader(content))
	if err != nil {
		return nil, fmt.Errorf("decode image: %w", err)
	}

	return img, nil
}

// Hasher produces perceptual hashes, subject to Guards.
type Hasher struct {
	guards     Guards
	ffmpegPath string
}

// NewHasher returns a Hasher. An empty ffmpegPath disables video frame
// extraction, degrading video to exact-hash-only rather than failing.
func NewHasher(guards Guards, ffmpegPath string) *Hasher {
	return &Hasher{guards: guards, ffmpegPath: ffmpegPath}
}

// PerceptualHash returns the 64-bit DCT pHash of content. GIF and video are
// hashed on their first frame; image/webp and unconfigured ffmpeg give
// ErrNoDecoder.
func (h *Hasher) PerceptualHash(ctx context.Context, content []byte, mimeType string) (uint64, error) {
	switch mimeType {
	case "image/png", "image/jpeg", "image/gif":
		img, err := decodeBounded(content)
		if err != nil {
			return 0, err
		}
		return h.hashImage(img)

	case "image/webp":
		return 0, ErrNoDecoder

	case "video/mp4", "video/webm":
		if h.ffmpegPath == "" {
			return 0, ErrNoDecoder
		}
		frame, err := h.extractFrame(ctx, content)
		if err != nil {
			return 0, err
		}
		// Bounded too: ffmpeg's scale filter is not trusted to have applied.
		img, err := decodeBounded(frame)
		if err != nil {
			return 0, err
		}
		return h.hashImage(img)

	default:
		return 0, ErrNoDecoder
	}
}

// hashImage applies the guards to a decoded frame's real dimensions, then hashes.
func (h *Hasher) hashImage(img image.Image) (uint64, error) {
	bounds := img.Bounds()
	if bounds.Dx() < h.guards.MinWidth || bounds.Dy() < h.guards.MinHeight {
		return 0, ErrTooSmall
	}
	if Entropy(img) < h.guards.MinEntropy {
		return 0, ErrLowEntropy
	}

	hash, err := goimagehash.PerceptionHash(img)
	if err != nil {
		return 0, fmt.Errorf("compute perceptual hash: %w", err)
	}

	return hash.GetHash(), nil
}
