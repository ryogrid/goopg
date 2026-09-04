package executor

// EX3-02 Cut 2 (stratum D — Datum cells into chunks) poison pins.
//
// retainBuildRow packs arena-lane, Buf-free build rows into
// buildCells.AllocAligned(w*48, 8) chunk views; every other row stays FULLY
// heap-backed (F1 whole-row heap rule for Buf-carriers, rowHasArena gate for
// non-arena rows). These tests pin the behaviour contract, not the win:
//   - cells pack contiguously (the dense_alloc analogue) and read back
//     value-equal to legacy ownedBuildRow after the producer arena resets;
//   - probe-after-producer-reset on the INNER match path over chunk views;
//   - demoteIntHash mid-build re-key moves dense Row headers, not payloads;
//   - composite-lane filing (incl. pack-miss demotion) over dense rows;
//   - the F7 pack assertion (ArenaID!=0 ==> Buf==nil) panics on drift, and
//     is unreachable through retainBuildRow (the Buf gate diverts first);
//   - Buf-carrying rows consume ZERO stratum-D bytes and stay bit-identical;
//   - the releaseRow guard: pool memory never aliases chunk memory;
//   - stratum-D ownership (statement parenting, serial eager release,
//     nil-Mctx fallback) mirroring stratum B;
//   - census actuals: arena-lane headers go per-row acquire to
//     chunk-amortized ~0; make-lane int rows stay 1.00.

import (
	"fmt"
	"runtime"
	"strings"
	"testing"
	"unsafe"

	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/utils/mmgr"
)

// TestDenseBuildCellsPackContiguous pins the Cut-2 geometry: consecutive
// retained rows are contiguous w*48 extents in one chunk, and read back
// value-equal to the legacy path after the producer arena is gone.
func TestDenseBuildCellsPackContiguous(t *testing.T) {
	stmt := mmgr.Acquire(nil, mmgr.KindStmt)
	defer stmt.Release()
	prod := mmgr.Acquire(nil, mmgr.KindStmt)
	defer prod.Release()

	o := cut1BuildOp(stmt)
	const n = 50
	rows := make([]Row, 0, n)
	for i := 0; i < n; i++ {
		src := Row{NewIntDatum(int64(i)), cut1ArenaString(t, prod, fmt.Sprintf("payload-%04d", i))}
		rows = append(rows, o.retainBuildRow(src))
	}
	for i, r := range rows {
		if len(r) != 2 || cap(r) != 2 {
			t.Fatalf("row %d: len/cap = %d/%d, want 2/2", i, len(r), cap(r))
		}
	}
	// 50 rows x 96 B = 4.8 KiB << 64 KiB chunk: every adjacent pair must
	// be exactly one w*48 extent apart (AllocAligned pads to 8, and 96 is
	// already 8-aligned, so pad is 0 throughout the first chunk).
	const stride = uintptr(2 * denseDatumCellSize)
	for i := 1; i < n; i++ {
		got := uintptr(unsafe.Pointer(&rows[i][0])) - uintptr(unsafe.Pointer(&rows[i-1][0]))
		if got != stride {
			t.Fatalf("rows[%d] starts %d bytes after rows[%d], want contiguous %d",
				i, got, i-1, stride)
		}
	}

	prod.Reset()
	for i, r := range rows {
		if r[0].Int != int64(i) {
			t.Fatalf("row %d: key = %d, want %d", i, r[0].Int, i)
		}
		if want := fmt.Sprintf("payload-%04d", i); r[1].StringValue() != want {
			t.Fatalf("row %d: payload = %q, want %q", i, r[1].StringValue(), want)
		}
		legacy := ownedBuildRow(Row{NewIntDatum(int64(i)), NewStringDatum(fmt.Sprintf("payload-%04d", i))})
		cut1AssertRowsEqual(t, r, legacy)
	}
}

