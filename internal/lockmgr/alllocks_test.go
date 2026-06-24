package lockmgr

import (
	"context"
	"testing"
	"time"
)

// findHolding looks up a single (tag, backend, mode) entry in an AllLocks
// result and reports whether it was found and its granted flag.
func findHolding(all []LockHolding, tag LockTag, b BackendID, m Mode) (granted, found bool) {
	for _, h := range all {
		if h.Tag == tag && h.Backend == b && h.Mode == m {
			return h.Granted, true
		}
	}
	return false, false
}

// TestAllLocksEmpty: a fresh manager enumerates nothing.
func TestAllLocksEmpty(t *testing.T) {
	lm := New()
	if got := lm.AllLocks(); len(got) != 0 {
		t.Errorf("AllLocks on empty manager = %v, want empty", got)
	}
}

// TestAllLocksGrantedHolders: holders across multiple tags and backends
// surface as Granted=true, one row per (backend, tag, mode).
func TestAllLocksGrantedHolders(t *testing.T) {
	lm := New()
	tagA := LockTag{DB: 1, Rel: 100}
	tagB := LockTag{DB: 1, Rel: 200}
	ctx := context.Background()
	if err := lm.Acquire(ctx, 1, tagA, AccessExclusiveLock); err != nil {
		t.Fatal(err)
	}
	if err := lm.Acquire(ctx, 2, tagB, AccessShareLock); err != nil {
		t.Fatal(err)
	}
	if err := lm.Acquire(ctx, 3, tagB, AccessShareLock); err != nil {
		t.Fatal(err)
	}

	all := lm.AllLocks()
	if len(all) != 3 {
		t.Fatalf("AllLocks = %d entries, want 3: %+v", len(all), all)
	}
	for _, c := range []struct {
		tag LockTag
		b   BackendID
		m   Mode
	}{
		{tagA, 1, AccessExclusiveLock},
		{tagB, 2, AccessShareLock},
		{tagB, 3, AccessShareLock},
	} {
		granted, found := findHolding(all, c.tag, c.b, c.m)
		if !found {
			t.Errorf("missing holding tag=%v backend=%d mode=%s", c.tag, c.b, c.m)
			continue
		}
		if !granted {
			t.Errorf("holding tag=%v backend=%d mode=%s: Granted=false, want true", c.tag, c.b, c.m)
		}
	}
}

// TestAllLocksMultiModeMaskExpanded: a backend that holds several
// self-compatible modes on one tag yields one entry per mode bit, matching
// upstream's one-LOCK-per-mode accounting in pg_locks.
func TestAllLocksMultiModeMaskExpanded(t *testing.T) {
	lm := New()
	tag := LockTag{DB: 1, Rel: 100}
	ctx := context.Background()
	// A single backend can hold multiple self-compatible modes (e.g. the
	// executor's nested-statement case: RowExclusive then AccessShare).
	if err := lm.Acquire(ctx, 1, tag, RowExclusiveLock); err != nil {
		t.Fatal(err)
	}
	if err := lm.Acquire(ctx, 1, tag, AccessShareLock); err != nil {
		t.Fatal(err)
	}

	all := lm.AllLocks()
	seen := map[Mode]bool{}
	for _, h := range all {
		if h.Backend != 1 || h.Tag != tag {
			t.Errorf("unexpected entry %+v", h)
			continue
		}
		if !h.Granted {
			t.Errorf("entry %+v: Granted=false, want true", h)
		}
		seen[h.Mode] = true
	}
	if len(all) != 2 || !seen[AccessShareLock] || !seen[RowExclusiveLock] {
		t.Errorf("modes seen = %v (%d entries), want {AccessShareLock, RowExclusiveLock}", seen, len(all))
	}
}

// TestAllLocksWaiterIsNotGranted: a backend blocked in the wait queue
// surfaces as Granted=false alongside the conflicting granted holder.
func TestAllLocksWaiterIsNotGranted(t *testing.T) {
	lm := New()
	tag := LockTag{DB: 1, Rel: 100}
	ctx := context.Background()
	if err := lm.Acquire(ctx, 1, tag, AccessExclusiveLock); err != nil {
		t.Fatal(err)
	}
	go func() { _ = lm.Acquire(ctx, 2, tag, AccessShareLock) }()
	// Wait until backend 2 is parked.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if len(lm.Waiters(tag)) == 1 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if len(lm.Waiters(tag)) != 1 {
		t.Fatal("backend 2 never queued as waiter")
	}

	all := lm.AllLocks()
	if granted, found := findHolding(all, tag, 1, AccessExclusiveLock); !found || !granted {
		t.Errorf("holder backend 1: found=%v granted=%v, want true/true", found, granted)
	}
	if granted, found := findHolding(all, tag, 2, AccessShareLock); !found || granted {
		t.Errorf("waiter backend 2: found=%v granted=%v, want true/false", found, granted)
	}

	// Cleanup: release so the waiter goroutine completes.
	lm.Release(1, tag, AccessExclusiveLock)
}

// TestAllLocksIncludesTupleTags: tuple-level tags (Block/Offset non-zero)
// are enumerated too; callers select relation locks by filtering on
// Block==0 && Offset==0.
func TestAllLocksIncludesTupleTags(t *testing.T) {
	lm := New()
	relTag := LockTag{DB: 1, Rel: 100}
	tupleTag := LockTag{DB: 1, Rel: 100, Block: 5, Offset: 3}
	ctx := context.Background()
	if err := lm.Acquire(ctx, 1, relTag, AccessShareLock); err != nil {
		t.Fatal(err)
	}
	if err := lm.Acquire(ctx, 1, tupleTag, ExclusiveLock); err != nil {
		t.Fatal(err)
	}

	all := lm.AllLocks()
	var relCount, tupleCount int
	for _, h := range all {
		if h.Tag.Block == 0 && h.Tag.Offset == 0 {
			relCount++
		} else {
			tupleCount++
		}
	}
	if relCount != 1 || tupleCount != 1 {
		t.Errorf("relCount=%d tupleCount=%d, want 1/1 (%+v)", relCount, tupleCount, all)
	}
}
