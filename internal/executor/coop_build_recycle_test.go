package executor

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// TestCoopBuildRecycledBatchesMatchSerialBuild pins M0122-0012 slice 92: the
// cooperative hash build's producers borrow their scan's row, copy its deform
// prefix into a batch carved from a recycled block, and the consumer hands
// each block back once it has moved past the batch. The join must return the
// rows the serial build returns (cooperative build off), with the deform
// poison off and armed, over a build side whose rows carry arena-backed text
// and columns past the deform bound, in enough rows (several 256-row batches
// per producer) that blocks get reused.
func TestCoopBuildRecycledBatchesMatchSerialBuild(t *testing.T) {
	ctx := deformW8Fixture(t)
	runSQL(t, ctx, `CREATE TABLE rb_dim (dk int, dname text, dcode varchar(20), pad1 text, pad2 int, pad3 text)`)
	runSQL(t, ctx, `CREATE TABLE rb_fact (fid int, fk int)`)
	runSQL(t, ctx, `INSERT INTO rb_dim SELECT x, 'name-' || x || repeat('n', x % 17), 'c' || (x % 97),
		repeat('p', 40), x * 3, repeat('q', 30) FROM generate_series(1, 3000) x`)
	runSQL(t, ctx, `INSERT INTO rb_fact SELECT x, (x * 7) % 3300 FROM generate_series(1, 12000) x`)
	// Statistics make the narrow, larger fact the probe and the wide dim
	// side (arena-backed text, columns past the deform bound) the build.
	runSQL(t, ctx, `ANALYZE rb_dim`)
	runSQL(t, ctx, `ANALYZE rb_fact`)
	var plan []string
	for _, r := range runSQL(t, ctx, `EXPLAIN SELECT f.fid FROM rb_fact f JOIN rb_dim d ON f.fk = d.dk`) {
		plan = append(plan, r[0].StringValue())
	}
	if text := strings.Join(plan, "\n"); !strings.Contains(text, "Hash\n") && !strings.Contains(text, "->  Hash  ") ||
		!strings.Contains(text[strings.Index(text, "Hash  "):], "rb_dim") {
		t.Fatalf("fixture does not build the hash on rb_dim:\n%s", text)
	}
	ctx.MinParallelTableScanBlocks = 0
	ctx.MaxParallelWorkers = 4
	// The narrow select list builds through a column-only Project and d.*
	// from the bare scan — both borrow. The third computes substr over the
	// build side's text; the planner evaluates it above the join, so its
	// producer is a column-only Project too (TestCoopBorrowScanShapes pins
	// the refusal of a computed target directly).
	queries := []string{
		`SELECT f.fid, d.dname, d.dcode FROM rb_fact f JOIN rb_dim d ON f.fk = d.dk`,
		`SELECT f.fid, d.* FROM rb_fact f JOIN rb_dim d ON f.fk = d.dk`,
		`SELECT f.fid, d.s FROM rb_fact f JOIN (SELECT dk, substr(dname, 3, 9) AS s FROM rb_dim) d ON f.fk = d.dk`,
	}
	run := func(q string) string {
		rows := runSQL(t, ctx, q)
		out := sortedCells(rows)
		sort.Strings(out)
		return fmt.Sprintf("%d:%s", len(out), strings.Join(out, "|"))
	}
	borrowedBefore, recycledBefore := coopBorrowedProducers.Load(), coopRecycledBatches.Load()
	for _, q := range queries {
		// The reference is a genuinely serial run: no parallel workers, so no
		// cooperative build at all. (Until M0147-0032 it toggled
		// GOOPG_COOP_JOIN_BUILD's variable, which gates only the join-tree
		// widening in coopDrivingScan — these builds are a bare scan or a
		// column-only Project, cooperative either way.)
		ctx.MaxParallelWorkers = 0
		want := run(q)
		ctx.MaxParallelWorkers = 4
		for _, poison := range []bool{false, true} {
			oldPoison := seqScanDeformPoison
			seqScanDeformPoison = poison
			// Repeated: a block handed back too early is overwritten only
			// when a producer happens to reuse it while the consumer still
			// reads it.
			for rep := 0; rep < 5; rep++ {
				if got := run(q); got != want {
					seqScanDeformPoison = oldPoison
					t.Fatalf("%s poison=%v: cooperative build rows differ from the serial build's\ncoop   %.300s\nserial %.300s", q, poison, got, want)
				}
			}
			seqScanDeformPoison = oldPoison
		}
		if n := strings.SplitN(want, ":", 2)[0]; len(n) < 4 {
			t.Fatalf("%s: fixture joins %s rows; expected thousands", q, n)
		}
	}
	// Reuse depends on the consumer handing a block back before a producer
	// starts its next batch, so it is asserted over all runs, not per run.
	if coopRecycledBatches.Load() == recycledBefore {
		t.Fatal("no producer batch reused a handed-back block; the recycling path is not exercised")
	}
	if coopBorrowedProducers.Load() == borrowedBefore {
		t.Fatal("no producer borrowed its scan; the bare-scan build path is not exercised")
	}
}

// TestCoopBorrowScanShapes pins which producer trees may let their scan
// borrow: the bare scan (copying its deform prefix), the scan under a Project
// of plain columns (copying every output column), and nothing else — a
// computed target over a borrowed scan can alias its page arena unmarked.
func TestCoopBorrowScanShapes(t *testing.T) {
	scan := &seqScanOp{}
	col := &optimizer.ColumnRef{Index: 1}
	for name, c := range map[string]struct {
		tree     Operator
		wantScan bool
		wantBare bool
	}{
		"bare scan":            {scan, true, true},
		"instrumented scan":    {&instrumentedOp{inner: scan}, true, true},
		"column-only project":  {&projectOp{child: scan, targets: []optimizer.Expr{col, &optimizer.ColumnRef{Index: 0}}}, true, false},
		"computed target":      {&projectOp{child: scan, targets: []optimizer.Expr{col, &optimizer.FuncCall{Name: "substr", Args: []optimizer.Expr{col}}}}, false, false},
		"whole-row target":     {&projectOp{child: scan, targets: []optimizer.Expr{&optimizer.ColumnRef{Index: -1}}}, false, false},
		"project over project": {&projectOp{child: &projectOp{child: scan, targets: []optimizer.Expr{col}}, targets: []optimizer.Expr{col}}, false, false},
	} {
		got, bare := coopBorrowScan(c.tree)
		if (got != nil) != c.wantScan || bare != c.wantBare {
			t.Fatalf("%s: borrow scan=%v bare=%v, want scan=%v bare=%v", name, got != nil, bare, c.wantScan, c.wantBare)
		}
	}
}
