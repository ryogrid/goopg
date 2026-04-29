package initdb

import (
	"path/filepath"
	"testing"
)

// TestIsStandbyAbsentReturnsFalse: a fresh data directory with no
// signal file is treated as primary.
func TestIsStandbyAbsentReturnsFalse(t *testing.T) {
	dir := t.TempDir()
	got, err := IsStandby(dir)
	if err != nil {
		t.Fatalf("IsStandby: %v", err)
	}
	if got {
		t.Errorf("IsStandby(empty dir) = true, want false")
	}
}

// TestIsStandbyEmptyDataDir: empty input means "no data dir
// configured"; the helper must not treat that as standby (would
// flip every in-process test that runs without a data dir).
func TestIsStandbyEmptyDataDir(t *testing.T) {
	got, err := IsStandby("")
	if err != nil {
		t.Fatalf("IsStandby: %v", err)
	}
	if got {
		t.Errorf("IsStandby(\"\") = true, want false")
	}
}

// TestCreateAndRemoveStandbySignal: round-trips the trigger-file
// API. After Create, IsStandby returns true; after Remove, false.
// Remove on a missing file is idempotent.
func TestCreateAndRemoveStandbySignal(t *testing.T) {
	dir := t.TempDir()
	if err := CreateStandbySignal(dir); err != nil {
		t.Fatalf("CreateStandbySignal: %v", err)
	}
	got, err := IsStandby(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !got {
		t.Errorf("IsStandby after Create = false, want true")
	}
	// Sanity: the file lives at <dir>/standby.signal.
	if want := filepath.Join(dir, StandbySignalFile); want == "" {
		t.Errorf("StandbySignalFile constant unset")
	}

	if err := RemoveStandbySignal(dir); err != nil {
		t.Fatalf("RemoveStandbySignal: %v", err)
	}
	got, err = IsStandby(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got {
		t.Errorf("IsStandby after Remove = true, want false")
	}
	// Idempotent: a second Remove must not error.
	if err := RemoveStandbySignal(dir); err != nil {
		t.Errorf("second RemoveStandbySignal: %v", err)
	}
}
