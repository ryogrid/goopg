package executor

// EX3-02 Cut 1 (stratum B only) poison pins.
//
// The build-loop retention call routes variable-width payloads into a
// per-joinOp stratum-B arena instead of per-Datum make/Perm allocations.
// These tests pin the behaviour contract, not the win:
//   - arena-reset survival (M0097-0058 replay over re-homed strings/bytes/
//     big-numerics);
//   - Perm-independence on the Q8 shape (big-numeric build rows land in the
//     build arena, never in mmgr.Perm());
//   - RIGHT/FULL sweep over re-homed strings (fillNullBuild + bucket sweep
//     read chunk-backed rows after the producer arena is gone);
//   - cloneRowOwned still satisfies AssertTransferable (the cross-goroutine
//     contract Cut 1 deliberately does not touch);
//   - value equivalence new-path vs legacy ownedBuildRow, including the
//     Buf-carrying passthrough (KindEnum/KindToastPointer bit-identical);
//   - shared-build ownership (strata parented to the statement context;
//     Close releases the serial arena, only dereferences an adopted one).

import (
	"math/big"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/utils/mmgr"
)

// cut1ArenaString builds an arena-backed KindString Datum in prod, the
// producer-arena shape the scan decode path hands the build loop.
func cut1ArenaString(t *testing.T, prod *mmgr.Context, s string) Datum {
	t.Helper()
	off, ln := prod.AllocString(s)
	return newStringArenaDatum(prod, off, ln)
}

// cut1ArenaBytes builds an arena-backed KindBytes Datum in prod.
func cut1ArenaBytes(t *testing.T, prod *mmgr.Context, b []byte) Datum {
	t.Helper()
	off, ln := prod.AllocBytes(b)
	return newBytesArenaDatum(prod, off, ln)
}

// cut1ArenaBigNumeric builds an arena-backed big-numeric Datum in prod (the
// Q8 shape: beyond int64 range, sign+magnitude body in mctx).
func cut1ArenaBigNumeric(t *testing.T, prod *mmgr.Context, digits string, scale int16) Datum {
	t.Helper()
	bi, ok := new(big.Int).SetString(digits, 10)
	if !ok {
		t.Fatalf("bad big digits %q", digits)
	}
	return newBigNumericInCtx(prod, bi, scale)
}

// cut1BuildOp returns a joinOp whose retention call targets fresh
// stratum-B and stratum-D arenas parented to stmt (the statement context).
// Callers own stmt's Release, which cascades to the arenas. EX3-02 Cut 2:
// retainBuildRow's dense path requires BOTH strata, so the fixture ensures
// both (Cut 1's payload pins hold unchanged on the full path).
func cut1BuildOp(stmt *mmgr.Context) *joinOp {
	o := &joinOp{}
	o.ensureBuildBytes(&Context{Mctx: stmt})
	o.ensureBuildCells(&Context{Mctx: stmt})
	if o.buildBytes == nil || o.buildCells == nil {
		panic("ensureBuildBytes/ensureBuildCells: nil arena with non-nil statement context")
	}
	return o
}

// datumLogicalEqual compares logical values across encodings (arena IDs and
// backing stores legitimately differ between the new and legacy paths).
func datumLogicalEqual(a, b Datum) bool {
	if a.Kind != b.Kind {
		return false
	}
	switch a.Kind {
	case KindString:
		return a.StringValue() == b.StringValue()
	case KindBytes:
		return string(a.BytesValue()) == string(b.BytesValue())
	case KindNumeric:
		if (a.Flags & flagBigNumeric) != (b.Flags & flagBigNumeric) {
			return false
		}
		if a.Flags&flagBigNumeric != 0 {
			ab, bb := a.NumericBigValue(), b.NumericBigValue()
			if ab == nil || bb == nil {
				return ab == nil && bb == nil
			}
			return ab.Cmp(bb) == 0 && a.Scale == b.Scale
		}
		return a.Int == b.Int && a.Scale == b.Scale
	case KindEnum:
		return a.Int == b.Int && string(a.Buf) == string(b.Buf)
	case KindToastPointer:
		return string(a.Buf) == string(b.Buf)
	default:
		return a.Flags == b.Flags && a.ArenaID == b.ArenaID &&
			a.Scale == b.Scale && a.TimeSub == b.TimeSub &&
			a.Int == b.Int && a.Hi == b.Hi &&
			string(a.Buf) == string(b.Buf)
	}
}

