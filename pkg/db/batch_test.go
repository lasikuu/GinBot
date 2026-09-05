package db

import (
	"context"
	"testing"
)

// TestGetTriggerInstancesBatchSkipsTheQueryOnEmptyInput: dbpool is never
// initialised in this (non-integration) test binary, so a query attempt would
// panic on a nil *pgxpool.Pool. Returning cleanly here is the proof that empty
// input never reaches db().
func TestGetTriggerInstancesBatchSkipsTheQueryOnEmptyInput(t *testing.T) {
	for _, ids := range [][]string{nil, {}} {
		got, err := GetTriggerInstancesBatch(context.Background(), ids)
		if err != nil {
			t.Fatalf("GetTriggerInstancesBatch(%v): %v", ids, err)
		}
		if len(got) != 0 {
			t.Errorf("GetTriggerInstancesBatch(%v) = %v, want empty", ids, got)
		}
	}
}

// TestGetFilesByIDsSkipsTheQueryOnEmptyInput mirrors the above for the file
// batch helper.
func TestGetFilesByIDsSkipsTheQueryOnEmptyInput(t *testing.T) {
	for _, ids := range [][]string{nil, {}} {
		got, err := GetFilesByIDs(context.Background(), ids)
		if err != nil {
			t.Fatalf("GetFilesByIDs(%v): %v", ids, err)
		}
		if len(got) != 0 {
			t.Errorf("GetFilesByIDs(%v) = %v, want empty", ids, got)
		}
	}
}
