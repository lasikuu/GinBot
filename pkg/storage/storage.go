// Package storage persists trigger media and other blobs behind an interface,
// so that local disk can later be swapped for object storage.
package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Storage persists trigger media and other blobs.
type Storage interface {
	// Put stores content and returns the storage path or key.
	Put(ctx context.Context, key string, r io.Reader) (string, error)
	// Get opens stored content for reading.
	Get(ctx context.Context, path string) (io.ReadCloser, error)
	// Delete removes stored content. Deleting a missing object is not an error.
	Delete(ctx context.Context, path string) error
}

// ErrInvalidKey is returned for a key that escapes the storage root.
var ErrInvalidKey = errors.New("invalid storage key")

// Local implements Storage on the local filesystem, rooted at a base directory.
type Local struct {
	base string
}

// NewLocal returns a Storage rooted at base, creating the directory if it is
// missing.
func NewLocal(base string) (*Local, error) {
	if err := os.MkdirAll(base, 0o755); err != nil {
		return nil, fmt.Errorf("create storage base %q: %w", base, err)
	}

	return &Local{base: filepath.Clean(base)}, nil
}

// resolve maps a key onto an absolute location under l.base plus the cleaned
// relative form. It filepath.Cleans first so a traversal like
// "a/../../etc/passwd" is visible rather than string-matched. The containment
// check is lexical only: it does not resolve symlinks. Safe today because keys
// are server-generated hashes; accepting a user-supplied key needs an
// filepath.EvalSymlinks check here first.
func (l *Local) resolve(p string) (full string, rel string, err error) {
	if p == "" || filepath.IsAbs(p) {
		return "", "", ErrInvalidKey
	}

	cleaned := filepath.Clean(p)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", "", ErrInvalidKey
	}

	return filepath.Join(l.base, cleaned), cleaned, nil
}

// Put stores content atomically: it writes to a temporary file in the
// destination directory and renames it into place, so a failed or concurrent
// write never leaves a half-written blob readable at the final path.
func (l *Local) Put(ctx context.Context, key string, r io.Reader) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}

	full, rel, err := l.resolve(key)
	if err != nil {
		return "", err
	}

	dir := filepath.Dir(full)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create storage directory: %w", err)
	}

	tmp, err := os.CreateTemp(dir, ".upload-*")
	if err != nil {
		return "", fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmp.Name()
	// Cleared once the rename succeeds; otherwise removes the half-written file.
	removeTmp := true
	defer func() {
		if removeTmp {
			_ = os.Remove(tmpPath)
		}
	}()

	if _, err := io.Copy(tmp, r); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("write blob: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("close temp file: %w", err)
	}

	if err := os.Rename(tmpPath, full); err != nil {
		return "", fmt.Errorf("rename blob into place: %w", err)
	}
	removeTmp = false

	return rel, nil
}

// Get opens stored content for reading. A missing path returns an error
// wrapping fs.ErrNotExist, via os.Open's own *PathError.
func (l *Local) Get(_ context.Context, path string) (io.ReadCloser, error) {
	full, _, err := l.resolve(path)
	if err != nil {
		return nil, err
	}

	f, err := os.Open(full)
	if err != nil {
		return nil, fmt.Errorf("open blob: %w", err)
	}

	return f, nil
}

// Delete removes stored content. A missing path is not an error.
func (l *Local) Delete(_ context.Context, path string) error {
	full, _, err := l.resolve(path)
	if err != nil {
		return err
	}

	if err := os.Remove(full); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("delete blob: %w", err)
	}

	return nil
}

var defaultStorage Storage

// Init sets the package-level Storage from a local directory: one process-wide
// instance configured at boot.
func Init(base string) error {
	local, err := NewLocal(base)
	if err != nil {
		return err
	}

	defaultStorage = local
	return nil
}

// Default returns the package-level Storage, or nil before Init.
func Default() Storage {
	return defaultStorage
}