func cut1AssertRowsEqual(t *testing.T, got, want Row) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("width %d, want %d", len(got), len(want))
	}
	for i := range want {
		if !datumLogicalEqual(got[i], want[i]) {
			t.Fatalf("col %d: got %v (%q), want %v (%q)",
				i, got[i], got[i].Format(), want[i], want[i].Format())
		}
	}
}

// TestDenseBuildArenaResetSurvival replays M0097-0058 over re-homed payloads:
// retained build rows must read back intact after the producer arena resets.
func TestDenseBuildArenaResetSurvival(t *testing.T) {
	stmt := mmgr.Acquire(nil, mmgr.KindStmt)
	defer stmt.Release()
	prod := mmgr.Acquire(nil, mmgr.KindStmt)
	defer prod.Release()

	o := cut1BuildOp(stmt)
	src := Row{
		NewIntDatum(1),
		cut1ArenaString(t, prod, "supplier#000000001"),
		cut1ArenaString(t, prod, "FRANCE"),
		cut1ArenaBytes(t, prod, []byte{0x01, 0x02, 0x03}),
		cut1ArenaBigNumeric(t, prod, "12345678901234567890123", 2),
		NewIntDatum(42),
		NullDatum,
	}
	legacy := ownedBuildRow(src)
	retained := o.retainBuildRow(src)

	// The producer pages are recycled before the join probes.
	prod.Reset()

	cut1AssertRowsEqual(t, retained, legacy)
	if got := retained[1].StringValue(); got != "supplier#000000001" {
		t.Fatalf("re-homed string = %q", got)
	}
	if got := retained[4].Format(); got != legacy[4].Format() {
		t.Fatalf("re-homed big-numeric = %q, want %q", got, legacy[4].Format())
	}
	if retained[1].ArenaID == prod.ID() {
		t.Fatalf("retained string still addresses the producer arena")
	}
}

// TestDenseBuildPermIndependenceQ8Shape pins the big-numeric-off-Perm move for
// BUILD rows: the retained big-numeric addresses the build arena (never
// mmgr.Perm()), Perm does not grow, and the value is exact.
func TestDenseBuildPermIndependenceQ8Shape(t *testing.T) {
	stmt := mmgr.Acquire(nil, mmgr.KindStmt)
	defer stmt.Release()
	prod := mmgr.Acquire(nil, mmgr.KindStmt)
	defer prod.Release()

	o := cut1BuildOp(stmt)
	const n = 500
	before, _ := mmgr.Perm().Usage()
	var first Row
	for i := 0; i < n; i++ {
		src := Row{
			NewIntDatum(int64(i)),
			cut1ArenaBigNumeric(t, prod, "100000000000000000000000", 20),
			cut1ArenaString(t, prod, "BRAZIL"),
			NewIntDatum(7),
		}
		r := o.retainBuildRow(src)
		if i == 0 {
			first = r
		}
		if got := r[1].ArenaID; got == mmgr.PermContextID {
			t.Fatalf("row %d: big-numeric still on Perm", i)
		}
		if got := r[1].ArenaID; got != o.buildBytes.ID() {
			t.Fatalf("row %d: big-numeric arena %d, want build arena %d",
				i, got, o.buildBytes.ID())
		}
	}
	after, _ := mmgr.Perm().Usage()
	if after != before {
		t.Fatalf("Perm grew by %d bytes over %d Q8-shape retains (want 0)", after-before, n)
	}
	// Exactness against the legacy path (Perm lane) after producer reset.
	prod.Reset()
	legacyNum := newBigNumericInCtx(mmgr.Perm(), func() *big.Int {
		bi, _ := new(big.Int).SetString("100000000000000000000000", 10)
		return bi
	}(), 20)
	if first[1].NumericBigValue().Cmp(legacyNum.NumericBigValue()) != 0 {
		t.Fatalf("big-numeric mismatch: %q vs %q",
			first[1].Format(), legacyNum.Format())
	}
	if got := first[2].StringValue(); got != "BRAZIL" {
		t.Fatalf("string mismatch: %q", got)
	}
}

