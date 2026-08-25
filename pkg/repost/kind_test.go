package repost

import (
	"testing"

	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
)

// ── Assumed symbols from pkg/repost/kind.go ──────────────────────────────────
//
// Recorded because these are the symbols the tests below depend on, so a
// change to any of them is a deliberate decision rather than a surprise:
//
//	func Kind(mimeType string) pb.RepostKind
//	func KindFromContentType(contentType string) pb.RepostKind
//	func KindFromFilename(filename string) pb.RepostKind
//
// Kind moved here from pkg/repost/fingerprint, which now delegates to it;
// pkg/discord's own repostKindFromMIME and repostKindFromExtension were
// deleted in favour of the two wrappers. There used to be three independent
// classifiers with three different answers for the same content — pkg/discord
// graded image/tiff as IMAGE and .mov as VIDEO where fingerprint graded both
// FILE — so the whole reason this package exists is that there is now exactly
// one table. The drift test at the bottom is what keeps it that way.

// ── Kind ─────────────────────────────────────────────────────────────────────

// TestKindClassifiesTheSupportedMIMETypes covers the closed set the fetcher
// allows plus the near misses. It is deliberately an exact-match table, not a
// prefix match: "image/" -> IMAGE would grade image/tiff as an image, and
// PerceptualHash has no decoder for it, so every such item would be counted
// as perceptually indexable and then silently fail to index.
func TestKindClassifiesTheSupportedMIMETypes(t *testing.T) {
	tests := []struct {
		mimeType string
		want     pb.RepostKind
	}{
		{"image/png", pb.RepostKind_REPOST_KIND_IMAGE},
		{"image/jpeg", pb.RepostKind_REPOST_KIND_IMAGE},
		{"image/webp", pb.RepostKind_REPOST_KIND_IMAGE},

		// GIF is VIDEO, not IMAGE: it is hashed on its first frame, which is
		// the video treatment, and repost_entry.kind is what decides that.
		{"image/gif", pb.RepostKind_REPOST_KIND_VIDEO},
		{"video/mp4", pb.RepostKind_REPOST_KIND_VIDEO},
		{"video/webm", pb.RepostKind_REPOST_KIND_VIDEO},

		// The near misses: same prefix, no decoder.
		{"image/tiff", pb.RepostKind_REPOST_KIND_FILE},
		{"image/bmp", pb.RepostKind_REPOST_KIND_FILE},
		{"video/quicktime", pb.RepostKind_REPOST_KIND_FILE},

		{"application/pdf", pb.RepostKind_REPOST_KIND_FILE},
		{"audio/mpeg", pb.RepostKind_REPOST_KIND_FILE},
		{"", pb.RepostKind_REPOST_KIND_FILE},
	}

	for _, tt := range tests {
		t.Run(tt.mimeType, func(t *testing.T) {
			if got := Kind(tt.mimeType); got != tt.want {
				t.Errorf("Kind(%q) = %v, want %v", tt.mimeType, got, tt.want)
			}
		})
	}
}

// TestKindIsCaseSensitiveOnItsOwn: Kind takes a SNIFFED MIME type, which
// http.DetectContentType and the fetcher's allow-list both produce
// lowercased. Normalisation is KindFromContentType's job, applied to the
// untrusted client-supplied header — pushing it down into Kind would hide
// which layer is responsible for it.
func TestKindIsCaseSensitiveOnItsOwn(t *testing.T) {
	if got := Kind("IMAGE/PNG"); got != pb.RepostKind_REPOST_KIND_FILE {
		t.Errorf("Kind(%q) = %v, want FILE; normalisation belongs to KindFromContentType", "IMAGE/PNG", got)
	}
}

// ── KindFromContentType ──────────────────────────────────────────────────────

