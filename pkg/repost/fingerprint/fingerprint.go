// Package fingerprint computes the two hashes WANHA indexes: an exact SHA-256
// of the raw bytes, and a perceptual DCT hash (ADR-0005) for images and video
// first frames. It is the only package in this feature that touches decoded
// pixel data, so the accuracy guards (minimum size, entropy floor) live here
// too, applied before a hash is ever computed.
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

// ErrTooSmall reports an image below the minimum dimensions (W4). It is not
// fatal: the caller still indexes the content hash, so the item stays
// detectable by exact re-upload even though it is excluded from perceptual
// matching.
var ErrTooSmall = errors.New("image is below the minimum dimensions")

// ErrLowEntropy reports an image at or below the entropy floor (W4) — a
// near-blank or solid-colour image, which produces near-identical perceptual
// hashes for unrelated content regardless of algorithm. Also not fatal, for
// the same reason as ErrTooSmall.
var ErrLowEntropy = errors.New("image entropy is below the floor")

// ErrNoDecoder reports content nothing here can decode: a MIME type with no
// registered still-image decoder (image/webp has none in the standard
// library), or a video/GIF when no ffmpeg binary is configured. Also not
// fatal: video and GIF still get an exact SHA-256, per W12's degrade-rather-
// than-crash requirement.
var ErrNoDecoder = errors.New("no decoder is available for this content")

// Guards bound which images are eligible for perceptual matching.
type Guards struct {
	MinWidth  int
	MinHeight int
	// MinEntropy is the Shannon entropy floor of the 8-bit greyscale
	// histogram, in bits, so the range is 0..8.
	MinEntropy float64
}

// DefaultGuards returns 128x128 and an entropy floor of 3.0 bits — starting
// points from the literature (see internal/config/repost.go), not values
// validated against this community's content.
func DefaultGuards() Guards {
	return Guards{MinWidth: 128, MinHeight: 128, MinEntropy: 3.0}
}

// ContentHash returns the raw SHA-256 digest of content and its lowercase hex
// form.
func ContentHash(content []byte) (sum []byte, hex string) {
	digest := sha256.Sum256(content)
	return digest[:], hexenc.EncodeToString(digest[:])
}

// Entropy returns the Shannon entropy, in bits, of img's 8-bit greyscale
// histogram. A solid-colour image scores 0; a near-blank image scores close
// to 0. This is what W4's guard filters on.
//
// It walks every pixel, so it is O(width*height). decodeBounded is what keeps
// that bounded — never call this on an image decoded by any other route.
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
// PATH. An empty result is not an error: video fingerprinting degrades to
// exact-hash-only, per ADR-0006.
func LookupFFmpeg() string {
	path, err := exec.LookPath("ffmpeg")
	if err != nil {
		return ""
	}
	return path
}

// Kind classifies a sniffed MIME type, both for repost_entry.kind and for
// which decode path PerceptualHash takes. It is deliberately driven by the
// content actually fetched and sniffed, never by what a caller claims a
// candidate is — see RepostMatch.kind's own doc comment on the original entry
// possibly differing from the candidate's declared kind.
//
// The table itself moved to repost.Kind, which the client-side classifiers
// also delegate to; this stays as the name the ingest path already calls, so
// that there is exactly one table rather than a server copy and a client copy
// free to drift.
func Kind(mimeType string) pb.RepostKind {
	return repost.Kind(mimeType)
}

// MaxPixels caps the decoded pixel count of anything this package will hash.
//
// This is a hard security bound, not a tuning knob. The byte cap on the
// compressed input (storage.MaxFileBytes, 8 MiB) does not bound the decoded
// size at all: Go's PNG decoder allocates width*height*4 as soon as it has
// parsed IHDR, before reading any pixel data, so a 68-byte file declaring
// 30000x30000 allocates 3.4 GiB. A Go allocation failure is a fatal runtime
// error, not a recoverable panic, so the gRPC recovery interceptor cannot
// contain it — the process dies. Any guild member can upload such a file to
// an allow-listed CDN, and one message may carry several.
//
// 40 megapixels is roughly a 8000x5000 image, far above anything worth
// fingerprinting: PerceptionHash resizes to 64x64 regardless.
const MaxPixels = 40 << 20

// ErrTooManyPixels reports content whose declared dimensions exceed MaxPixels.
// Like the guard errors it is non-fatal to ingest: the content is still
// indexed for exact matching.
var ErrTooManyPixels = errors.New("image exceeds the maximum decoded pixel count")

// decodeBounded decodes an image only after its declared dimensions prove it
// is safe to decode.
//
// DecodeConfig reads just the header, so the dimensions are known before any
// pixel buffer is allocated. This is the ONLY route by which this package
// decodes fetched bytes; a bare image.Decode on caller-supplied content is a
// decompression bomb waiting to happen.
func decodeBounded(content []byte) (image.Image, error) {
	config, _, err := image.DecodeConfig(bytes.NewReader(content))
	if err != nil {
		return nil, fmt.Errorf("decode image header: %w", err)
	}

	// Guarded against a negative or absurd header value before multiplying, so
	// the product itself cannot overflow into a small number and pass.
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
// extraction, degrading video to exact-hash-only rather than failing
// (ADR-0006, W12).
func NewHasher(guards Guards, ffmpegPath string) *Hasher {
	return &Hasher{guards: guards, ffmpegPath: ffmpegPath}
}

// PerceptualHash returns the 64-bit DCT pHash of content.
//
// image/webp has no decoder in the standard library; adding one just for this
// is out of scope, so it returns ErrNoDecoder rather than failing the whole
// ingest. Animated GIF is hashed on its first frame only (image/gif.Decode
// already does this). Video shells out to ffmpeg for a first-frame extraction
// (ADR-0006) and returns ErrNoDecoder when no binary is configured.
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
		// Bounded too, even though extractFrame caps the frame's resolution
		// with a scale filter: the bound belongs on every decode path, not on
		// the trust that an external process honoured its arguments.
		img, err := decodeBounded(frame)
		if err != nil {
			return 0, err
		}
		return h.hashImage(img)

	default:
		return 0, ErrNoDecoder
	}
}

// hashImage applies the guards to a decoded frame and, only once both pass,
// computes its perceptual hash. The guards run against the frame's REAL
// decoded dimensions, not anything claimed about the source file.
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
