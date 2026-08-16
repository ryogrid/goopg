package transam

import "testing"

// CheckTableForSerializableConflictIn is the relation-wide conflict-in path
// used by REFRESH MATERIALIZED VIEW / TRUNCATE / DROP. Unlike the per-tuple
// CheckForSerializableConflictIn (which walks UPWARD from a tuple/page tag to
// its covering ancestors), it must fire for a holder of ANY granularity on the
// relation — most importantly a FINE-GRAINED (tuple/page) SIREAD that the
// upward walk would never reach. These tests pin that contract (M0118-0001,
// matview-write-skew spec).

// TestCheckTableForSerializableConflictIn_FiresOnTupleLevelHolder is the core
// guarantee: a reader holding only a tuple-level SIREAD must receive the
// rw-edge from a relation-wide rewrite. Contrast
// TestCheckForSerializableConflictIn_NoOpForFinerDescendantHolder, where the
// upward per-tuple walk deliberately misses a descendant holder.
func TestCheckTableForSerializableConflictIn_FiresOnTupleLevelHolder(t *testing.T) {
	m := NewManager()
	reader := beginAndAssign(t, m)
	writer := beginAndAssign(t, m)
	if !m.AcquirePredicateLock(reader.Handle, TupleLockTag(1, 16384, 9, 5)) {
		t.Fatal("AcquirePredicateLock(tuple) returned false")
	}
	if !m.CheckTableForSerializableConflictIn(writer.Handle, 1, 16384) {
		t.Fatal("CheckTableForSerializableConflictIn returned false; expected tuple-level holder to fire")
	}
	if !m.HasRWConflict(reader.Handle, writer.Handle) {
		t.Fatal("HasRWConflict(reader→writer) = false, want true")
	}
}

// TestCheckTableForSerializableConflictIn_FiresOnPageAndRelationHolders confirms
// page-level and relation-level SIREAD holders are also caught.
func TestCheckTableForSerializableConflictIn_FiresOnPageAndRelationHolders(t *testing.T) {
	for _, tc := range []struct {
		name string
		tag  PredicateLockTag
	}{
		{"page", PageLockTag(1, 16384, 9)},
		{"relation", RelationLockTag(1, 16384)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := NewManager()
			reader := beginAndAssign(t, m)
			writer := beginAndAssign(t, m)
			if !m.AcquirePredicateLock(reader.Handle, tc.tag) {
				t.Fatalf("AcquirePredicateLock(%s) returned false", tc.name)
			}
			if !m.CheckTableForSerializableConflictIn(writer.Handle, 1, 16384) {
				t.Fatalf("CheckTableForSerializableConflictIn returned false for %s holder", tc.name)
			}
			if !m.HasRWConflict(reader.Handle, writer.Handle) {
				t.Fatalf("HasRWConflict(reader→writer) = false for %s holder, want true", tc.name)
			}
		})
	}
}

// TestCheckTableForSerializableConflictIn_NoOpForDifferentRelation confirms a
// reader on a different relation never receives the edge.
func TestCheckTableForSerializableConflictIn_NoOpForDifferentRelation(t *testing.T) {
	m := NewManager()
	reader := beginAndAssign(t, m)
	writer := beginAndAssign(t, m)
	if !m.AcquirePredicateLock(reader.Handle, TupleLockTag(1, 16385, 9, 5)) {
		t.Fatal("AcquirePredicateLock returned false")
	}
	if m.CheckTableForSerializableConflictIn(writer.Handle, 1, 16384) {
		t.Fatal("conflict registered against a reader on a different relation; expected no-op")
	}
	if got := m.InConflictCount(writer.Handle); got != 0 {
		t.Fatalf("writer.inConflicts = %d, want 0", got)
	}
}

// TestCheckTableForSerializableConflictIn_NoOpForSelfHolder confirms a writer
// that also read the relation (REFRESH first runs the matview's defining query)
// does not conflict with itself.
func TestCheckTableForSerializableConflictIn_NoOpForSelfHolder(t *testing.T) {
	m := NewManager()
	writer := beginAndAssign(t, m)
	if !m.AcquirePredicateLock(writer.Handle, TupleLockTag(1, 16384, 9, 5)) {
		t.Fatal("AcquirePredicateLock returned false")
	}
	if m.CheckTableForSerializableConflictIn(writer.Handle, 1, 16384) {
		t.Fatal("self-conflict registered; expected no-op")
	}
	if got := m.InConflictCount(writer.Handle); got != 0 {
		t.Fatalf("writer.inConflicts = %d, want 0", got)
	}
}

