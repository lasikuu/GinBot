package fingerprint

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// ffmpegTimeout bounds one frame extraction. ADR-0006 requires a timeout on
// every ffmpeg invocation; 10 seconds is ample for decoding a single frame
// out of an 8 MiB clip (storage.MaxFileBytes), and a hung or malicious input
// must not pin a handler goroutine indefinitely.
const ffmpegTimeout = 10 * time.Second

// maxFrameDimension caps the width and height of the frame ffmpeg hands back.
//
// Without it the extracted PNG is whatever resolution the source clip was, so
// an 8 MiB 8K clip yields a frame that decodes to roughly 132 MB of RGBA —
// times however many extractions run concurrently. PerceptionHash resizes to
// 64x64 internally, so capping here costs no accuracy whatsoever.
const maxFrameDimension = 1024

// extractFrame writes content to a temporary file and shells out to ffmpeg to
// pull its first frame as a PNG, returning the PNG bytes.
//
// Per ADR-0006 (W12), ffmpeg is invoked as a subprocess via os/exec, never
// linked via cgo — that is a licensing constraint, not a style preference.
// The only variables on the command line are server-generated temporary file
// paths; content itself never reaches a flag, only the files ffmpeg is told
// to read from and write to.
func (h *Hasher) extractFrame(ctx context.Context, content []byte) ([]byte, error) {
	// Both files live in a private directory rather than directly in
	// os.TempDir(), and this is load-bearing. The output path is necessarily
	// derived from the input path, so in a world-writable /tmp (mode 1777) a
	// local unprivileged account could watch for the input file to appear,
	// pre-create the output path as a symlink, and have ffmpeg — which is
	// invoked with -y, i.e. overwrite without asking — write the decoded frame
	// through it. MkdirTemp creates with mode 0700, so nothing outside this
	// process can see or plant either path.
	dir, err := os.MkdirTemp("", "ginbot-repost-frame-*")
	if err != nil {
		return nil, fmt.Errorf("create temp frame directory: %w", err)
	}
	defer func() {
		// RemoveAll rather than two Removes: it also collects a partial or
		// unexpectedly-named output that ffmpeg may have left behind.
		_ = os.RemoveAll(dir)
	}()

	inputPath := filepath.Join(dir, "input")
	outputPath := filepath.Join(dir, "frame.png")

	if err := os.WriteFile(inputPath, content, 0o600); err != nil {
		return nil, fmt.Errorf("write temp video file: %w", err)
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, ffmpegTimeout)
	defer cancel()

	// -y: overwrite the output path without prompting (there is nothing to
	// prompt on a non-interactive subprocess, and a stall waiting for
	// confirmation would just burn the timeout).
	// -loglevel error: keep stderr free of ffmpeg's normal banner and
	// progress noise, which is not user-controlled but is still not
	// something worth capturing.
	// -frames:v 1: exactly one frame — the first — is all a fingerprint
	// needs.
	// -vf scale: bound the decoded frame (see maxFrameDimension). The filter
	// is a fixed literal; min() keeps a smaller source untouched rather than
	// upscaling it, and -2 preserves aspect ratio on an even boundary.
	cmd := exec.CommandContext(timeoutCtx, h.ffmpegPath,
		"-y",
		"-loglevel", "error",
		"-i", inputPath,
		"-frames:v", "1",
		"-vf", fmt.Sprintf("scale='min(%d,iw)':-2", maxFrameDimension),
		outputPath,
	)
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("extract video frame: %w", err)
	}

	frame, err := os.ReadFile(outputPath)
	if err != nil {
		return nil, fmt.Errorf("read extracted video frame: %w", err)
	}

	return frame, nil
}