// TestKindFromContentTypeStripsParametersAndNormalises.
//
// A Content-Type header is a media type plus optional parameters, and
// Discord's attachment metadata really does carry them. Comparing the raw
// header against the table would grade "image/png; charset=binary" as FILE —
// so every attachment from a client that annotates its uploads would lose
// perceptual matching entirely, silently and only for those clients.
func TestKindFromContentTypeStripsParametersAndNormalises(t *testing.T) {
	tests := []struct {
		name        string
		contentType string
		want        pb.RepostKind
	}{
		{"plain", "image/png", pb.RepostKind_REPOST_KIND_IMAGE},
		{"a charset parameter", "image/png; charset=binary", pb.RepostKind_REPOST_KIND_IMAGE},
		{"a parameter with no space after the semicolon", "image/png;charset=binary", pb.RepostKind_REPOST_KIND_IMAGE},
		{"a codecs parameter on a video", "video/mp4;codecs=avc1.42E01E", pb.RepostKind_REPOST_KIND_VIDEO},
		{"uppercase", "IMAGE/PNG", pb.RepostKind_REPOST_KIND_IMAGE},
		{"mixed case", "Image/Gif", pb.RepostKind_REPOST_KIND_VIDEO},
		{"leading and trailing space", "  image/webp  ", pb.RepostKind_REPOST_KIND_IMAGE},
		{"space around the media type and a parameter", "  IMAGE/PNG ; charset=binary ", pb.RepostKind_REPOST_KIND_IMAGE},
		{"empty", "", pb.RepostKind_REPOST_KIND_FILE},
		{"whitespace only", "   ", pb.RepostKind_REPOST_KIND_FILE},
		{"a bare semicolon", ";", pb.RepostKind_REPOST_KIND_FILE},
		{"a parameter but no media type", "; charset=binary", pb.RepostKind_REPOST_KIND_FILE},
		{"an unsupported type with a parameter", "application/pdf; version=1.7", pb.RepostKind_REPOST_KIND_FILE},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := KindFromContentType(tt.contentType); got != tt.want {
				t.Errorf("KindFromContentType(%q) = %v, want %v", tt.contentType, got, tt.want)
			}
		})
	}
}

// ── KindFromFilename ─────────────────────────────────────────────────────────

func TestKindFromFilenameClassifiesByExtension(t *testing.T) {
	tests := []struct {
		name     string
		filename string
		want     pb.RepostKind
	}{
		{"png", "photo.png", pb.RepostKind_REPOST_KIND_IMAGE},
		{"jpg", "photo.jpg", pb.RepostKind_REPOST_KIND_IMAGE},
		{"jpeg", "photo.jpeg", pb.RepostKind_REPOST_KIND_IMAGE},
		{"webp", "photo.webp", pb.RepostKind_REPOST_KIND_IMAGE},

		{"gif", "loop.gif", pb.RepostKind_REPOST_KIND_VIDEO},
		{"mp4", "clip.mp4", pb.RepostKind_REPOST_KIND_VIDEO},
		{"webm", "clip.webm", pb.RepostKind_REPOST_KIND_VIDEO},

		{"uppercase extension", "PHOTO.PNG", pb.RepostKind_REPOST_KIND_IMAGE},
		{"mixed-case extension", "Clip.Mp4", pb.RepostKind_REPOST_KIND_VIDEO},

		// .mov has no decoder and no ffmpeg demuxer guarantee, so it is a
		// plain FILE. pkg/discord used to grade it VIDEO, which promised
		// perceptual matching the server could never deliver.
		{"mov is not a supported video", "clip.mov", pb.RepostKind_REPOST_KIND_FILE},
		{"tiff is not a supported image", "scan.tiff", pb.RepostKind_REPOST_KIND_FILE},

		{"no extension at all", "README", pb.RepostKind_REPOST_KIND_FILE},
		{"a dotfile is all extension", ".gitignore", pb.RepostKind_REPOST_KIND_FILE},
		{"an unknown extension", "data.xyz123", pb.RepostKind_REPOST_KIND_FILE},
		{"a double extension takes the last", "archive.tar.gz", pb.RepostKind_REPOST_KIND_FILE},
		{"a double extension ending in a known one", "photo.tar.png", pb.RepostKind_REPOST_KIND_IMAGE},
		{"empty", "", pb.RepostKind_REPOST_KIND_FILE},
		{"a trailing dot", "photo.", pb.RepostKind_REPOST_KIND_FILE},

		// Directories are stripped, and — the part worth pinning — a
		// misleading extension on a PARENT directory must not win.
		{"a path with directories", "/var/tmp/uploads/photo.jpg", pb.RepostKind_REPOST_KIND_IMAGE},
		{"a directory carrying an extension", "/var/tmp/album.png/notes.txt", pb.RepostKind_REPOST_KIND_FILE},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := KindFromFilename(tt.filename); got != tt.want {
				t.Errorf("KindFromFilename(%q) = %v, want %v", tt.filename, got, tt.want)
			}
		})
	}
}