// TestDenseBuildProbeAfterProducerReset drives the INNER match path over
// chunk-view build rows after the producer arena is gone: the probe reads
// dense cells + re-homed stratum-B payloads, never producer memory.
func TestDenseBuildProbeAfterProducerReset(t *testing.T) {
	stmt := mmgr.Acquire(nil, mmgr.KindStmt)
	defer stmt.Release()
	prod := mmgr.Acquire(nil, mmgr.KindStmt)
	defer prod.Release()

	probe := &shapedProbeOp{
		schema: chainSchema(2, 0),
		shape:  probeShapeSharedVirtual,
		rows: []Row{
			{NewIntDatum(1), NewStringDatum("p1")}, // matches
			{NewIntDatum(9), NewStringDatum("p9")}, // hash-level miss
		},
	}
	o := chainFixture(optimizer.JoinTypeInner, probe, nil, 2, 2)
	o.ensureBuildBytes(&Context{Mctx: stmt})
	o.ensureBuildCells(&Context{Mctx: stmt})

	for i, payload := range []string{"b1", "b2"} {
		r := o.retainBuildRow(Row{NewIntDatum(int64(i + 1)), cut1ArenaString(t, prod, payload)})
		if err := o.insertBuildRow(NewIntDatum(int64(i+1)), r); err != nil {
			t.Fatalf("insertBuildRow: %v", err)
		}
	}

	prod.Reset()

	if err := probe.Open(nil); err != nil {
		t.Fatalf("open probe: %v", err)
	}
	got := formatChainRows(drainJoin(t, o))
	want := []string{"1|p1|1|b1"}
	if len(got) != len(want) {
		t.Fatalf("emitted %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("row %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestDenseBuildDemoteIntHashRekey pins §3.5 over chunk views: demoteIntHash
// moves Row headers between maps (never Datum copies), so dense rows filed
// before the demotion stay valid and land under the same canonical keys as
// rows filed after it — all surviving the producer reset.
func TestDenseBuildDemoteIntHashRekey(t *testing.T) {
	stmt := mmgr.Acquire(nil, mmgr.KindStmt)
	defer stmt.Release()
	prod := mmgr.Acquire(nil, mmgr.KindStmt)
	defer prod.Release()

	o := &joinOp{lazyHashIsInt: true}
	o.ensureBuildBytes(&Context{Mctx: stmt})
	o.ensureBuildCells(&Context{Mctx: stmt})

	retain := func(key int64, payload string) {
		t.Helper()
		r := o.retainBuildRow(Row{NewIntDatum(key), cut1ArenaString(t, prod, payload)})
		o.lazyHashInsertDatum(NewIntDatum(key), r)
	}
	retain(1, "one")
	retain(2, "two")
	if o.lazyIntHash == nil || len(o.lazyIntHash) != 2 {
		t.Fatalf("int map holds %d keys before the demotion, want 2", len(o.lazyIntHash))
	}
	// An integer-typed key yielding a non-int64-representable datum breaks
	// the plan's promise: the build degrades to the string map mid-build.
	// The demoting row is itself dense (arena payload, no Buf).
	demoter := o.retainBuildRow(Row{NewIntDatum(3), cut1ArenaString(t, prod, "three")})
	o.lazyHashInsertDatum(NewStringDatum("s"), demoter)
	if o.lazyHashIsInt {
		t.Fatal("int lane survived a non-int64 key")
	}
	if o.lazyIntHash != nil {
		t.Fatal("int map not dropped by the demotion")
	}

	prod.Reset()

	payloads := map[string]bool{}
	count := 0
	for _, rows := range o.lazyHash {
		for _, r := range rows {
			count++
			payloads[r[1].StringValue()] = true
		}
	}
	if count != 3 {
		t.Fatalf("string map holds %d rows after the demotion, want 3", count)
	}
	for _, want := range []string{"one", "two", "three"} {
		if !payloads[want] {
			t.Fatalf("payload %q lost across the demotion (have %v)", want, payloads)
		}
	}
}

// TestDenseBuildCompositeLaneFiling pins the composite lane over dense rows:
// filing under o.execKeyBuf, the packed-int demotion on a pack miss, and
// payload survival past the producer reset.
func TestDenseBuildCompositeLaneFiling(t *testing.T) {
	stmt := mmgr.Acquire(nil, mmgr.KindStmt)
	defer stmt.Release()
	prod := mmgr.Acquire(nil, mmgr.KindStmt)
	defer prod.Release()

	o := &joinOp{
		execKeys: make([]optimizer.JoinKeyPair, 2),
		buildKeyExprs: []optimizer.Expr{
			&optimizer.ColumnRef{Index: 0},
			&optimizer.ColumnRef{Index: 1},
		},
		execKeyPackInt: true,
	}
	o.ensureBuildBytes(&Context{Mctx: stmt})
	o.ensureBuildCells(&Context{Mctx: stmt})

	file := func(a, b Datum, payload string) {
		t.Helper()
		slot := SlotFromRow(nil, Row{a, b})
		ok, err := o.encodeBuildCompositeKey(slot)
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		if !ok {
			t.Fatalf("key rejected for payload %q", payload)
		}
		o.fileCompositeBuildRow(o.retainBuildRow(Row{a, b, cut1ArenaString(t, prod, payload)}))
	}

	file(NewIntDatum(1998), NewIntDatum(7), "a")
	file(NewIntDatum(1998), NewIntDatum(8), "b")
	if !o.execKeyPackInt {
		t.Fatal("pack lane abandoned before any non-int key")
	}
	// Arena-backed string key: breaks the packed lane (pack miss) while the
	// ROW stays dense-eligible (Buf==nil throughout).
	file(NewIntDatum(1998), cut1ArenaString(t, prod, "s"), "c")
	if o.execKeyPackInt {
		t.Fatal("pack lane survived a non-int64 key")
	}
	file(NewIntDatum(1998), NewIntDatum(7), "d")

	if len(o.lazyHash) != 3 {
		t.Fatalf("string map has %d keys after the demotion, want 3", len(o.lazyHash))
	}

	prod.Reset()

	payloads := map[string]bool{}
	for _, rows := range o.lazyHash {
		for _, r := range rows {
			if len(r) != 3 {
				t.Fatalf("filed row has width %d, want 3", len(r))
			}
			payloads[r[2].StringValue()] = true
		}
	}
	for _, want := range []string{"a", "b", "c", "d"} {
		if !payloads[want] {
			t.Fatalf("payload %q lost across composite filing+demotion (have %v)", want, payloads)
		}
	}
}

// TestDenseBuildF7PackAssertion pins the Cut-2 representation guard: a Datum
// with BOTH ArenaID!=0 and Buf!=nil must never enter stratum D. The pack
// helper panics loudly rather than planting a GC-invisible pointer in
// noscan chunk memory — while retainBuildRow itself diverts such rows to
// the heap path first (the assertion is unreachable through it).
func TestDenseBuildF7PackAssertion(t *testing.T) {
	stmt := mmgr.Acquire(nil, mmgr.KindStmt)
	defer stmt.Release()
	prod := mmgr.Acquire(nil, mmgr.KindStmt)
	defer prod.Release()

	o := cut1BuildOp(stmt)
	off, ln := prod.AllocString("drift")
	drifted := newStringArenaDatum(prod, off, ln)
	drifted.Buf = []byte("drift-alias")
	row := Row{NewIntDatum(1), drifted}

	func() {
		defer func() {
			r := recover()
			if r == nil {
				t.Fatalf("packDenseBuildRow accepted an ArenaID!=0 + Buf!=nil Datum")
			}
			if !strings.Contains(fmt.Sprint(r), "F7") {
				t.Fatalf("panic %v does not identify the F7 guard", r)
			}
		}()
		_ = o.packDenseBuildRow(row)
	}()

	// Through the public path the Buf gate diverts first: no panic, values
	// intact, and identical to the legacy whole-row heap path.
	prod.Reset()
	var kept Row
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("retainBuildRow panicked on a Buf-carrying row: %v", r)
			}
		}()
		kept = o.retainBuildRow(row)
	}()
	legacy := ownedBuildRow(row)
	cut1AssertRowsEqual(t, kept, legacy)
}

