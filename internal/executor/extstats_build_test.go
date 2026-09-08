package executor

// B-05a: extended-statistics build tests — ndistinct/dependencies values,
// serialize layout fidelity, _data row round-trip, and stattarget-0 skip.
//
// Oracle pins: postgres/src/backend/statistics/{mvdistinct.c,
// dependencies.c, extended_stats.c} + include/statistics/statistics.h.

import (
	"encoding/binary"
	"math"
	"math/rand"
	"testing"

	"github.com/goopg/goopg/internal/access/transam"
	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/storage"
)

// mkExtRow builds one synthetic sample row: ints[i] as KindInt, strs[i] as
// KindString. Column i of the row maps to attnum i+1 in the tests below.
func mkExtRow(ints []int64, strs []string) Row {
	row := make(Row, len(ints)+len(strs))
	for i, v := range ints {
		row[i] = NewIntDatum(v)
	}
	for i, s := range strs {
		row[len(ints)+i] = NewStringDatum(s)
	}
	return row
}

func TestExtNDistinctFullySampledIsExact(t *testing.T) {
	// 10 rows, x=i%5, y=i%2: all 10 (x,y) pairs distinct, sample == table.
	var rows []Row
	for i := int64(0); i < 10; i++ {
		rows = append(rows, mkExtRow([]int64{i % 5, i % 2}, nil))
	}
	nd := buildExtNDistinct(rows, []int{0, 1}, []int16{1, 2}, 10)
	if nd == nil {
		t.Fatal("buildExtNDistinct returned nil for a 2-column sample")
	}
	if len(nd.Items) != 1 {
		t.Fatalf("nitems=%d, want 1 (only the (1,2) combination)", len(nd.Items))
	}
	it := nd.Items[0]
	if len(it.Attrs) != 2 || it.Attrs[0] != 1 || it.Attrs[1] != 2 {
		t.Errorf("attrs=%v, want [1 2]", it.Attrs)
	}
	// Fully sampled (n == N): Duj1 degenerates to d exactly.
	if it.NDistinct != 10 {
		t.Errorf("ndistinct=%v, want 10 (exact, fully sampled)", it.NDistinct)
	}
}

func TestExtNDistinctScalesPartialSample(t *testing.T) {
	// Same 10 rows drawn from a 100-row table, all singletons:
	// 10*10 / (0 + 10*10/100) = 100, clamped to totalRows.
	var rows []Row
	for i := int64(0); i < 10; i++ {
		rows = append(rows, mkExtRow([]int64{i % 5, i % 2}, nil))
	}
	nd := buildExtNDistinct(rows, []int{0, 1}, []int16{3, 5}, 100)
	if nd == nil || len(nd.Items) != 1 {
		t.Fatalf("build = %v, want 1 item", nd)
	}
	if nd.Items[0].NDistinct != 100 {
		t.Errorf("ndistinct=%v, want 100 (Duj1 scale-up, clamped to N)", nd.Items[0].NDistinct)
	}
	if nd.Items[0].Attrs[0] != 3 || nd.Items[0].Attrs[1] != 5 {
		t.Errorf("attrs=%v, want [3 5] (caller attnums pass through)", nd.Items[0].Attrs)
	}
}

func TestExtNDistinctThreeColumnsEnumerateInOracleOrder(t *testing.T) {
	var rows []Row
	for i := int64(0); i < 8; i++ {
		rows = append(rows, mkExtRow([]int64{i, i % 2, i % 4}, nil))
	}
	nd := buildExtNDistinct(rows, []int{0, 1, 2}, []int16{1, 2, 3}, 8)
	if nd == nil {
		t.Fatal("nil build")
	}
	// C(3,2)+C(3,3) = 4 items: k=2 lexicographic, then the triple —
	// statext_ndistinct_build's emission order.
	want := [][]int16{{1, 2}, {1, 3}, {2, 3}, {1, 2, 3}}
	if len(nd.Items) != len(want) {
		t.Fatalf("nitems=%d, want %d", len(nd.Items), len(want))
	}
	for i, w := range want {
		got := nd.Items[i].Attrs
		if len(got) != len(w) {
			t.Fatalf("item %d attrs=%v, want %v", i, got, w)
		}
		for j := range w {
			if got[j] != w[j] {
				t.Fatalf("item %d attrs=%v, want %v", i, got, w)
			}
		}
	}
}

