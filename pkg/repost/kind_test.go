package repost

import (
	"testing"

	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
)

func TestKindClassifiesTheSupportedMIMETypes(t *testing.T) {
	tests := []struct {
		mimeType string
		want     pb.RepostKind
	}{
		{"image/png", pb.RepostKind_REPOST_KIND_IMAGE},
		{"image/jpeg", pb.RepostKind_REPOST_KIND_IMAGE},
		{"image/webp", pb.RepostKind_REPOST_KIND_IMAGE},

		// GIF is VIDEO: it is hashed on its first frame.
		{"image/gif", pb.RepostKind_REPOST_KIND_VIDEO},
		{"video/mp4", pb.RepostKind_REPOST_KIND_VIDEO},
		{"video/webm", pb.RepostKind_REPOST_KIND_VIDEO},

		// Near misses: same prefix, no decoder.
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

// TestKindIsCaseSensitiveOnItsOwn: Kind takes an already-lowercased sniffed
// type; normalisation is KindFromContentType's job.
func TestKindIsCaseSensitiveOnItsOwn(t *testing.T) {
	if got := Kind("IMAGE/PNG"); got != pb.RepostKind_REPOST_KIND_FILE {
		t.Errorf("Kind(%q) = %v, want FILE; normalisation belongs to KindFromContentType", "IMAGE/PNG", got)
	}
}

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

		{"mov is not a supported video", "clip.mov", pb.RepostKind_REPOST_KIND_FILE},
		{"tiff is not a supported image", "scan.tiff", pb.RepostKind_REPOST_KIND_FILE},

		{"no extension at all", "README", pb.RepostKind_REPOST_KIND_FILE},
		{"a dotfile is all extension", ".gitignore", pb.RepostKind_REPOST_KIND_FILE},
		{"an unknown extension", "data.xyz123", pb.RepostKind_REPOST_KIND_FILE},
		{"a double extension takes the last", "archive.tar.gz", pb.RepostKind_REPOST_KIND_FILE},
		{"a double extension ending in a known one", "photo.tar.png", pb.RepostKind_REPOST_KIND_IMAGE},
		{"empty", "", pb.RepostKind_REPOST_KIND_FILE},
		{"a trailing dot", "photo.", pb.RepostKind_REPOST_KIND_FILE},

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

// TestKindFromFilenameTakesAFilenameNotAURL: filepath.Ext on a URL returns
// ".png?size=large", so callers must pass a filename.
func TestKindFromFilenameTakesAFilenameNotAURL(t *testing.T) {
	if got := KindFromFilename("photo.png?size=large"); got != pb.RepostKind_REPOST_KIND_FILE {
		t.Errorf("KindFromFilename(%q) = %v, want FILE; the query string is part of the extension, so callers must pass a filename",
			"photo.png?size=large", got)
	}
}

// TestKindFromFilenameAgreesWithKindForEveryCanonicalPair guards the extension
// and MIME tables against drift.
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

		// The negative side of the set.
		{".mov", "video/quicktime"},
		{".tiff", "image/tiff"},
		{".bmp", "image/bmp"},
		{".pdf", "application/pdf"},
	}

	// Guards against the vacuous case where every pair grades FILE.
	var perceptual int

	for _, pair := range pairs {
		t.Run(pair.extension, func(t *testing.T) {
			fromMIME := Kind(pair.mimeType)
			fromExtension := KindFromFilename("content" + pair.extension)

			if fromExtension != fromMIME {
				t.Errorf("KindFromFilename(%q) = %v but Kind(%q) = %v; the extension and MIME tables have drifted apart",
					"content"+pair.extension, fromExtension, pair.mimeType, fromMIME)
			}

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