// TestDenseBuildBufRowsStayHeap pins the F1 whole-row heap rule: rows with
// ANY Buf-carrying Datum (enum labels, toast bodies, Buf-backed strings)
// consume ZERO stratum-D bytes, keep those columns bit-identical, and still
// re-home their arena-backed columns into stratum B (Cut-1 behaviour on the
// heap lane). Zero-width rows take the same heap path (never AllocAligned).
func TestDenseBuildBufRowsStayHeap(t *testing.T) {
	stmt := mmgr.Acquire(nil, mmgr.KindStmt)
	defer stmt.Release()
	prod := mmgr.Acquire(nil, mmgr.KindStmt)
	defer prod.Release()

	o := cut1BuildOp(stmt)
	rows := []Row{
		{NewIntDatum(1), NewEnumDatum(3, "shipped"), cut1ArenaString(t, prod, "a")},
		{NewIntDatum(2), NewToastPointerDatum([]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}), cut1ArenaString(t, prod, "b")},
		{NewIntDatum(3), NewStringDatum("owned-heap"), NewIntDatum(7)},
		{NewIntDatum(4), NewBytesDatum([]byte{0xaa, 0xbb}), cut1ArenaBytes(t, prod, []byte{0x01})},
	}
	var kept []Row
	for _, src := range rows {
		kept = append(kept, o.retainBuildRow(src))
	}
	kept = append(kept, o.retainBuildRow(Row{}))

	if alloc, peak := o.buildCells.Usage(); alloc != 0 || peak != 0 {
		t.Fatalf("Buf-carrying rows consumed stratum D: allocated=%d peak=%d, want 0/0", alloc, peak)
	}

	prod.Reset()

	for i, src := range rows {
		got := kept[i]
		cut1AssertRowsEqual(t, got, ownedBuildRow(src))
		// The Buf column survives bit-identical (same ArenaID==0 shape and
		// same bytes — never fabricated into an (offset,length)).
		d, g := src[1], got[1]
		if g.Kind != d.Kind || g.Flags != d.Flags || g.ArenaID != 0 || d.ArenaID != 0 ||
			g.Int != d.Int || g.Hi != d.Hi || string(g.Buf) != string(d.Buf) {
			t.Fatalf("row %d: Buf column changed %v → %v", i, d, g)
		}
	}
	// Arena-backed columns on heap-lane rows still address stratum B.
	if got := kept[0][2].ArenaID; got != o.buildBytes.ID() {
		t.Fatalf("heap-lane arena column addresses arena %d, want stratum B %d",
			got, o.buildBytes.ID())
	}
	if len(kept[4]) != 0 {
		t.Fatalf("zero-width row retained with width %d", len(kept[4]))
	}
}

