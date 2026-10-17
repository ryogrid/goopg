package executor

import (
	"reflect"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/access/transam"
	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

// TestSSI_BitmapHeapScanTakesPredicateLocks pins the M-NIGHTLY
// ReadWriteUnique4 fix: a SERIALIZABLE read through a Bitmap Heap Scan must
// predicate-lock what it read, as PG's bitmap path does (btgetbitmap locks the
// index leaf pages; BitmapHeapScanNextBlock locks each visible TID). goopg
// takes the relation grain for the index range, as indexScanOp's gap lock
// does, and a tuple SIREAD per visible row. Before the fix the bitmap scan
// took none, so read-write-unique-4's `r1 r2 w1 w2` — whose
// `MAX(invoice_number) … WHERE year = 2016` plans as a bitmap scan on the
// small table — never saw the rw-conflict and raised 23505 where PG raises
// 40001.
func TestSSI_BitmapHeapScanTakesPredicateLocks(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	for _, q := range []string{
		"CREATE TABLE invoice (year int, invoice_number int, PRIMARY KEY (year, invoice_number))",
		"INSERT INTO invoice VALUES (2016, 1), (2016, 2)",
	} {
		runSQL(t, ctx, q)
	}
	if err := ctx.TxnMgr.Commit(ctx.Tx); err != nil {
		t.Fatalf("commit setup: %v", err)
	}

	reader, err := ctx.TxnMgr.Begin(transam.IsolationSerializable)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ctx.TxnMgr.Rollback(reader) }()
	snap, err := ctx.TxnMgr.SnapshotFor(reader)
	if err != nil {
		t.Fatal(err)
	}
	ctx.Tx, ctx.Snap = reader, snap

	sql := "SELECT invoice_number FROM invoice WHERE year = 2016"
	ps := optimizer.DefaultPlannerSettings()
	ps.EnableSeqScan = false
	ps.EnableIndexScan = false
	ps.MaxParallelWorkersPerGather = 0
	stmts, err := parser.Parse(sql)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := optimizer.PlanWithSettings(stmts[0], ctx.Catalog, ps)
	if err != nil {
		t.Fatal(err)
	}
	if !planHasBitmapHeapScan(plan) {
		t.Fatalf("fixture: want a Bitmap Heap Scan plan, got %T", plan)
	}
	op, err := Build(plan)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := Run(op, ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(renderRows(rows), ";"); got != "1;2" {
		t.Fatalf("rows = %q, want 1;2", got)
	}

	tbl, _ := ctx.Catalog.LookupTable(parser.ObjectName{Name: "invoice"})
	rel := ctx.Catalog.RelFileNode(tbl)
	if !ctx.TxnMgr.HoldsPredicateLock(reader.Handle, transam.RelationLockTag(rel.DBOid, rel.RelOid)) {
		t.Errorf("SERIALIZABLE bitmap scan holds no relation-grain SIREAD on invoice")
	}
}

// planHasBitmapHeapScan reports whether the plan tree holds a BitmapHeapScan.
func planHasBitmapHeapScan(n optimizer.Node) bool {
	found := false
	seen := map[uintptr]bool{}
	var walk func(v reflect.Value, depth int)
	walk = func(v reflect.Value, depth int) {
		if found || depth > 200 || !v.IsValid() {
			return
		}
		switch v.Kind() {
		case reflect.Interface:
			if !v.IsNil() {
				walk(v.Elem(), depth+1)
			}
		case reflect.Ptr:
			if v.IsNil() || seen[v.Pointer()] {
				return
			}
			seen[v.Pointer()] = true
			if v.CanInterface() {
				if _, ok := v.Interface().(*optimizer.BitmapHeapScan); ok {
					found = true
					return
				}
			}
			walk(v.Elem(), depth+1)
		case reflect.Struct:
			for i := 0; i < v.NumField(); i++ {
				if f := v.Field(i); f.Kind() == reflect.Interface || f.Kind() == reflect.Ptr || f.Kind() == reflect.Slice {
					walk(f, depth+1)
				}
			}
		case reflect.Slice:
			for i := 0; i < v.Len(); i++ {
				walk(v.Index(i), depth+1)
			}
		}
	}
	walk(reflect.ValueOf(n), 0)
	return found
}
