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

// maliciousKeys, once cleaned, escape the storage base; refusal must be uniform
// across Put, Get and Delete.
var maliciousKeys = []string{
	"../escape",
	"a/../../escape",
	"/etc/passwd",
	"..",
	".",
	"",
}

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

			escaped := filepath.Join(root, "escape")
			if _, statErr := os.Stat(escaped); !errors.Is(statErr, fs.ErrNotExist) {
				t.Errorf("Put(%q) created a file outside the storage base at %q", key, escaped)
			}
		})
	}
}

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

func TestDeleteMissingPathReturnsNil(t *testing.T) {
	base := t.TempDir()
	l := mustNewLocal(t, base)

	if err := l.Delete(context.Background(), "never/written"); err != nil {
		t.Errorf("Delete on a missing path = %v, want nil", err)
	}
}

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

	if _, err := l.Get(ctx, path); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Get after Delete err = %v, want fs.ErrNotExist", err)
	}
}

// failingReader returns some bytes, then errs on the next Read.
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
// atomicity: a mid-read failure leaves no readable final path and no temp file.
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

// TestDefaultBeforeAndAfterInit is the sole caller of Init in this binary:
// Init sets package-level state and Go does not order tests, so before/after
// are asserted together to avoid a run-order flake.
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

	path, err := got.Put(context.Background(), "smoke-test", bytes.NewReader([]byte("ok")))
	if err != nil {
		t.Fatalf("Put through Default(): %v", err)
	}
	if _, err := os.Stat(filepath.Join(base, path)); err != nil {
		t.Errorf("Default()'s store did not persist under the configured base: %v", err)
	}
}