func TestExtNDistinctSerializeLayoutMatchesOracle(t *testing.T) {
	// Byte-fidelity against statext_ndistinct_serialize: 4-byte SET_VARSIZE
	// header, magic/type/nitems uint32s, then double + INT32 count + int16s
	// per item (the int32-vs-int16 asymmetry with dependencies is oracle's).
	nd := &ExtNDistinct{Items: []ExtNDistinctItem{{NDistinct: 5, Attrs: []int16{1, 2}}}}
	blob := serializeExtNDistinct(nd)
	if len(blob) != 4+12+8+4+4 {
		t.Fatalf("len=%d, want 32", len(blob))
	}
	if got := binary.LittleEndian.Uint32(blob[0:4]); got != uint32(32)<<2 {
		t.Errorf("varlena header=%08x, want SET_VARSIZE(32)", got)
	}
	if got := binary.LittleEndian.Uint32(blob[4:8]); got != 0xA352BFA4 {
		t.Errorf("magic=%08x, want STATS_NDISTINCT_MAGIC", got)
	}
	if got := binary.LittleEndian.Uint32(blob[8:12]); got != 1 {
		t.Errorf("type=%d, want STATS_NDISTINCT_TYPE_BASIC", got)
	}
	if got := binary.LittleEndian.Uint32(blob[12:16]); got != 1 {
		t.Errorf("nitems=%d, want 1", got)
	}
	if got := math.Float64frombits(binary.LittleEndian.Uint64(blob[16:24])); got != 5 {
		t.Errorf("ndistinct=%v, want 5", got)
	}
	// int32 member count (NOT int16).
	if got := binary.LittleEndian.Uint32(blob[24:28]); got != 2 {
		t.Errorf("nattributes=%d, want 2 as int32", got)
	}
	if binary.LittleEndian.Uint16(blob[28:30]) != 1 || binary.LittleEndian.Uint16(blob[30:32]) != 2 {
		t.Errorf("attributes bytes=%x, want attnums 1,2 as int16", blob[28:32])
	}
	rt, err := deserializeExtNDistinct(blob)
	if err != nil {
		t.Fatalf("round-trip deserialize: %v", err)
	}
	if len(rt.Items) != 1 || rt.Items[0].NDistinct != 5 ||
		len(rt.Items[0].Attrs) != 2 || rt.Items[0].Attrs[0] != 1 || rt.Items[0].Attrs[1] != 2 {
		t.Errorf("round-trip = %+v, want [{5 [1 2]}]", rt.Items)
	}
	if _, err := deserializeExtNDistinct(append([]byte(nil), blob...)); err != nil {
		t.Fatalf("copy deserialize: %v", err)
	}
	bad := append([]byte(nil), blob...)
	binary.LittleEndian.PutUint32(bad[4:8], 0xDEADBEEF)
	if _, err := deserializeExtNDistinct(bad); err == nil {
		t.Error("corrupt magic decoded without error")
	}
}

func TestExtDependenciesFunctionalDegree(t *testing.T) {
	// b = a%2 is functionally determined by a (degree 1.0); a is not
	// determined by b (degree 0, dropped).
	var rows []Row
	for i := int64(0); i < 100; i++ {
		rows = append(rows, mkExtRow([]int64{i, i % 2}, nil))
	}
	deps := buildExtDependencies(rows, []int{0, 1}, []int16{1, 2})
	if deps == nil {
		t.Fatal("buildExtDependencies returned nil")
	}
	if len(deps.Deps) != 1 {
		t.Fatalf("ndeps=%d, want 1 ((a)->b only)", len(deps.Deps))
	}
	d := deps.Deps[0]
	if d.Degree != 1.0 {
		t.Errorf("degree=%v, want 1.0", d.Degree)
	}
	if len(d.Attrs) != 2 || d.Attrs[0] != 1 || d.Attrs[1] != 2 {
		t.Errorf("attrs=%v, want [1 2] (implied column last)", d.Attrs)
	}
}

