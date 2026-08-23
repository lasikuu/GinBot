package storage

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func mustNewLocal(t *testing.T, base string) *Local {
	t.Helper()
	l, err := NewLocal(base)
	if err != nil {
		t.Fatalf("NewLocal(%q): %v", base, err)
	}
	return l
}

// TestNewLocalCreatesMissingBaseDirectory.
func TestNewLocalCreatesMissingBaseDirectory(t *testing.T) {
	root := t.TempDir()
	base := filepath.Join(root, "does", "not", "exist", "yet")

	if _, err := os.Stat(base); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("precondition failed: %q already exists", base)
	}

	mustNewLocal(t, base)

	info, err := os.Stat(base)
	if err != nil {
		t.Fatalf("NewLocal did not create the base directory: %v", err)
	}
	if !info.IsDir() {
		t.Errorf("%q exists but is not a directory", base)
	}
}

// TestPutGetRoundTrip: the returned path is relative to the base, Get returns
// the same bytes Put stored, and the blob actually exists on disk under base.
func TestPutGetRoundTrip(t *testing.T) {
	base := t.TempDir()
	l := mustNewLocal(t, base)
	ctx := context.Background()

	content := []byte("trigger media bytes")
	path, err := l.Put(ctx, "trigger/ab/abcdef0123456789", bytes.NewReader(content))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	if filepath.IsAbs(path) {
		t.Errorf("Put returned an absolute path %q, want relative to base", path)
	}

	onDisk := filepath.Join(base, path)
	if _, err := os.Stat(onDisk); err != nil {
		t.Fatalf("blob does not exist on disk at %q: %v", onDisk, err)
	}

	rc, err := l.Get(ctx, path)
	if err != nil {
		t.Fatalf("Get(%q): %v", path, err)
	}
	defer func() {
		if err := rc.Close(); err != nil {
			t.Errorf("close reader: %v", err)
		}
	}()

	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Errorf("Get returned %q, want %q", got, content)
	}
}

// TestPutCreatesIntermediateDirectories: the trigger media key layout
// "trigger/<hash[0:2]>/<hash>" needs Put to create the fan-out directory.
func TestPutCreatesIntermediateDirectories(t *testing.T) {
	base := t.TempDir()
	l := mustNewLocal(t, base)
	ctx := context.Background()

	const hash = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcd"
	key := "trigger/" + hash[0:2] + "/" + hash

	path, err := l.Put(ctx, key, bytes.NewReader([]byte("data")))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	fanoutDir := filepath.Join(base, "trigger", hash[0:2])
	info, err := os.Stat(fanoutDir)
	if err != nil {
		t.Fatalf("fan-out directory %q was not created: %v", fanoutDir, err)
	}
	if !info.IsDir() {
		t.Errorf("%q exists but is not a directory", fanoutDir)
	}

	if _, err := os.Stat(filepath.Join(base, path)); err != nil {
		t.Errorf("stored blob missing at returned path %q: %v", path, err)
	}
}

// maliciousKeys are keys/paths that, once cleaned, escape the storage base.
// Shared between Put, Get and Delete: the refusal must be uniform across all
// three entry points.
var maliciousKeys = []string{
	"../escape",
	"a/../../escape",
	"/etc/passwd",
	"..",
	".",
	"",
}

// TestPathTraversalRefusedOnPut.
func TestPathTraversalRefusedOnPut(t *testing.T) {
	root := t.TempDir()
	base := filepath.Join(root, "storage")
	l := mustNewLocal(t, base)
	ctx := context.Background()

	for _, key := range maliciousKeys {
		t.Run(key, func(t *testing.T) {
			_, err := l.Put(ctx, key, bytes.NewReader([]byte("payload")))
			if !errors.Is(err, ErrInvalidKey) {
				t.Fatalf("Put(%q) err = %v, want ErrInvalidKey", key, err)
			}

			// No file must exist outside the storage base as a result.
			escaped := filepath.Join(root, "escape")
			if _, statErr := os.Stat(escaped); !errors.Is(statErr, fs.ErrNotExist) {
				t.Errorf("Put(%q) created a file outside the storage base at %q", key, escaped)
			}
		})
	}
}

// TestPathTraversalRefusedOnGet.
func TestPathTraversalRefusedOnGet(t *testing.T) {
	base := t.TempDir()
	l := mustNewLocal(t, base)
	ctx := context.Background()

	for _, key := range maliciousKeys {
		t.Run(key, func(t *testing.T) {
			if _, err := l.Get(ctx, key); !errors.Is(err, ErrInvalidKey) {
				t.Errorf("Get(%q) err = %v, want ErrInvalidKey", key, err)
			}
		})
	}
}