// TestDenseBuildPassThrough pins the §3.4 carve-out: Buf-carrying and
// fixed-width Datums never enter stratum B — the retained row is
// bit-identical (==, not just value-equal) on those columns.
func TestDenseBuildPassThrough(t *testing.T) {
	stmt := mmgr.Acquire(nil, mmgr.KindStmt)
	defer stmt.Release()
	prod := mmgr.Acquire(nil, mmgr.KindStmt)
	defer prod.Release()

	o := cut1BuildOp(stmt)
	src := Row{
		NewIntDatum(1),
		NewEnumDatum(3, "shipped"),
		NewToastPointerDatum([]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}),
		NewStringDatum("owned-heap"),
		NewStringDatum(""),
		NullDatum,
		NewBoolDatum(true),
		cut1ArenaString(t, prod, "rehomed"),
	}
	retained := o.retainBuildRow(src)
	prod.Reset()
	for i, d := range src[:7] {
		g := retained[i]
		identical := g.Kind == d.Kind && g.Flags == d.Flags &&
			g.ArenaID == d.ArenaID && g.Scale == d.Scale &&
			g.TimeSub == d.TimeSub && g.Int == d.Int && g.Hi == d.Hi &&
			string(g.Buf) == string(d.Buf)
		if !identical {
			t.Fatalf("col %d (kind %v): passthrough changed %v → %v", i, d.Kind, d, retained[i])
		}
	}
	if got := retained[7].StringValue(); got != "rehomed" {
		t.Fatalf("arena col not re-homed: %q", got)
	}
	if got := retained[2].Format(); got != src[2].Format() {
		t.Fatalf("toast col changed: %q vs %q", got, src[2].Format())
	}
}

// TestDenseBuildRightSweepRehomedStrings drives a RIGHT join's unmatched-build
// emission (bucket sweep + NULL-key lane) over chunk-backed rows after the
// producer arena is gone.
func TestDenseBuildRightSweepRehomedStrings(t *testing.T) {
	stmt := mmgr.Acquire(nil, mmgr.KindStmt)
	defer stmt.Release()
	prod := mmgr.Acquire(nil, mmgr.KindStmt)
	defer prod.Release()

	probe := &shapedProbeOp{
		schema: chainSchema(2, 0),
		shape:  probeShapeSharedVirtual,
		rows: []Row{
			{NewIntDatum(9), NewStringDatum("p9")}, // hash-level miss
		},
	}
	o := chainFixture(optimizer.JoinTypeRight, probe, nil, 2, 2)
	o.ensureBuildBytes(&Context{Mctx: stmt})
	o.ensureBuildCells(&Context{Mctx: stmt})

	// One keyed-but-unmatched build row (bucket sweep lane) and one NULL-key
	// row (fillNullBuild lane), both retained through the new path.
	keyed := o.retainBuildRow(Row{NewIntDatum(1), cut1ArenaString(t, prod, "unmatched-build")})
	if err := o.insertBuildRow(NewIntDatum(1), keyed); err != nil {
		t.Fatalf("insertBuildRow: %v", err)
	}
	o.recordBuildNullKey(o.retainBuildRow(Row{NullDatum, cut1ArenaString(t, prod, "null-key-build")}))

	prod.Reset()

	if err := probe.Open(nil); err != nil {
		t.Fatalf("open probe: %v", err)
	}
	got := formatChainRows(drainJoin(t, o))
	want := []string{"NULL|NULL|1|unmatched-build", "NULL|NULL|NULL|null-key-build"}
	if len(got) != len(want) {
		t.Fatalf("emitted %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("row %d = %q, want %q (all: %v)", i, got[i], want[i], got)
		}
	}
}

// TestDenseBuildFullSweepRehomedStrings is the FULL-join half of the same
// contract: unmatched probe rows null-pad on the build side while unmatched
// build rows sweep with re-homed strings intact.
func TestDenseBuildFullSweepRehomedStrings(t *testing.T) {
	stmt := mmgr.Acquire(nil, mmgr.KindStmt)
	defer stmt.Release()
	prod := mmgr.Acquire(nil, mmgr.KindStmt)
	defer prod.Release()

	probe := &shapedProbeOp{
		schema: chainSchema(2, 0),
		shape:  probeShapeSharedVirtual,
		rows: []Row{
			{NewIntDatum(1), NewStringDatum("p1")}, // matches
			{NewIntDatum(9), NewStringDatum("p9")}, // probe-side fill
		},
	}
	o := chainFixture(optimizer.JoinTypeFull, probe, nil, 2, 2)
	o.ensureBuildBytes(&Context{Mctx: stmt})
	o.ensureBuildCells(&Context{Mctx: stmt})

	matched := o.retainBuildRow(Row{NewIntDatum(1), cut1ArenaString(t, prod, "b1")})
	if err := o.insertBuildRow(NewIntDatum(1), matched); err != nil {
		t.Fatalf("insertBuildRow: %v", err)
	}
	unmatched := o.retainBuildRow(Row{NewIntDatum(2), cut1ArenaString(t, prod, "b2")})
	if err := o.insertBuildRow(NewIntDatum(2), unmatched); err != nil {
		t.Fatalf("insertBuildRow: %v", err)
	}

	prod.Reset()

	if err := probe.Open(nil); err != nil {
		t.Fatalf("open probe: %v", err)
	}
	got := formatChainRows(drainJoin(t, o))
	want := []string{"1|p1|1|b1", "9|p9|NULL|NULL", "NULL|NULL|2|b2"}
	if len(got) != len(want) {
		t.Fatalf("emitted %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("row %d = %q, want %q (all: %v)", i, got[i], want[i], got)
		}
	}
}

