package fingerprint

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestLookupFFmpegIsConsistentWithPATH(t *testing.T) {
	got := LookupFFmpeg()

	wantPath, err := exec.LookPath("ffmpeg")
	wantPresent := err == nil

	if wantPresent && got == "" {
		t.Errorf("LookupFFmpeg() = %q, but exec.LookPath(\"ffmpeg\") found %q", got, wantPath)
	}
	if !wantPresent && got != "" {
		t.Errorf("LookupFFmpeg() = %q, but exec.LookPath(\"ffmpeg\") found nothing", got)
	}

	if got != "" {
		info, statErr := os.Stat(got)
		if statErr != nil {
			t.Errorf("LookupFFmpeg() = %q does not stat: %v", got, statErr)
		} else if info.IsDir() {
			t.Errorf("LookupFFmpeg() = %q is a directory, not an executable", got)
		}
	}
}

func TestLookupFFmpegIsDeterministic(t *testing.T) {
	first := LookupFFmpeg()
	second := LookupFFmpeg()
	if first != second {
		t.Errorf("LookupFFmpeg() = %q then %q, want the same result", first, second)
	}
}

// TestPerceptualHashVideoWithoutFFmpegReturnsErrNoDecoder: the context
// deadline is the hang guard, not incidental.
func TestPerceptualHashVideoWithoutFFmpegReturnsErrNoDecoder(t *testing.T) {
	hasher := NewHasher(DefaultGuards(), "")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	garbage := []byte("not a real mp4 container, just bytes")

	_, err := hasher.PerceptualHash(ctx, garbage, "video/mp4")
	if !errors.Is(err, ErrNoDecoder) {
		t.Errorf("PerceptualHash(video, no ffmpeg configured) err = %v, want ErrNoDecoder", err)
	}
}

func TestPerceptualHashVideoWithoutFFmpegDoesNotPanic(t *testing.T) {
	hasher := NewHasher(DefaultGuards(), "")

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("PerceptualHash panicked: %v", r)
		}
	}()

	_, _ = hasher.PerceptualHash(context.Background(), []byte{0x00, 0x01, 0x02}, "video/mp4")
}

// generateTestVideo builds a decodable MP4 from ffmpeg's lavfi test source. It
// reports false for any failure, so callers skip rather than fail.
func generateTestVideo(t *testing.T, ffmpegPath string) ([]byte, bool) {
	t.Helper()

	out := filepath.Join(t.TempDir(), "fixture.mp4")

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, ffmpegPath,
		"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc=size=256x256:rate=1:duration=1",
		"-frames:v", "3",
		out,
	)
	if err := cmd.Run(); err != nil {
		return nil, false
	}

	content, err := os.ReadFile(out)
	if err != nil || len(content) == 0 {
		return nil, false
	}

	return content, true
}

func TestPerceptualHashVideoWithFFmpegHashesTheFirstFrame(t *testing.T) {
	ffmpegPath := LookupFFmpeg()
	if ffmpegPath == "" {
		t.Skip("ffmpeg is not on PATH; the subprocess frame-extraction path cannot be exercised here")
	}

	content, ok := generateTestVideo(t, ffmpegPath)
	if !ok {
		t.Skip("could not generate a synthetic test video with the available ffmpeg build")
	}

	hasher := NewHasher(DefaultGuards(), ffmpegPath)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	_, err := hasher.PerceptualHash(ctx, content, "video/mp4")
	if err != nil {
		t.Errorf("PerceptualHash(real video, ffmpeg available) unexpected error: %v", err)
	}
}