// TestPathTraversalRefusedOnDelete.
func TestPathTraversalRefusedOnDelete(t *testing.T) {
	base := t.TempDir()
	l := mustNewLocal(t, base)
	ctx := context.Background()

	for _, key := range maliciousKeys {
		t.Run(key, func(t *testing.T) {
			if err := l.Delete(ctx, key); !errors.Is(err, ErrInvalidKey) {
				t.Errorf("Delete(%q) err = %v, want ErrInvalidKey", key, err)
			}
		})
	}
}

// TestGetMissingPathWrapsFsErrNotExist.
func TestGetMissingPathWrapsFsErrNotExist(t *testing.T) {
	base := t.TempDir()
	l := mustNewLocal(t, base)

	_, err := l.Get(context.Background(), "never/written")
	if err == nil {
		t.Fatal("Get on a missing path returned nil error")
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Get on a missing path err = %v, want it to satisfy errors.Is(err, fs.ErrNotExist)", err)
	}
}

// TestDeleteMissingPathReturnsNil.
func TestDeleteMissingPathReturnsNil(t *testing.T) {
	base := t.TempDir()
	l := mustNewLocal(t, base)

	if err := l.Delete(context.Background(), "never/written"); err != nil {
		t.Errorf("Delete on a missing path = %v, want nil", err)
	}
}

// TestDeleteExistingPathRemovesIt.
func TestDeleteExistingPathRemovesIt(t *testing.T) {
	base := t.TempDir()
	l := mustNewLocal(t, base)
	ctx := context.Background()

	path, err := l.Put(ctx, "to-delete", bytes.NewReader([]byte("gone soon")))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	if err := l.Delete(ctx, path); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	if _, err := os.Stat(filepath.Join(base, path)); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("blob still exists on disk after Delete: stat err = %v", err)
	}

	// And it now reads back as not found, consistently with a path that was
	// never written.
	if _, err := l.Get(ctx, path); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Get after Delete err = %v, want fs.ErrNotExist", err)
	}
}

// failingReader returns some bytes successfully, then errs on the next Read.
// Used to simulate a reader that fails partway through a Put.
type failingReader struct {
	data []byte
	err  error
	sent bool
}

func (r *failingReader) Read(p []byte) (int, error) {
	if !r.sent {
		r.sent = true
		return copy(p, r.data), nil
	}
	return 0, r.err
}

// TestPutOnReadFailureLeavesNothingReadableAndNoTempFile asserts Put's
// documented atomicity: when the source reader fails partway through, (a) the
// final path is not readable, and (b) no leftover temporary file remains in
// the destination directory.
func TestPutOnReadFailureLeavesNothingReadableAndNoTempFile(t *testing.T) {
	base := t.TempDir()
	l := mustNewLocal(t, base)
	ctx := context.Background()

	key := "trigger/pp/partial-write"
	readErr := errors.New("boom: simulated read failure")
	r := &failingReader{data: []byte("partial content"), err: readErr}

	_, err := l.Put(ctx, key, r)
	if err == nil {
		t.Fatal("Put with a failing reader returned a nil error")
	}
	if !errors.Is(err, readErr) {
		t.Errorf("Put err = %v, want it to wrap %v", err, readErr)
	}

	final := filepath.Join(base, key)
	if _, statErr := os.Stat(final); !errors.Is(statErr, fs.ErrNotExist) {
		t.Errorf("final path %q is readable after a failed Put (stat err = %v)", final, statErr)
	}

	destDir := filepath.Dir(final)
	entries, err := os.ReadDir(destDir)
	if err != nil {
		t.Fatalf("read destination directory %q: %v", destDir, err)
	}
	for _, entry := range entries {
		t.Errorf("leftover file %q in destination directory %q after a failed Put", entry.Name(), destDir)
	}
}

// TestDefaultBeforeAndAfterInit: Default() is nil before Init, and the
// configured store afterwards.
//
// storage.Init sets PACKAGE-LEVEL state, so this is written as a single test
// that both makes the "before" assertion and performs the only call to Init in
// this test binary, rather than as two separate test functions ordered by
// convention — Go does not guarantee test execution order across files, and a
// second test elsewhere calling Init would make a separate "before" test
// flaky depending on run order. If any other test in this package ever needs
// to call Init, this test must be revisited; as written, it is the sole
// caller, so the ordering hazard cannot arise.
func TestDefaultBeforeAndAfterInit(t *testing.T) {
	if got := Default(); got != nil {
		t.Fatalf("Default() before Init = %v, want nil", got)
	}

	base := t.TempDir()
	if err := Init(base); err != nil {
		t.Fatalf("Init: %v", err)
	}

	got := Default()
	if got == nil {
		t.Fatal("Default() after Init = nil, want the configured store")
	}

	// The configured store must actually work, not just be non-nil.
	path, err := got.Put(context.Background(), "smoke-test", bytes.NewReader([]byte("ok")))
	if err != nil {
		t.Fatalf("Put through Default(): %v", err)
	}
	if _, err := os.Stat(filepath.Join(base, path)); err != nil {
		t.Errorf("Default()'s store did not persist under the configured base: %v", err)
	}
}