// TestDenseBuildPoolAliasGuard is the executable half of the §2.4 invariant:
// dense rows bypass rowPool, and no lifecycle path may hand one to
// releaseRow (which zeroes + Puts — pinning the whole chunk per row and
// aliasing pool memory to chunk memory). Aggressively churn the pool around
// live filed dense rows (incl. a demotion) and demand the filed rows read
// back intact: any leaked dense view would alias an acquired row and the
// scribble would corrupt the chunk.
func TestDenseBuildPoolAliasGuard(t *testing.T) {
	stmt := mmgr.Acquire(nil, mmgr.KindStmt)
	defer stmt.Release()
	prod := mmgr.Acquire(nil, mmgr.KindStmt)
	defer prod.Release()

	o := &joinOp{lazyHashIsInt: true}
	o.ensureBuildBytes(&Context{Mctx: stmt})
	o.ensureBuildCells(&Context{Mctx: stmt})

	const n = 20
	for i := 0; i < n; i++ {
		r := o.retainBuildRow(Row{NewIntDatum(int64(i)), cut1ArenaString(t, prod, fmt.Sprintf("guard-%04d", i))})
		o.lazyHashInsertDatum(NewIntDatum(int64(i)), r)
	}
	// Move every dense header across maps mid-build.
	o.lazyHashInsertDatum(NewStringDatum("demote"), o.retainBuildRow(
		Row{NewIntDatum(n), cut1ArenaString(t, prod, "guard-demoter")}))
	if o.lazyIntHash != nil {
		t.Fatal("demotion did not drop the int map")
	}

	prod.Reset()

	scribble := func() {
		t.Helper()
		for j := 0; j < 50; j++ {
			a := acquireRow(2)
			for i := range a {
				a[i] = NewIntDatum(-999)
			}
			if a[0].Int != -999 || a[1].Int != -999 {
				t.Fatalf("acquired pool row not writable")
			}
			releaseRow(a)
			// Re-acquired pool rows come back zeroed (existing contract):
			// proves the scribble actually cycled through pool memory.
			b := acquireRow(2)
			for i := range b {
				if z := b[i]; z.Kind != KindNull || z.Int != 0 || z.Buf != nil ||
					z.ArenaID != 0 || z.Flags != 0 || z.Hi != 0 {
					t.Fatalf("pool row not zeroed after release: %+v", z)
				}
			}
			releaseRow(b)
		}
	}
	scribble()

	seen := map[string]bool{}
	for _, rows := range o.lazyHash {
		for _, r := range rows {
			seen[r[1].StringValue()] = true
		}
	}
	for i := 0; i < n; i++ {
		if want := fmt.Sprintf("guard-%04d", i); !seen[want] {
			t.Fatalf("filed dense row %q corrupted by pool churn (have %v)", want, seen)
		}
	}
	if !seen["guard-demoter"] {
		t.Fatalf("demoted dense row corrupted by pool churn (have %v)", seen)
	}
	scribble()
	for _, rows := range o.lazyHash {
		for _, r := range rows {
			if r[0].Kind != KindInt {
				t.Fatalf("filed dense row key changed kind to %v after pool churn", r[0].Kind)
			}
		}
	}
}

