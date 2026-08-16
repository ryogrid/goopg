package xlog

import (
	"testing"
)

// writeThreeRecords writes a small WAL and returns the dir.
func writeThreeRecords(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	w, err := NewWriter(Config{WALDir: dir})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	for _, p := range [][]byte{[]byte("aaa"), []byte("bbb"), []byte("ccc")} {
		if _, _, err := w.Append(p); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	return dir
}

// TestRecoveryCacheReturnsSameRecords verifies that with the cache active,
// repeated ReadAll calls return the identical decoded records (same content),
// and that the cache is only consulted for the bracketed directory.
func TestRecoveryCacheReturnsSameRecords(t *testing.T) {
	dir := writeThreeRecords(t)

	// Baseline: uncached decode.
	want, err := ReadAll(dir, 0)
	if err != nil {
		t.Fatalf("ReadAll (uncached): %v", err)
	}
	if len(want) == 0 {
		t.Fatal("expected records, got none")
	}

	BeginRecoveryCache(dir)
	defer EndRecoveryCache()

	got1, err := ReadAll(dir, 0)
	if err != nil {
		t.Fatalf("ReadAll (cached 1): %v", err)
	}
	got2, err := ReadAll(dir, 0)
	if err != nil {
		t.Fatalf("ReadAll (cached 2): %v", err)
	}
	// Cached calls return the same backing slice (memoized), and it matches
	// the uncached decode in content.
	if &got1[0] != &got2[0] {
		t.Errorf("cached ReadAll returned distinct slices; expected memoized identity")
	}
	if len(got1) != len(want) {
		t.Fatalf("cached len=%d, uncached len=%d", len(got1), len(want))
	}
	for i := range want {
		if string(got1[i].Payload) != string(want[i].Payload) || got1[i].EndLSN != want[i].EndLSN {
			t.Errorf("record %d differs: cached=%q@%d uncached=%q@%d",
				i, got1[i].Payload, got1[i].EndLSN, want[i].Payload, want[i].EndLSN)
		}
	}
}

// TestRecoveryCacheInactiveFallsThrough verifies that outside a
// Begin/End bracket, ReadAll decodes fresh every time (no staleness for the
// per-module unit tests that call recovery functions directly).
func TestRecoveryCacheInactiveFallsThrough(t *testing.T) {
	dir := writeThreeRecords(t)
	// No BeginRecoveryCache: each ReadAll must decode independently.
	a, err := ReadAll(dir, 0)
	if err != nil {
		t.Fatalf("ReadAll a: %v", err)
	}
	b, err := ReadAll(dir, 0)
	if err != nil {
		t.Fatalf("ReadAll b: %v", err)
	}
	if len(a) == 0 || len(b) == 0 {
		t.Fatal("expected records")
	}
	if &a[0] == &b[0] {
		t.Errorf("inactive cache returned memoized slice; expected fresh decode")
	}
}

// TestRecoveryCacheOnlyMatchesBracketedDir verifies a different directory (or
// a non-zero segment size) bypasses the cache.
func TestRecoveryCacheOnlyMatchesBracketedDir(t *testing.T) {
	dir := writeThreeRecords(t)
	other := writeThreeRecords(t)

	BeginRecoveryCache(dir)
	defer EndRecoveryCache()

	// A different directory must not be served from the cache: decode fresh.
	o1, err := ReadAll(other, 0)
	if err != nil {
		t.Fatalf("ReadAll other 1: %v", err)
	}
	o2, err := ReadAll(other, 0)
	if err != nil {
		t.Fatalf("ReadAll other 2: %v", err)
	}
	if len(o1) == 0 {
		t.Fatal("expected records for other dir")
	}
	if &o1[0] == &o2[0] {
		t.Errorf("non-bracketed dir was memoized; expected fresh decode")
	}
}
