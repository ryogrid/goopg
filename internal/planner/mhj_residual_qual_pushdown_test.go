package planner

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// M0125-0046: a WHERE conjunct that pushOneConjunct never AND'd onto a
// join predicate sits in the residual *Filter above the MultiHashJoin,
// where neither collectMultiHashTables (mh.Filters extras only) nor
// pushSingleSourceFiltersIntoMHJTables (reads mh.Filters only) ever
// sees it. The measured cost (design doc 0125-0035 §2): `ca_state IN
// ('IL','TX','ME')` stayed on the MHJ node, customer_address was
// hashed whole (50,000 rows) and the MHJ emitted 96,562 rows for an
// 11,049-row answer. pushResidualQualsIntoMHJTables closes the gap:
// the residual conjunct is DUPLICATED (property 2) onto the owning
// Tables[i] as a leaf-local Filter, so the build side is restricted
// before hashing.

func mhjResidualTestCatalog(t *testing.T) *catalog.InMemory {
	t.Helper()
	cat := catalog.NewInMemory()
	create := func(name string, cols []catalog.Column, rows int64) {
		tbl, err := cat.CreateTable(parser.ObjectName{Name: name}, cols)
		if err != nil {
			t.Fatalf("CreateTable(%s): %v", name, err)
		}
		tbl.Stats = &catalog.TableStats{RowCount: rows}
		tbl.Stats.Columns = make([]catalog.ColumnStats, len(cols))
		for i := range cols {
			tbl.Stats.Columns[i] = catalog.ColumnStats{NDistinct: rows}
		}
	}
	create("customer", []catalog.Column{
		{Name: "c_customer_sk", Type: catalog.Type{Name: "int8"}},
		{Name: "c_current_addr_sk", Type: catalog.Type{Name: "int8"}},
		{Name: "c_current_cdemo_sk", Type: catalog.Type{Name: "int8"}},
	}, 100000)
	create("customer_address", []catalog.Column{
		{Name: "ca_address_sk", Type: catalog.Type{Name: "int8"}},
		{Name: "ca_state", Type: catalog.Type{Name: "text"}},
	}, 50000)
	create("customer_demographics", []catalog.Column{
		{Name: "cd_demo_sk", Type: catalog.Type{Name: "int8"}},
		{Name: "cd_gender", Type: catalog.Type{Name: "text"}},
	}, 1920800)
	return cat
}

func planMHJResidualQuery(t *testing.T, cat *catalog.InMemory, q string) Node {
	t.Helper()
	stmts, err := parser.Parse(q)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	plan, err := Plan(stmts[0], cat)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	return plan
}

func findMHJ(n Node) *MultiHashJoin {
	switch x := n.(type) {
	case nil:
		return nil
	case *MultiHashJoin:
		return x
	case *Filter:
		return findMHJ(x.Child)
	case *Project:
		return findMHJ(x.Child)
	case *Aggregate:
		return findMHJ(x.Child)
	case *Sort:
		return findMHJ(x.Child)
	case *Limit:
		return findMHJ(x.Child)
	case *Join:
		if m := findMHJ(x.Left); m != nil {
			return m
		}
		return findMHJ(x.Right)
	}
	return nil
}

const mhjResidualInListQuery = `select count(*)
from customer c, customer_address ca, customer_demographics cd
where c.c_current_addr_sk = ca.ca_address_sk
  and c.c_current_cdemo_sk = cd.cd_demo_sk
  and ca.ca_state in ('IL','TX','ME')`

func TestMHJResidualInListPushedToMemberScan(t *testing.T) {
	// M0127-P5.9: a legacy-rule assertion; see useLegacyEnumerator.
	useLegacyEnumerator(t)
	SetMHJPackingEnabled(true)
	defer SetMHJPackingEnabled(false)
	cat := mhjResidualTestCatalog(t)
	plan := planMHJResidualQuery(t, cat, mhjResidualInListQuery)

	mh := findMHJ(plan)
	if mh == nil {
		t.Fatal("no MultiHashJoin in plan — 3-table equi-join chain should pack")
	}
	var wrapped *Filter
	for _, tb := range mh.Tables {
		f, ok := tb.(*Filter)
		if !ok {
			continue
		}
		if ss, ok := f.Child.(*SeqScan); ok && ss.Table.Name == "customer_address" {
			wrapped = f
		}
	}
	if wrapped == nil {
		t.Fatal("customer_address member is not Filter-wrapped — residual IN-list was not pushed")
	}
	in, ok := wrapped.Predicate.(*InExpr)
	if !ok {
		t.Fatalf("pushed predicate is %T, want *InExpr", wrapped.Predicate)
	}
	if in.Plan != nil {
		t.Fatal("pushed InExpr unexpectedly carries a subquery Plan")
	}
	cr, ok := in.Operand.(*ColumnRef)
	if !ok {
		t.Fatalf("InExpr operand is %T, want *ColumnRef", in.Operand)
	}
	// Leaf-local coordinates: ca_state is customer_address column 1.
	if cr.Index != 1 || cr.Name != "ca_state" {
		t.Fatalf("pushed operand = {Index:%d Name:%q}, want leaf-local {1 ca_state}", cr.Index, cr.Name)
	}
	if !wrapped.LeafLocal {
		t.Fatal("pushed wrapper must be LeafLocal — its predicate is in leaf coordinates")
	}

	// Property 2: the conjunct is duplicated, not moved — the residual
	// Filter above the MHJ still carries the IN-list, so a decline
	// anywhere downstream can never lose the restriction.
	residual := false
	var walk func(n Node)
	walk = func(n Node) {
		switch x := n.(type) {
		case *Filter:
			if _, ok := x.Predicate.(*InExpr); ok && x.Child == Node(mh) {
				residual = true
			}
			walk(x.Child)
		case *Project:
			walk(x.Child)
		case *Aggregate:
			walk(x.Child)
		}
	}
	walk(plan)
	if !residual {
		t.Fatal("residual Filter above the MHJ no longer holds the IN-list — property 2 (duplicate, not move) violated")
	}
}