// TestDenseBuildCellsOwnership pins the §2.3 lifetime decision for stratum D:
// parented to the statement context (statement-end Release cascades even
// though the prebuild tree is never Closed); serial teardown is eager;
// ensure is idempotent; and the nil-Mctx shape degrades to legacy values.
func TestDenseBuildCellsOwnership(t *testing.T) {
	newStmt := func() *mmgr.Context { return mmgr.Acquire(nil, mmgr.KindStmt) }

	// Parented to the statement context: statement-end Release cascades.
	stmt := newStmt()
	o := cut1BuildOp(stmt)
	id := o.buildCells.ID()
	if mmgr.Lookup(id) != o.buildCells {
		t.Fatalf("cell arena not registered")
	}
	stmt.Release()
	if mmgr.Lookup(id) != nil {
		t.Fatalf("cell arena survived its statement context's Release")
	}

	// Serial teardown is eager.
	stmt2 := newStmt()
	defer stmt2.Release()
	o2 := cut1BuildOp(stmt2)
	id2 := o2.buildCells.ID()
	first := o2.buildCells
	o2.ensureBuildCells(&Context{Mctx: stmt2})
	if o2.buildCells != first {
		t.Fatalf("ensureBuildCells not idempotent")
	}
	o2.releaseBuildCells()
	o2.releaseBuildBytes()
	if o2.buildCells != nil {
		t.Fatalf("releaseBuildCells left a reference")
	}
	if mmgr.Lookup(id2) != nil {
		t.Fatalf("serial cell arena not released at teardown")
	}
	// Double release is safe (re-Open discipline calls release unconditionally).
	o2.releaseBuildCells()

	// No statement context: legacy fallback, values intact.
	prod := mmgr.Acquire(nil, mmgr.KindStmt)
	defer prod.Release()
	plain := &joinOp{}
	src := Row{NewIntDatum(1), cut1ArenaString(t, prod, "fallback")}
	legacy := ownedBuildRow(src)
	got := plain.retainBuildRow(src)
	prod.Reset()
	cut1AssertRowsEqual(t, got, legacy)
}

