package fingerprint

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// ffmpegTimeout bounds one frame extraction so a hung input cannot pin a goroutine.
const ffmpegTimeout = 10 * time.Second

// maxFrameDimension bounds the RGBA the returned frame decodes to. Costs no
// accuracy: PerceptionHash resizes to 64x64 internally.
const maxFrameDimension = 1024

// extractFrame shells out to ffmpeg for content's first frame as PNG bytes.
// ffmpeg must stay a subprocess and never be linked via cgo (ADR-0006,
// licensing).
func (h *Hasher) extractFrame(ctx context.Context, content []byte) ([]byte, error) {
	// A private 0700 directory, not bare os.TempDir(): the output path is
	// derived from the input path, so in a world-writable /tmp another account
	// could pre-plant it as a symlink for ffmpeg's -y to write through.
	dir, err := os.MkdirTemp("", "ginbot-repost-frame-*")
	if err != nil {
		return nil, fmt.Errorf("create temp frame directory: %w", err)
	}
	defer func() {
		// RemoveAll also collects any partial output ffmpeg left behind.
		_ = os.RemoveAll(dir)
	}()

	inputPath := filepath.Join(dir, "input")
	outputPath := filepath.Join(dir, "frame.png")

	if err := os.WriteFile(inputPath, content, 0o600); err != nil {
		return nil, fmt.Errorf("write temp video file: %w", err)
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, ffmpegTimeout)
	defer cancel()

	// The scale filter's min() avoids upscaling a smaller source; -2 preserves
	// aspect ratio on an even boundary.
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