// TestCheckTableForSerializableConflictIn_MultipleReadersDistinctEdges confirms
// one relation-wide write fans the edge out to every reader regardless of the
// granularity each holds.
func TestCheckTableForSerializableConflictIn_MultipleReadersDistinctEdges(t *testing.T) {
	m := NewManager()
	r1 := beginAndAssign(t, m)
	r2 := beginAndAssign(t, m)
	r3 := beginAndAssign(t, m)
	writer := beginAndAssign(t, m)
	if !m.AcquirePredicateLock(r1.Handle, TupleLockTag(1, 16384, 9, 5)) {
		t.Fatal("AcquirePredicateLock(r1) returned false")
	}
	if !m.AcquirePredicateLock(r2.Handle, PageLockTag(1, 16384, 12)) {
		t.Fatal("AcquirePredicateLock(r2) returned false")
	}
	if !m.AcquirePredicateLock(r3.Handle, RelationLockTag(1, 16384)) {
		t.Fatal("AcquirePredicateLock(r3) returned false")
	}
	if !m.CheckTableForSerializableConflictIn(writer.Handle, 1, 16384) {
		t.Fatal("CheckTableForSerializableConflictIn returned false; expected three new edges")
	}
	if got := m.InConflictCount(writer.Handle); got != 3 {
		t.Fatalf("writer.inConflicts = %d, want 3", got)
	}
	for _, r := range []Transaction{r1, r2, r3} {
		if !m.HasRWConflict(r.Handle, writer.Handle) {
			t.Fatalf("HasRWConflict(reader handle %d → writer) = false, want true", r.Handle)
		}
	}
}

// TestCheckTableForSerializableConflictInReportingFailure_DeferredPivotReturnsNil
// mirrors the spec's commit ordering: while the conflicting reader is still
// in-flight, the refreshing writer is a deferred pivot — the ReportingFailure
// variant returns nil (no mid-statement abort) but still installs the edge, so
// the failure surfaces at the reader's later COMMIT.
func TestCheckTableForSerializableConflictInReportingFailure_DeferredPivotReturnsNil(t *testing.T) {
	m := NewManager()
	reader := beginAndAssign(t, m)
	writer := beginAndAssign(t, m)
	if !m.AcquirePredicateLock(reader.Handle, TupleLockTag(1, 16384, 9, 5)) {
		t.Fatal("AcquirePredicateLock returned false")
	}
	if err := m.CheckTableForSerializableConflictInReportingFailure(writer.Handle, 1, 16384); err != nil {
		t.Fatalf("ReportingFailure returned %v; want nil (partner still in flight)", err)
	}
	if !m.HasRWConflict(reader.Handle, writer.Handle) {
		t.Fatal("edge not installed by ReportingFailure variant")
	}
}

// TestCheckTableForSerializableConflictIn_EdgeToRetainedCommittedReader confirms
// the second walk: a reader that predicate-locked the relation and then
// committed while the writer is still in-flight is retained in ssiState.finished
// and still receives the rw-edge.
func TestCheckTableForSerializableConflictIn_EdgeToRetainedCommittedReader(t *testing.T) {
	m := NewManager()
	reader := beginAndAssign(t, m)
	writer := beginAndAssign(t, m)
	if !m.AcquirePredicateLock(reader.Handle, TupleLockTag(1, 16384, 9, 5)) {
		t.Fatal("AcquirePredicateLock returned false")
	}
	// Commit the reader BEFORE the relation-wide write; it overlaps the
	// still-active writer, so it is retained and must still be found.
	if err := m.Commit(reader); err != nil {
		t.Fatalf("Commit reader: %v", err)
	}
	if !m.CheckTableForSerializableConflictIn(writer.Handle, 1, 16384) {
		t.Fatal("CheckTableForSerializableConflictIn returned false; expected retained committed reader to fire")
	}
	if got := m.InConflictCount(writer.Handle); got != 1 {
		t.Fatalf("writer.inConflicts = %d, want 1", got)
	}
}

// TestCheckTableForSerializableConflictIn_NoOpForUnknownWriter and empty-state
// guards: a missing writer handle or an empty registry is a silent no-op.
func TestCheckTableForSerializableConflictIn_NoOpForUnknownWriter(t *testing.T) {
	m := NewManager()
	reader := beginAndAssign(t, m)
	if !m.AcquirePredicateLock(reader.Handle, TupleLockTag(1, 16384, 9, 5)) {
		t.Fatal("AcquirePredicateLock returned false")
	}
	if m.CheckTableForSerializableConflictIn(TxnHandle(99999), 1, 16384) {
		t.Fatal("conflict registered for an unknown writer handle; expected no-op")
	}
}
