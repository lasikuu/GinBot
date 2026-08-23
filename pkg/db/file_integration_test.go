//go:build integration

// Integration tests for pkg/db/file.go beyond the dedupe and orphan-listing
// behaviour already covered by TestGetOrCreateFileByHashDedupesByHash and
// TestListOrphanFilesExcludesFilesReferencedByALiveTrigger in
// trigger_integration_test.go.
package db

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestGetFileReturnsTheStoredRowAndErrNotFoundOnceDeleted.
func TestGetFileReturnsTheStoredRowAndErrNotFoundOnceDeleted(t *testing.T) {
	ctx := context.Background()
	hash := "getfile-hash-" + time.Now().Format("150405.000000000")

	id, inserted, err := GetOrCreateFileByHash(ctx, hash, "trigger/gf/"+hash, "image/gif", 555)
	if err != nil {
		t.Fatalf("GetOrCreateFileByHash: %v", err)
	}
	if !inserted {
		t.Fatal("precondition failed: file already existed")
	}
	t.Cleanup(func() {
		if _, err := db().Exec(context.Background(), `DELETE FROM file WHERE id = $1`, id); err != nil {
			t.Errorf("cleanup file %s: %v", id, err)
		}
	})

	row, err := GetFile(ctx, id)
	if err != nil {
		t.Fatalf("GetFile: %v", err)
	}
	if row.MimeType != "image/gif" {
		t.Errorf("mime_type = %q, want image/gif", row.MimeType)
	}
	if row.ByteSize != 555 {
		t.Errorf("byte_size = %d, want 555", row.ByteSize)
	}
	if row.FileHash != hash {
		t.Errorf("file_hash = %q, want %q", row.FileHash, hash)
	}
	if row.Category != FileCategoryLocal {
		t.Errorf("category = %d, want %d (FileCategoryLocal)", row.Category, FileCategoryLocal)
	}

	if err := SoftDeleteFile(ctx, id); err != nil {
		t.Fatalf("SoftDeleteFile: %v", err)
	}

	if _, err := GetFile(ctx, id); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetFile after SoftDeleteFile err = %v, want ErrNotFound", err)
	}
}

// TestSoftDeleteFileOnAMissingRowReturnsErrNotFound.
func TestSoftDeleteFileOnAMissingRowReturnsErrNotFound(t *testing.T) {
	err := SoftDeleteFile(context.Background(), "018f0000-0000-7000-8000-0000000000ff")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("SoftDeleteFile on a missing row err = %v, want ErrNotFound", err)
	}
}

// TestFileVisibleToCallerScopesByOwnerOrInstance: a file is visible when the
// caller created the referencing trigger, OR when the caller's origin
// instance is one the referencing trigger is scoped to — and invisible to
// neither.
func TestFileVisibleToCallerScopesByOwnerOrInstance(t *testing.T) {
	owner := newTriggerFixture(t, "visible-owner")
	stranger := newTriggerFixture(t, "visible-stranger")
	ctx := context.Background()

	hash := "visible-hash-" + owner.suffix
	fileID, _, err := GetOrCreateFileByHash(ctx, hash, "trigger/vv/"+hash, "image/png", 10)
	if err != nil {
		t.Fatalf("GetOrCreateFileByHash: %v", err)
	}
	t.Cleanup(func() {
		if _, err := db().Exec(context.Background(), `DELETE FROM file WHERE id = $1`, fileID); err != nil {
			t.Errorf("cleanup file %s: %v", fileID, err)
		}
	})

	owner.createTriggerWithFile(t, "visible-trigger-"+owner.suffix, fileID)

	// The creator can see it, from anywhere (instanceID 0 = no origin instance).
	visibleToOwner, err := FileVisibleToCaller(ctx, fileID, owner.userID, 0)
	if err != nil {
		t.Fatalf("FileVisibleToCaller(owner): %v", err)
	}
	if !visibleToOwner {
		t.Error("file not visible to its creator")
	}

	// A caller on the SAME instance, even if they didn't create it, can see it.
	visibleOnInstance, err := FileVisibleToCaller(ctx, fileID, stranger.userID, owner.instanceID)
	if err != nil {
		t.Fatalf("FileVisibleToCaller(stranger, owner's instance): %v", err)
	}
	if !visibleOnInstance {
		t.Error("file not visible to a caller on the instance the trigger is scoped to")
	}

	// Neither the creator nor scoped to the trigger's instance: invisible.
	visibleToStranger, err := FileVisibleToCaller(ctx, fileID, stranger.userID, stranger.instanceID)
	if err != nil {
		t.Fatalf("FileVisibleToCaller(stranger, stranger's own instance): %v", err)
	}
	if visibleToStranger {
		t.Error("file incorrectly visible to a caller who neither created nor shares an instance with the referencing trigger")
	}
}
