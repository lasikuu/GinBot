package repost

import (
	"path/filepath"
	"strings"

	pb "github.com/lasikuu/GinBot/pkg/gen/ginbot/v1"
)

// Kind classifies a sniffed MIME type. It must be driven by content actually
// fetched and sniffed, never by what a caller claims a candidate is.
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
func KindFromContentType(contentType string) pb.RepostKind {
	if idx := strings.IndexByte(contentType, ';'); idx >= 0 {
		contentType = contentType[:idx]
	}

	return Kind(strings.ToLower(strings.TrimSpace(contentType)))
}

// KindFromFilename classifies by filename extension, for attachments with no
// declared content type. The table is hand-maintained to mirror Kind's MIME
// set; mime.TypeByExtension is host-dependent and must not be used here.
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