// Re-walking the pass (an enclosing scope's planSelect reaches it again
// with the subtree embedded) must not AND the same conjunct in twice —
// pushConjunctIntoSubtree's exprEqual guard owns this.
func TestMHJResidualPushIdempotent(t *testing.T) {
	// M0127-P5.9: a legacy-rule assertion; see useLegacyEnumerator.
	useLegacyEnumerator(t)
	SetMHJPackingEnabled(true)
	defer SetMHJPackingEnabled(false)
	cat := mhjResidualTestCatalog(t)
	plan := planMHJResidualQuery(t, cat, mhjResidualInListQuery)
	mh := findMHJ(plan)
	if mh == nil {
		t.Fatal("no MultiHashJoin in plan")
	}
	pushSingleSideQualsIntoInnerJoinInputs(plan)
	pushSingleSideQualsIntoInnerJoinInputs(plan)
	for _, tb := range mh.Tables {
		f, ok := tb.(*Filter)
		if !ok {
			continue
		}
		if len(splitAnd(f.Predicate)) != 1 {
			t.Fatalf("member wrapper predicate grew to %d conjuncts after re-walk, want 1", len(splitAnd(f.Predicate)))
		}
	}
}

// A conjunct spanning two member tables is not a restriction clause and
// must stay in the residual — attribution requires a UNIQUE table.
func TestMHJResidualCrossTableConjunctStaysPut(t *testing.T) {
	// M0127-P5.9: a legacy-rule assertion; see useLegacyEnumerator.
	useLegacyEnumerator(t)
	SetMHJPackingEnabled(true)
	defer SetMHJPackingEnabled(false)
	cat := mhjResidualTestCatalog(t)
	plan := planMHJResidualQuery(t, cat, `select count(*)
from customer c, customer_address ca, customer_demographics cd
where c.c_current_addr_sk = ca.ca_address_sk
  and c.c_current_cdemo_sk = cd.cd_demo_sk
  and c.c_customer_sk > ca.ca_address_sk`)
	mh := findMHJ(plan)
	if mh == nil {
		t.Fatal("no MultiHashJoin in plan")
	}
	for i, tb := range mh.Tables {
		if f, ok := tb.(*Filter); ok {
			if bin, isBin := f.Predicate.(*BinaryOp); isBin && bin.Op == parser.OpGt {
				t.Fatalf("cross-table conjunct was pushed onto Tables[%d]", i)
			}
		}
	}
}

// The two MHJ push passes compose: pushSingleSourceFiltersIntoMHJTables
// (mh.Filters, runs first) wraps the member with a LeafLocal Filter,
// and pushResidualQualsIntoMHJTables must then AND its conjunct into
// that same wrapper rather than declining on a coordinate-convention
// mismatch. This is why the mh.Filters pass stamps LeafLocal.
func TestMHJResidualComposesWithMHFiltersWrapper(t *testing.T) {
	mkTable := func(name string, n int) *catalog.Table {
		cols := make([]catalog.Column, n)
		for i := range cols {
			cols[i] = catalog.Column{Name: name + string(rune('0'+i)), Type: catalog.Type{Name: "text"}}
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
	scanA := mkScan(mkTable("a", 2))
	scanB := mkScan(mkTable("b", 3))
	scanC := mkScan(mkTable("c", 2))

	// mh.Filters: b0 = 'k' (MHJ-output index 2). Residual: b1 IN ('x','y')
	// (MHJ-output index 3). Both belong to table b.
	eq := &BinaryOp{
		Op:    parser.OpEq,
		Left:  &ColumnRef{Index: 2, Name: "b0"},
		Right: &StringConst{Value: "k"},
	}
	in := &InExpr{
		Operand: &ColumnRef{Index: 3, Name: "b1"},
		List:    []Expr{&StringConst{Value: "x"}, &StringConst{Value: "y"}},
	}
	mh := &MultiHashJoin{
		Tables:  []Node{scanA, scanB, scanC},
		Filters: []Expr{eq},
	}
	f := &Filter{Child: mh, Predicate: in}

	pushSingleSourceFiltersAfterRemap(f)
	pushSingleSideQualsIntoInnerJoinInputs(f)

	wrapper, ok := mh.Tables[1].(*Filter)
	if !ok {
		t.Fatalf("Tables[1] is %T, want *Filter from the mh.Filters push", mh.Tables[1])
	}
	if !wrapper.LeafLocal {
		t.Fatal("mh.Filters wrapper must be LeafLocal — without it the residual pass declines the AND-in")
	}
	conjuncts := splitAnd(wrapper.Predicate)
	if len(conjuncts) != 2 {
		t.Fatalf("wrapper carries %d conjuncts, want 2 (equality + IN-list)", len(conjuncts))
	}
	pushedIn, ok := conjuncts[1].(*InExpr)
	if !ok {
		t.Fatalf("second conjunct is %T, want *InExpr", conjuncts[1])
	}
	cr, ok := pushedIn.Operand.(*ColumnRef)
	if !ok || cr.Index != 1 {
		t.Fatalf("IN operand = %#v, want leaf-local index 1 (b1)", pushedIn.Operand)
	}
}