func TestExtDependenciesSerializeLayoutMatchesOracle(t *testing.T) {
	// Byte-fidelity against statext_dependencies_serialize: header + magic /
	// type / ndeps, then double + INT16 count + int16s per dependency.
	deps := &ExtDependencies{Deps: []ExtDependency{{Degree: 1.0, Attrs: []int16{1, 2}}}}
	blob := serializeExtDependencies(deps)
	if len(blob) != 4+12+8+2+4 {
		t.Fatalf("len=%d, want 30", len(blob))
	}
	if got := binary.LittleEndian.Uint32(blob[4:8]); got != 0xB4549A2C {
		t.Errorf("magic=%08x, want STATS_DEPS_MAGIC", got)
	}
	if got := binary.LittleEndian.Uint32(blob[12:16]); got != 1 {
		t.Errorf("ndeps=%d, want 1", got)
	}
	if got := math.Float64frombits(binary.LittleEndian.Uint64(blob[16:24])); got != 1.0 {
		t.Errorf("degree=%v, want 1.0", got)
	}
	// int16 member count (NOT int32 — the mirror asymmetry with ndistinct).
	if got := binary.LittleEndian.Uint16(blob[24:26]); got != 2 {
		t.Errorf("nattributes=%d, want 2 as int16", got)
	}
	rt, err := deserializeExtDependencies(blob)
	if err != nil {
		t.Fatalf("round-trip deserialize: %v", err)
	}
	if len(rt.Deps) != 1 || rt.Deps[0].Degree != 1.0 ||
		len(rt.Deps[0].Attrs) != 2 || rt.Deps[0].Attrs[0] != 1 || rt.Deps[0].Attrs[1] != 2 {
		t.Errorf("round-trip = %+v, want [{1.0 [1 2]}]", rt.Deps)
	}
	bad := append([]byte(nil), blob...)
	binary.LittleEndian.PutUint32(bad[4:8], 0xDEADBEEF)
	if _, err := deserializeExtDependencies(bad); err == nil {
		t.Error("corrupt magic decoded without error")
	}
}

func TestExtStatsTargetMechanics(t *testing.T) {
	tbl := &catalog.Table{Columns: []catalog.Column{
		{Name: "a", Ordinal: 0, StatTarget: &[]int{5}[0]},
		{Name: "b", Ordinal: 1},
	}}
	mkObj := func() *catalog.StatisticsObject {
		return &catalog.StatisticsObject{Name: "s", Schema: "public", Columns: []string{"a", "b"}}
	}
	// Unset everywhere → table-wide default.
	plain := &catalog.Table{Columns: []catalog.Column{
		{Name: "a", Ordinal: 0},
		{Name: "b", Ordinal: 1},
	}}
	if got := extStatsTarget(mkObj(), plain, 100); got != 100 {
		t.Errorf("default target=%d, want 100", got)
	}
	// Object target wins (statext_compute_stattarget's first arm)…
	obj := mkObj()
	obj.StatTarget = &[]int{42}[0]
	if got := extStatsTarget(obj, tbl, 100); got != 42 {
		t.Errorf("object target=%d, want 42", got)
	}
	// …including 0, which disables the build (the stattarget-0 skip).
	obj.StatTarget = &[]int{0}[0]
	if got := extStatsTarget(obj, tbl, 100); got != 0 {
		t.Errorf("object target 0=%d, want 0 (skip)", got)
	}
	// Column target wins over the default (max over member columns)…
	if got := extStatsTarget(mkObj(), tbl, 100); got != 5 {
		t.Errorf("column target=%d, want 5", got)
	}
	// …and an explicit column 0 disables too (0 > -1 in the max fold,
	// mirroring the oracle where attstattarget=0 beats the -1 default).
	tbl.Columns[1].StatTarget = &[]int{0}[0]
	if got := extStatsTarget(mkObj(), tbl, 100); got != 5 {
		t.Errorf("max column target=%d, want 5", got)
	}
	tbl.Columns[0].StatTarget = nil
	if got := extStatsTarget(mkObj(), tbl, 100); got != 0 {
		t.Errorf("lone column target 0=%d, want 0 (skip)", got)
	}
}

func TestExtStatsKindSelection(t *testing.T) {
	obj := &catalog.StatisticsObject{}
	if nd, dep := extStatsKinds(obj); !nd || !dep {
		t.Errorf("empty kinds → (%v,%v), want (true,true) (default = all)", nd, dep)
	}
	obj.Kinds = []string{"ndistinct"}
	if nd, dep := extStatsKinds(obj); !nd || dep {
		t.Errorf("ndistinct-only → (%v,%v), want (true,false)", nd, dep)
	}
	obj.Kinds = []string{"mcv"}
	if nd, dep := extStatsKinds(obj); nd || dep {
		t.Errorf("mcv-only → (%v,%v), want (false,false) (MCV deferred)", nd, dep)
	}
}

