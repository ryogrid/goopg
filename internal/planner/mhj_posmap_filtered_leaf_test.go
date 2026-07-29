package planner

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestBuildBindingsPosMapSeesThroughFilteredMHJLeaf pins the M0125-0013
// fix: `buildBindingsPosMap` must classify a MultiHashJoin table that is
// wrapped in a *Filter, not silently skip it.
//
// An MHJ table is not always a bare scan.
// `pushSingleSourceFiltersIntoMHJTables` replaces `mh.Tables[i]` with
// `&Filter{Child: <scan>}` whenever it pushes a single-source conjunct
// down into that input, and it runs (from planSelect, after
// remapWithBindings) BEFORE remapTopProjection consumes this posMap.
//
// The pre-fix MHJ arm matched only bare *SeqScan / *IndexScan, so a
// Filter-wrapped table contributed NO scanEntry and — the damaging half —
// never advanced `off`. That corrupts the map twice over: every table to
// the wrapped one's RIGHT is registered at an offset short by the wrapped
// table's width, and the wrapped table itself is absent from scanMap so
// its own columns fall through with their FROM-cumulative index intact.
//
// This is the TPC-DS Q47 wrong-answer shape. Q47's 4-way join
// (item, store_sales, date_dim, store) packs date_dim/store/store_sales
// into an MHJ, and its multi-column OR disjunction
// (`d_year=2000 OR (d_year=1999 AND d_moy=12) OR ...`) is exactly the
// single-source conjunct that gets pushed into the date_dim leaf. With
// date_dim skipped, `d_year` kept FROM-cumulative index 51 and landed on
// store's `s_county`, while `s_store_sk` was remapped to 0 and read
// date_dim's `d_date_sk`. The row COUNT stayed correct (332240, equal to
// PG) because only the top projection was misremapped — which is why no
// row-count gate ever caught it.
//
// The layout below reproduces that structure with minimal widths: FROM
// order A,B,C but MHJ order C,A,B, with C wrapped in a Filter.
func TestBuildBindingsPosMapSeesThroughFilteredMHJLeaf(t *testing.T) {
	mkTable := func(name string, n int) *catalog.Table {
		cols := make([]catalog.Column, n)
		for i := range cols {
			cols[i] = catalog.Column{Name: name + string(rune('0'+i)), Type: catalog.Type{Name: "int4"}}
		}
		return &catalog.Table{Name: name, Columns: cols}
	}
	mkScan := func(tbl *catalog.Table) *SeqScan {
		sc := make(Schema, len(tbl.Columns))
		for i, c := range tbl.Columns {
			sc[i] = SchemaColumn{Name: c.Name, Type: c.Type}
		}
		return &SeqScan{Table: tbl, schema: sc}
	}

	tblA := mkTable("a", 2)
	tblB := mkTable("b", 3)
	tblC := mkTable("c", 4)

	scanA, scanB, scanC := mkScan(tblA), mkScan(tblB), mkScan(tblC)

	// FROM-clause order A,B,C → cumulative offsets 0, 2, 5.
	bindings := []rangeBinding{
		{table: tblA, offset: 0},
		{table: tblB, offset: 2},
		{table: tblC, offset: 5},
	}

	// MHJ lays the tables out in a DIFFERENT (e.g. OID-sorted) order —
	// C, A, B — and C carries a pushed-down single-source filter.
	// True MHJ-output offsets are therefore C=0, A=4, B=6.
	mhj := &MultiHashJoin{
		Tables: []Node{
			&Filter{Child: scanC, Predicate: &BooleanConst{Value: true}},
			scanA,
			scanB,
		},
	}

	posMap := buildBindingsPosMap(mhj, bindings)
	if posMap == nil {
		t.Fatal("buildBindingsPosMap declined a Filter-wrapped MHJ leaf; " +
			"remapTopProjection would then leave the projection unremapped")
	}

	cases := []struct {
		what     string
		from     int // FROM-cumulative index
		wantMHJ  int // expected MHJ-output index
		preFixWa int // what the pre-fix code produced, for the failure message
	}{
		{"a's first column", 0, 4, 0},
		{"a's second column", 1, 5, 1},
		{"b's first column", 2, 6, 2},
		{"c's first column", 5, 0, 5},
		{"c's last column", 8, 3, 8},
	}
	for _, tc := range cases {
		if got := posMap(tc.from); got != tc.wantMHJ {
			t.Errorf("posMap(%d) [%s] = %d, want %d (pre-fix produced %d — "+
				"the Filter-wrapped leaf was skipped without advancing off)",
				tc.from, tc.what, got, tc.wantMHJ, tc.preFixWa)
		}
	}
}

// TestBuildBindingsPosMapBareMHJLeavesUnchanged pins that the M0125-0013
// change is a strict generalisation: bare *SeqScan leaves must still map
// exactly as they did before, including when the MHJ order differs from
// FROM order. Without this, the fix could silently alter every existing
// MHJ plan rather than only the Filter-wrapped shape.
func TestBuildBindingsPosMapBareMHJLeavesUnchanged(t *testing.T) {
	mkTable := func(name string, n int) *catalog.Table {
		cols := make([]catalog.Column, n)
		for i := range cols {
			cols[i] = catalog.Column{Name: name + string(rune('0'+i)), Type: catalog.Type{Name: "int4"}}
		}
		return &catalog.Table{Name: name, Columns: cols}
	}
	mkScan := func(tbl *catalog.Table) *SeqScan {
		sc := make(Schema, len(tbl.Columns))
		for i, c := range tbl.Columns {
			sc[i] = SchemaColumn{Name: c.Name, Type: c.Type}
		}
		return &SeqScan{Table: tbl, schema: sc}
	}

	tblA, tblB, tblC := mkTable("a", 2), mkTable("b", 3), mkTable("c", 4)
	bindings := []rangeBinding{
		{table: tblA, offset: 0},
		{table: tblB, offset: 2},
		{table: tblC, offset: 5},
	}
	mhj := &MultiHashJoin{Tables: []Node{mkScan(tblC), mkScan(tblA), mkScan(tblB)}}

	posMap := buildBindingsPosMap(mhj, bindings)
	if posMap == nil {
		t.Fatal("buildBindingsPosMap returned nil for an all-bare-scan MHJ")
	}
	for from, want := range map[int]int{0: 4, 1: 5, 2: 6, 5: 0, 8: 3} {
		if got := posMap(from); got != want {
			t.Errorf("posMap(%d) = %d, want %d", from, got, want)
		}
	}
}
