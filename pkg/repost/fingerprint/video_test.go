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

// This file covers acceptance criterion 17: ffmpeg absence must degrade video
// to exact-hash-only rather than crashing ingest (docs/plans/wanha.md, the
// "ffmpeg licensing (W12)" section, and ADR-0006).

// ── LookupFFmpeg ──────────────────────────────────────────────────────────────

// TestLookupFFmpegIsConsistentWithPATH: LookupFFmpeg must agree with
// exec.LookPath, since that is the documented contract ("a path to an ffmpeg
// binary, or "" when none is on PATH") and any other source of truth would let
// the two disagree about whether the subprocess path is usable.
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

// TestLookupFFmpegIsDeterministic: repeated calls within the same process must
// agree, since NewHasher is expected to be told the result once at startup
// rather than re-resolving PATH on every hash.
func TestLookupFFmpegIsDeterministic(t *testing.T) {
	first := LookupFFmpeg()
	second := LookupFFmpeg()
	if first != second {
		t.Errorf("LookupFFmpeg() = %q then %q, want the same result", first, second)
	}
}

// ── Video degradation without ffmpeg ─────────────────────────────────────────

// TestPerceptualHashVideoWithoutFFmpegReturnsErrNoDecoder is AC17's core claim:
// an empty ffmpegPath must disable video frame extraction and degrade
// gracefully to ErrNoDecoder, never a panic and never a hang. A short,
// enforced context deadline is the hang guard: if PerceptualHash blocked
// trying to invoke a nonexistent subprocess, this test would time out and fail
// loudly rather than pass by accident.
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

// TestPerceptualHashVideoWithoutFFmpegDoesNotPanic runs the same case through
// recover(), because a hasher that panics on malformed video content would
// take down whichever goroutine ingests a posted attachment — the recovery
// interceptor in pkg/grpc/server exists for exactly this kind of boundary, but
// this pins the fingerprint package's own contract independently of it.
func TestPerceptualHashVideoWithoutFFmpegDoesNotPanic(t *testing.T) {
	hasher := NewHasher(DefaultGuards(), "")

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("PerceptualHash panicked: %v", r)
		}
	}()

	_, _ = hasher.PerceptualHash(context.Background(), []byte{0x00, 0x01, 0x02}, "video/mp4")
}

// ── Video hashing WITH ffmpeg, when available ────────────────────────────────

// generateTestVideo shells out to ffmpeg's own lavfi test source to produce a
// short, genuinely decodable MP4 with no network access and no committed
// binary fixture. It returns ("", false) for ANY failure — missing filter
// support, a sandboxed environment without a working encoder, etc. — because
// this helper backs an opportunistic test that must skip rather than fail when
// the environment cannot support it.
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

// TestPerceptualHashVideoWithFFmpegHashesTheFirstFrame is the positive control
// for AC17: when a real ffmpeg binary IS available, video content must
// actually be hashed rather than always falling back to ErrNoDecoder. It skips
// itself, rather than failing, in any environment where ffmpeg is absent or
// cannot produce the synthetic fixture — this is deliberately not run on every
// machine.
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