// seedExtStatsFixture seeds 200 (id, parity) rows and registers a statistics
// object on items(id, label); id is unique so (id)->label has degree 1.0 and
// every (id,label) pair is distinct.
func seedExtStatsFixture(t *testing.T, kinds []string, statTarget *int) (*Context, *catalog.Table, *catalog.StatisticsObject) {
	t.Helper()
	ctx, cat, cleanup := newStorageFixture(t)
	t.Cleanup(cleanup)
	im, ok := cat.(*catalog.InMemory)
	if !ok {
		t.Fatalf("fixture catalog is %T, want *catalog.InMemory", cat)
	}
	tbl, _ := im.LookupTable(parser.ObjectName{Name: "items"})
	obj := im.RegisterStatisticsFull("public", "s_ab", tbl.OID, kinds, []string{"id", "label"}, nil, false)
	if statTarget != nil {
		v := *statTarget
		im.SetStatisticsTarget("public.s_ab", &v)
	}
	var batch [][]optimizer.Expr
	flush := func() {
		if len(batch) == 0 {
			return
		}
		op, err := Build(&optimizer.Insert{Table: tbl, Source: &optimizer.Values{Rows: batch}, ColumnIndex: []int{0, 1}})
		if err != nil {
			t.Fatal(err)
		}
		if err := op.Open(ctx); err != nil {
			t.Fatal(err)
		}
		if _, err := op.Next(); err != EOF {
			t.Fatalf("Insert.Next: %v", err)
		}
		_ = op.Close()
		batch = nil
	}
	for i := int64(0); i < 200; i++ {
		par := "even"
		if i%2 == 1 {
			par = "odd"
		}
		batch = append(batch, []optimizer.Expr{
			&optimizer.IntegerConst{Value: i},
			&optimizer.StringConst{Value: par},
		})
		if len(batch) == 50 {
			flush()
		}
	}
	flush()
	if err := ctx.TxnMgr.Commit(ctx.Tx); err != nil {
		t.Fatal(err)
	}
	// Fresh statement transaction for ANALYZE, as the server would run it —
	// the seeding tx is committed and must not be reused for catalog writes.
	tx, err := ctx.TxnMgr.Begin(transam.IsolationReadCommitted)
	if err != nil {
		t.Fatal(err)
	}
	ctx.Tx = tx
	if snap, err := ctx.TxnMgr.SnapshotFor(tx); err != nil {
		t.Fatal(err)
	} else {
		ctx.Snap = snap
	}
	return ctx, tbl, obj
}

// scanExtDataHeap returns every live decoded row of the connection's 3429
// heap, in page order.
func scanExtDataHeap(t *testing.T, ctx *Context) []Row {
	t.Helper()
	rel := pgStatisticExtDataRel(ctx)
	cols := PGStatisticExtDataColumnsPG18()
	nBlocks, err := ctx.Pool.NBlocks(rel)
	if err != nil {
		t.Fatalf("NBlocks(3429): %v", err)
	}
	var out []Row
	for blk := storage.BlockNumber(0); blk < nBlocks; blk++ {
		slot, err := ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: blk})
		if err != nil {
			t.Fatalf("pin 3429 blk %d: %v", blk, err)
		}
		page := slot.Page()
		if storage.IsNew(page) {
			ctx.Pool.Unpin(slot)
			continue
		}
		count, err := storage.PageLinePointerCount(page)
		if err != nil {
			ctx.Pool.Unpin(slot)
			t.Fatalf("line pointers: %v", err)
		}
		for s := uint16(1); s <= uint16(count); s++ {
			ht, perr := storage.PageGetHeapTuple(page, s)
			if perr != nil {
				continue
			}
			if ht.Header.Xmax != storage.InvalidTransactionID {
				continue // stamped predecessor — reload skips these too
			}
			row := make(Row, len(cols))
			natts := int(ht.Header.Infomask2 & 0x07FF)
			if derr := DecodeRowIntoMctxPGTuple(row, cols, ht.Data, ht.Bitmap, natts, nil); derr != nil {
				ctx.Pool.Unpin(slot)
				t.Fatalf("decode 3429 row: %v", derr)
			}
			out = append(out, row)
		}
		ctx.Pool.Unpin(slot)
	}
	return out
}