// TestDenseBuildAllocsPerRow reports census actuals against the Cut-0
// prediction: arena-lane headers go from per-row acquire (~2+ allocs/row
// with payloads) to chunk-amortized ~0; make-lane int rows stay 1.00.
// A runtime.GC up front drains the row/chunk freelists so both arms measure
// steady-state misses, and N=2000 keeps any residual pool warmth negligible.
func TestDenseBuildAllocsPerRow(t *testing.T) {
	// Make-lane int rows: deterministic 1.00 on both paths (per-row make,
	// never pooled, never chunked — the rowHasArena gate).
	intRow := Row{NewIntDatum(1), NewIntDatum(2), NewIntDatum(3)}
	if got := testing.AllocsPerRun(100, func() { _ = ownedBuildRow(intRow) }); got != 1 {
		t.Fatalf("legacy int row = %.2f allocs/row, want 1.00", got)
	}
	stmt0 := mmgr.Acquire(nil, mmgr.KindStmt)
	defer stmt0.Release()
	op0 := cut1BuildOp(stmt0)
	if got := testing.AllocsPerRun(100, func() { _ = op0.retainBuildRow(intRow) }); got != 1 {
		t.Fatalf("dense int row = %.2f allocs/row, want 1.00 (make lane must not move)", got)
	}

	// Arena-lane rows via MemStats Mallocs deltas. Two shapes: a header-only
	// row (zero-length arena string: rowHasArena true, zero payload bytes —
	// isolates the header acquire the census priced at ~2.00) and a full
	// row (header + 2 payload makes on the legacy path).
	stmt := mmgr.Acquire(nil, mmgr.KindStmt)
	defer stmt.Release()
	prod := mmgr.Acquire(nil, mmgr.KindStmt)
	defer prod.Release()
	o := cut1BuildOp(stmt)

	const N = 2000
	zeroOff, zeroLen := prod.AllocString("")
	headerSrcs := make([]Row, 0, N)
	for i := 0; i < N; i++ {
		headerSrcs = append(headerSrcs, Row{
			NewIntDatum(int64(i)),
			newStringArenaDatum(prod, zeroOff, zeroLen),
		})
	}

	measure := func(retain func(Row) Row, srcs []Row) float64 {
		t.Helper()
		runtime.GC()
		var before runtime.MemStats
		runtime.ReadMemStats(&before)
		kept := make([]Row, 0, len(srcs))
		for _, src := range srcs {
			kept = append(kept, retain(src))
		}
		runtime.KeepAlive(kept)
		var after runtime.MemStats
		runtime.ReadMemStats(&after)
		return float64(after.Mallocs-before.Mallocs) / float64(len(srcs))
	}

	legacyHeader := measure(ownedBuildRow, headerSrcs)
	denseHeader := measure(o.retainBuildRow, headerSrcs)

	srcs := make([]Row, 0, N)
	for i := 0; i < N; i++ {
		srcs = append(srcs, Row{
			NewIntDatum(int64(i)),
			cut1ArenaString(t, prod, fmt.Sprintf("supplier#%09d", i)),
			cut1ArenaString(t, prod, "FRANCE"),
			NewIntDatum(7),
		})
	}
	legacyPerRow := measure(ownedBuildRow, srcs)
	densePerRow := measure(o.retainBuildRow, srcs)

	t.Logf("allocs/row: legacy header-only=%.3f dense header-only=%.3f "+
		"(predicted ~2.00 acquire → ~0); legacy full=%.3f dense full=%.3f "+
		"(header + 2 payload makes → chunk-amortized ~0); int-lane=1.000 both (predicted 1.00)",
		legacyHeader, denseHeader, legacyPerRow, densePerRow)

	if legacyHeader < 1.5 {
		t.Fatalf("legacy header-only = %.3f allocs/row, want >= 1.5 (census baseline moved?)", legacyHeader)
	}
	if denseHeader > 0.1 {
		t.Fatalf("dense header-only = %.3f allocs/row, want <= 0.1 (headers not amortized?)", denseHeader)
	}
	if legacyPerRow < 2.0 {
		t.Fatalf("legacy arena-lane = %.3f allocs/row, want >= 2.0 (census baseline moved?)", legacyPerRow)
	}
	if densePerRow > 0.25 {
		t.Fatalf("dense arena-lane = %.3f allocs/row, want <= 0.25 (headers not amortized?)", densePerRow)
	}

	// Values agree across the measured arms after the producer is gone.
	prod.Reset()
	for i := 0; i < N; i += 211 {
		cut1AssertRowsEqual(t, o.retainBuildRow(srcs[i]), ownedBuildRow(srcs[i]))
	}
}