// TestCloneRowOwnedStillAssertTransferable pins the F3 boundary from the
// other side: the legacy primitive Cut 1 leaves untouched still produces
// worker-transferable rows — including the big-numeric Perm lane.
func TestCloneRowOwnedStillAssertTransferable(t *testing.T) {
	prod := mmgr.Acquire(nil, mmgr.KindStmt)
	defer prod.Release()

	src := Row{
		NewIntDatum(1),
		cut1ArenaString(t, prod, "cross-queue"),
		cut1ArenaBigNumeric(t, prod, "12345678901234567890123", 0),
	}
	owned := cloneRowOwned(src)
	if err := AssertTransferable(owned); err != nil {
		t.Fatalf("cloneRowOwned output not transferable: %v", err)
	}
	if got := owned[2].ArenaID; got != mmgr.PermContextID {
		t.Fatalf("legacy big-numeric lane left Perm (arena %d) — non-build Perm contract moved", got)
	}
	prod.Reset()
	cut1AssertRowsEqual(t, owned, src)
}

// TestDenseBuildSharedOwnership pins the §2.3 shared-build decision: the
// stratum is parented to the statement context (a statement-end Release
// reclaims it even though the prebuild tree is never Closed); serial Close
// releases eagerly; a shared-adopted arena is only dereferenced.
func TestDenseBuildSharedOwnership(t *testing.T) {
	newStmt := func() *mmgr.Context { return mmgr.Acquire(nil, mmgr.KindStmt) }

	// Parented to the statement context: statement-end Release cascades.
	stmt := newStmt()
	o := cut1BuildOp(stmt)
	id := o.buildBytes.ID()
	if mmgr.Lookup(id) != o.buildBytes {
		t.Fatalf("build arena not registered")
	}
	stmt.Release()
	if mmgr.Lookup(id) != nil {
		t.Fatalf("build arena survived its statement context's Release")
	}

	// Serial teardown is eager.
	stmt2 := newStmt()
	defer stmt2.Release()
	o2 := cut1BuildOp(stmt2)
	id2 := o2.buildBytes.ID()
	o2.releaseBuildBytes()
	if o2.buildBytes != nil {
		t.Fatalf("releaseBuildBytes left a reference")
	}
	if mmgr.Lookup(id2) != nil {
		t.Fatalf("serial arena not released at teardown")
	}

	// Shared adoption: capture publishes, apply adopts, worker teardown
	// only dereferences, owner teardown releases.
	stmt3 := newStmt()
	defer stmt3.Release()
	leader := cut1BuildOp(stmt3)
	sb, err := leader.captureSharedBuild(true)
	if err != nil {
		t.Fatalf("captureSharedBuild: %v", err)
	}
	if sb.buildBytes != leader.buildBytes {
		t.Fatalf("captureSharedBuild did not publish the stratum")
	}
	worker := &joinOp{}
	worker.applySharedBuild(&Context{Mctx: stmt3}, sb)
	if worker.buildBytes != leader.buildBytes || !worker.buildBytesShared {
		t.Fatalf("applySharedBuild did not adopt the stratum as shared")
	}
	worker.releaseBuildBytes()
	if mmgr.Lookup(leader.buildBytes.ID()) == nil {
		t.Fatalf("worker teardown released a shared stratum")
	}
	leader.releaseBuildBytes()
	if mmgr.Lookup(sb.buildBytes.ID()) != nil {
		t.Fatalf("owner teardown did not release the stratum")
	}

	// No statement context: legacy fallback, values intact.
	prod := mmgr.Acquire(nil, mmgr.KindStmt)
	defer prod.Release()
	plain := &joinOp{}
	src := Row{NewIntDatum(1), cut1ArenaString(t, prod, "fallback")}
	got := plain.retainBuildRow(src)
	prod.Reset()
	cut1AssertRowsEqual(t, got, ownedBuildRow(src))
	if !strings.Contains(got[1].StringValue(), "fallback") {
		t.Fatalf("fallback path lost the value: %q", got[1].StringValue())
	}
}
