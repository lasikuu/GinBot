package repost

import (
	"path/filepath"
	"strings"

	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/proto"
)

// Kind classifies a sniffed MIME type, both for repost_entry.kind and for
// which decode path fingerprint.PerceptualHash takes. It is deliberately
// driven by the content actually fetched and sniffed, never by what a caller
// claims a candidate is — see RepostMatch.kind's own doc comment on the
// original entry possibly differing from the candidate's declared kind.
//
// It lives here rather than in pkg/repost/fingerprint so that the client-side
// classifiers below and the server-side authority are one table. They were two,
// and they disagreed: image/tiff was IMAGE to the client and FILE to the
// server, .mov VIDEO and FILE. Nothing observed the disagreement — the server
// only ever reads the declared kind to decide LINK versus not — but two tables
// that must agree and are never compared only drift further apart.
func Kind(mimeType string) pb.RepostKind {
	switch mimeType {
	case "image/png", "image/jpeg", "image/webp":
		return pb.RepostKind_REPOST_KIND_IMAGE
	case "image/gif", "video/mp4", "video/webm":
		return pb.RepostKind_REPOST_KIND_VIDEO
	default:
		return pb.RepostKind_REPOST_KIND_FILE
	}
}

// KindFromContentType classifies a declared Content-Type header, which may
// carry parameters ("image/png; charset=binary").
//
// It normalises and then delegates rather than matching prefixes of its own:
// a prefix rule would classify types Kind has never heard of, which is exactly
// how the two classifiers diverged before.
func KindFromContentType(contentType string) pb.RepostKind {
	if idx := strings.IndexByte(contentType, ';'); idx >= 0 {
		contentType = contentType[:idx]
	}

	return Kind(strings.ToLower(strings.TrimSpace(contentType)))
}

// KindFromFilename classifies by filename extension, for attachments that
// arrive with no declared content type.
//
// The table is hand-maintained to mirror Kind's MIME set exactly.
// mime.TypeByExtension is deliberately not used: it consults /etc/mime.types,
// so the same attachment would classify differently on a developer's machine,
// in CI, and in the deployed container.
func KindFromFilename(filename string) pb.RepostKind {
	switch strings.ToLower(filepath.Ext(filename)) {
	case ".png", ".jpg", ".jpeg", ".webp":
		return pb.RepostKind_REPOST_KIND_IMAGE
	case ".gif", ".mp4", ".webm":
		return pb.RepostKind_REPOST_KIND_VIDEO
	default:
		return pb.RepostKind_REPOST_KIND_FILE
	}
}