// TestKindFromFilenameTakesAFilenameNotAURL pins a real trap rather than an
// abstract edge case: filepath.Ext on a URL with a query string returns
// ".png?size=large", which classifies as FILE. Callers must pass the
// attachment's filename, and this is what says so out loud.
func TestKindFromFilenameTakesAFilenameNotAURL(t *testing.T) {
	if got := KindFromFilename("photo.png?size=large"); got != pb.RepostKind_REPOST_KIND_FILE {
		t.Errorf("KindFromFilename(%q) = %v, want FILE; the query string is part of the extension, so callers must pass a filename",
			"photo.png?size=large", got)
	}
}

// ── The two tables must not drift apart ──────────────────────────────────────

// TestKindFromFilenameAgreesWithKindForEveryCanonicalPair is the whole reason
// the classifier was unified into one package.
//
// The extension table and the MIME table are separate switches over the same
// closed set of content types, so nothing but a test stops someone adding
// ".avif" to one and forgetting the other. When they disagree, a client sends
// a candidate declared IMAGE, the server sniffs the bytes and stores it as
// FILE, and the mismatch is invisible: the candidate is still indexed, just
// never perceptually matched.
//
// Both directions of the set are covered — the pairs that must classify as
// IMAGE or VIDEO, and the pairs that must classify as FILE — so the test
// cannot be satisfied by a classifier that answers FILE for everything.
func TestKindFromFilenameAgreesWithKindForEveryCanonicalPair(t *testing.T) {
	pairs := []struct {
		extension string
		mimeType  string
	}{
		{".png", "image/png"},
		{".jpg", "image/jpeg"},
		{".jpeg", "image/jpeg"},
		{".webp", "image/webp"},
		{".gif", "image/gif"},
		{".mp4", "video/mp4"},
		{".webm", "video/webm"},

		// The negative side of the set: both tables must agree these are
		// FILE, not merely agree on the supported ones.
		{".mov", "video/quicktime"},
		{".tiff", "image/tiff"},
		{".bmp", "image/bmp"},
		{".pdf", "application/pdf"},
	}

	// Guards against the vacuous version of this test: if every pair below
	// graded FILE, agreement would prove nothing.
	var perceptual int

	for _, pair := range pairs {
		t.Run(pair.extension, func(t *testing.T) {
			fromMIME := Kind(pair.mimeType)
			fromExtension := KindFromFilename("content" + pair.extension)

			if fromExtension != fromMIME {
				t.Errorf("KindFromFilename(%q) = %v but Kind(%q) = %v; the extension and MIME tables have drifted apart",
					"content"+pair.extension, fromExtension, pair.mimeType, fromMIME)
			}

			// The content-type wrapper has to reach the same answer too, or
			// an attachment classified from its header and the same
			// attachment classified from its name would disagree.
			if fromContentType := KindFromContentType(pair.mimeType); fromContentType != fromMIME {
				t.Errorf("KindFromContentType(%q) = %v, want %v (the same as Kind)",
					pair.mimeType, fromContentType, fromMIME)
			}

			if fromMIME != pb.RepostKind_REPOST_KIND_FILE {
				perceptual++
			}
		})
	}

	if perceptual == 0 {
		t.Error("no pair in the table classified as anything but FILE, so agreement between the tables proves nothing")
	}
}