func TestExtStatsAnalyzeWritesDataRow(t *testing.T) {
	ctx, tbl, obj := seedExtStatsFixture(t, []string{"ndistinct", "dependencies"}, nil)
	if _, err := analyzeRelationWith(ctx.Pool, ctx.TxnMgr, ctx.Catalog, tbl, upstreamDefaultStatsTarget,
		rand.New(rand.NewSource(42)), ctx.MultiXact, ctx); err != nil {
		t.Fatalf("analyzeRelationWith: %v", err)
	}
	rows := scanExtDataHeap(t, ctx)
	if len(rows) != 1 {
		t.Fatalf("3429 live rows=%d, want 1", len(rows))
	}
	row := rows[0]
	if got := uint32(row[0].Int); got != obj.OID {
		t.Errorf("stxoid=%d, want %d", got, obj.OID)
	}
	if row[1].BoolValue() {
		t.Error("stxdinherit=true, want false (single-relation scan only)")
	}
	ndBlob, depBlob, mcvBlob, exprBlob, err := DecodeStatisticExtDataPayloads(row)
	if err != nil {
		t.Fatalf("DecodeStatisticExtDataPayloads: %v", err)
	}
	if mcvBlob != nil || exprBlob != nil {
		t.Errorf("stxdmcv/stxdexpr = %v/%v, want NULL/NULL (both deferred)", mcvBlob != nil, exprBlob != nil)
	}
	if ndBlob == nil || depBlob == nil {
		t.Fatalf("blobs nd=%v dep=%v, want both non-NULL", ndBlob != nil, depBlob != nil)
	}
	nd, err := deserializeExtNDistinct(ndBlob)
	if err != nil {
		t.Fatalf("ndistinct deserialize: %v", err)
	}
	if len(nd.Items) != 1 {
		t.Fatalf("ndistinct items=%d, want 1", len(nd.Items))
	}
	// Fully sampled (200 rows < 30000 cap): every (id,label) pair distinct.
	if nd.Items[0].NDistinct != 200 {
		t.Errorf("ndistinct=%v, want 200 (exact)", nd.Items[0].NDistinct)
	}
	if len(nd.Items[0].Attrs) != 2 || nd.Items[0].Attrs[0] != 1 || nd.Items[0].Attrs[1] != 2 {
		t.Errorf("ndistinct attrs=%v, want [1 2]", nd.Items[0].Attrs)
	}
	deps, err := deserializeExtDependencies(depBlob)
	if err != nil {
		t.Fatalf("dependencies deserialize: %v", err)
	}
	if len(deps.Deps) != 1 {
		t.Fatalf("ndeps=%d, want 1 ((id)->label only)", len(deps.Deps))
	}
	if deps.Deps[0].Degree != 1.0 {
		t.Errorf("degree=%v, want 1.0", deps.Deps[0].Degree)
	}
	if len(deps.Deps[0].Attrs) != 2 || deps.Deps[0].Attrs[0] != 1 || deps.Deps[0].Attrs[1] != 2 {
		t.Errorf("dep attrs=%v, want [1 2]", deps.Deps[0].Attrs)
	}
}

func TestExtStatsTargetZeroSkipsWrite(t *testing.T) {
	zero := 0
	ctx, tbl, _ := seedExtStatsFixture(t, nil, &zero)
	if _, err := analyzeRelationWith(ctx.Pool, ctx.TxnMgr, ctx.Catalog, tbl, upstreamDefaultStatsTarget,
		rand.New(rand.NewSource(42)), ctx.MultiXact, ctx); err != nil {
		t.Fatalf("analyzeRelationWith: %v", err)
	}
	if rows := scanExtDataHeap(t, ctx); len(rows) != 0 {
		t.Errorf("3429 live rows=%d, want 0 (stattarget 0 disables the build)", len(rows))
	}
}

func TestExtStatsNoObjectWritesNothing(t *testing.T) {
	// A table with no statistics objects must leave 3429 untouched —
	// the hook is a no-op when fetch_statentries_for_relation is empty.
	ctx, tbl, _ := seedExtStatsFixture(t, nil, nil)
	im := ctx.Catalog.(*catalog.InMemory)
	im.DropStatistics("s_ab", "public")
	if _, err := analyzeRelationWith(ctx.Pool, ctx.TxnMgr, ctx.Catalog, tbl, upstreamDefaultStatsTarget,
		rand.New(rand.NewSource(42)), ctx.MultiXact, ctx); err != nil {
		t.Fatalf("analyzeRelationWith: %v", err)
	}
	if rows := scanExtDataHeap(t, ctx); len(rows) != 0 {
		t.Errorf("3429 live rows=%d, want 0 (no statistics objects)", len(rows))
	}
}

func TestExtStatsObjectsForTableAccessor(t *testing.T) {
	_, cat, cleanup := newStorageFixture(t)
	defer cleanup()
	im := cat.(*catalog.InMemory)
	a, _ := im.LookupTable(parser.ObjectName{Name: "items"})
	im.RegisterStatisticsFull("public", "s1", a.OID, []string{"ndistinct"}, []string{"id"}, nil, false)
	im.RegisterStatisticsFull("public", "s2", a.OID+999, []string{"dependencies"}, []string{"id"}, nil, false)
	got := im.StatisticsObjectsForTable(a.OID)
	if len(got) != 1 || got[0].Name != "s1" {
		t.Errorf("StatisticsObjectsForTable=%v, want [s1]", got)
	}
	if got := im.StatisticsObjectsForTable(123456789); len(got) != 0 {
		t.Errorf("unknown table → %d objects, want 0", len(got))
	}
}
