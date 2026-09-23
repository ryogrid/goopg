package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// restrictionEqualityPrefix binds a GAPLESS leading prefix of the index to the
// leaf's `col = const` conjuncts (btree amoptionalkey, build_index_paths): a
// bound second column behind an unbound first one is not an index qual, and a
// non-equality or boolean-constant conjunct never binds. M0145-0029 slice 1.
func TestRestrictionEqualityPrefix(t *testing.T) {
	cat := saopFixture(t)
	tbl, ok := cat.LookupTable(parser.ObjectName{Name: "item"})
	if !ok {
		t.Fatal("item missing")
	}
	var composite *catalog.Index
	for _, idx := range cat.IndexesOnTable(tbl) {
		if idx.Name == "idx_item_sk_flag" {
			composite = idx
		}
	}
	if composite == nil {
		t.Fatal("idx_item_sk_flag missing")
	}
	col := func(i int) *ColumnRef { return &ColumnRef{Index: i, Name: tbl.Columns[i].Name} }
	eqSK := &BinaryOp{Op: parser.OpEq, Left: col(0), Right: &IntegerConst{Value: 2}}
	eqFlag := &BinaryOp{Op: parser.OpEq, Left: &IntegerConst{Value: 7}, Right: col(2)} // const on the left
	gtSK := &BinaryOp{Op: parser.OpGt, Left: col(0), Right: &IntegerConst{Value: 2}}
	eqBool := &BinaryOp{Op: parser.OpEq, Left: col(0), Right: &BooleanConst{Value: true}}

	if got := restrictionEqualityPrefix(cat, tbl, composite, []Expr{eqFlag, eqSK}); len(got) != 2 ||
		got[0].indexCol != 0 || got[0].local != Expr(eqSK) || got[1].indexCol != 1 || got[1].local != Expr(eqFlag) {
		t.Fatalf("both columns bound: want [sk, flag] in index order, got %+v", got)
	}
	if got := restrictionEqualityPrefix(cat, tbl, composite, []Expr{eqFlag}); len(got) != 0 {
		t.Fatalf("second column alone must not bind (gapless prefix), got %d clauses", len(got))
	}
	if got := restrictionEqualityPrefix(cat, tbl, composite, []Expr{eqSK}); len(got) != 1 {
		t.Fatalf("leading column alone is a 1-column prefix, got %d clauses", len(got))
	}
	if got := restrictionEqualityPrefix(cat, tbl, composite, []Expr{gtSK, eqBool}); len(got) != 0 {
		t.Fatalf("range / boolean-constant conjuncts must not bind as equality keys, got %d", len(got))
	}
}

// The lowering reinstates the leaf's Filter WITHOUT the conjuncts the probe
// already applies (PG's qpqual excludes quals redundant with the index quals),
// omitting a wrapper that is left empty and keeping LeafLocal.
func TestRewrapLeafDroppingRemovesConsumedConjuncts(t *testing.T) {
	a := &BinaryOp{Op: parser.OpEq, Left: &ColumnRef{Index: 0}, Right: &IntegerConst{Value: 1}}
	b := &BinaryOp{Op: parser.OpGt, Left: &ColumnRef{Index: 1}, Right: &IntegerConst{Value: 5}}
	inner := &Filter{Child: &SeqScan{}, Predicate: &BinaryOp{Op: parser.OpAnd, Left: a, Right: b}, LeafLocal: true}
	outer := &Filter{Child: inner, Predicate: a, LeafLocal: true}
	scan := &IndexScan{}

	got := rewrapLeafDropping(outer, scan, map[Expr]bool{a: true})
	f, ok := got.(*Filter)
	if !ok || f.Child != Node(scan) || f.Predicate != Expr(b) || !f.LeafLocal {
		t.Fatalf("want one LeafLocal Filter{b} directly over the scan, got %T", got)
	}
	if got := rewrapLeafDropping(&Filter{Child: &SeqScan{}, Predicate: a}, scan, map[Expr]bool{a: true}); got != Node(scan) {
		t.Fatalf("a wrapper left empty must be omitted, got %T", got)
	}
}

// An index-only path over a FILTERED leaf is admitted only when its
// equality-prefix index clauses consume every local conjunct: a residual qual
// would have to be re-evaluated over the narrowed schema, which the lowering
// cannot express. M0145-0029 slice 3.
func TestConsumingIndexClausesRequiresEveryConjunct(t *testing.T) {
	cat := saopFixture(t)
	tbl, ok := cat.LookupTable(parser.ObjectName{Name: "item"})
	if !ok {
		t.Fatal("item missing")
	}
	byName := map[string]*catalog.Index{}
	for _, idx := range cat.IndexesOnTable(tbl) {
		byName[idx.Name] = idx
	}
	composite, pkey := byName["idx_item_sk_flag"], byName["item_pkey"]
	if composite == nil || pkey == nil {
		t.Fatal("fixture indexes missing")
	}
	col := func(i int) *ColumnRef { return &ColumnRef{Index: i, Name: tbl.Columns[i].Name} }
	eqSK := &BinaryOp{Op: parser.OpEq, Left: col(0), Right: &IntegerConst{Value: 2}}
	eqFlag := &BinaryOp{Op: parser.OpEq, Left: col(2), Right: &IntegerConst{Value: 7}}
	gtFlag := &BinaryOp{Op: parser.OpGt, Left: col(2), Right: &IntegerConst{Value: 7}}

	if got := consumingIndexClauses(cat, tbl, composite, []Expr{eqFlag, eqSK}); len(got) != 2 {
		t.Fatalf("both conjuncts bound by the composite: want 2 clauses, got %d", len(got))
	}
	if got := consumingIndexClauses(cat, tbl, pkey, []Expr{eqSK}); len(got) != 1 {
		t.Fatalf("the single conjunct bound by the pkey: want 1 clause, got %d", len(got))
	}
	if got := consumingIndexClauses(cat, tbl, pkey, []Expr{eqSK, eqFlag}); got != nil {
		t.Fatalf("i_flag = 7 is a residual on item_pkey; want nil, got %d clauses", len(got))
	}
	if got := consumingIndexClauses(cat, tbl, composite, []Expr{eqSK, gtFlag}); got != nil {
		t.Fatalf("a range conjunct is not consumed by the equality prefix; want nil, got %d clauses", len(got))
	}
}

// The index-only lowering turns the consumed clauses into the probe (Key for
// one, Keys for several — the executor pads a short prefix) and leaves no
// leaf Filter behind: every conjunct was an index qual, as PG's Index Only
// Scan with `Index Cond` and no Filter.
func TestIndexOnlyScanPlanCarriesIndexQuals(t *testing.T) {
	cat := saopFixture(t)
	tbl, ok := cat.LookupTable(parser.ObjectName{Name: "item"})
	if !ok {
		t.Fatal("item missing")
	}
	var composite *catalog.Index
	for _, idx := range cat.IndexesOnTable(tbl) {
		if idx.Name == "idx_item_sk_flag" {
			composite = idx
		}
	}
	schema := make(Schema, len(tbl.Columns))
	for i, c := range tbl.Columns {
		schema[i] = SchemaColumn{Name: c.Name}
	}
	col := func(i int) *ColumnRef { return &ColumnRef{Index: i, Name: tbl.Columns[i].Name} }
	eqSK := &BinaryOp{Op: parser.OpEq, Left: col(0), Right: &IntegerConst{Value: 2}}
	eqFlag := &BinaryOp{Op: parser.OpEq, Left: col(2), Right: &IntegerConst{Value: 7}}
	leaf := &Filter{Child: &SeqScan{Table: tbl, schema: schema},
		Predicate: &BinaryOp{Op: parser.OpAnd, Left: eqSK, Right: eqFlag}, LeafLocal: true}
	rel := newRelOptInfo(1, 1000, 32)
	rel.baseLeaf = leaf

	clauses := consumingIndexClauses(cat, tbl, composite, extractFilterConjuncts(leaf))
	if len(clauses) != 2 {
		t.Fatalf("fixture: want 2 consuming clauses, got %d", len(clauses))
	}
	covered, _ := indexCoversColumns(composite, []catalog.Column{tbl.Columns[0], tbl.Columns[2]})
	p := &Path{Kind: PathIndexScan, Rel: rel, IndexInfo: composite, IndexScanDir: ForwardScanDirection,
		IndexOnly: true, IndexOnlyCovered: covered, IndexClauses: clauses}
	ios, ok := createIndexScanPlan(p).(*IndexOnlyScan)
	if !ok {
		t.Fatalf("want a bare *IndexOnlyScan (no residual Filter), got %T", createIndexScanPlan(p))
	}
	if len(ios.Keys) != 2 || ios.Keys[0] != clauses[0].key || ios.Keys[1] != clauses[1].key || ios.Key != nil {
		t.Fatalf("want Keys = [sk key, flag key] in index order, got Key=%v Keys=%v", ios.Key, ios.Keys)
	}

	// A one-column prefix probe uses Key.
	leaf1 := &Filter{Child: &SeqScan{Table: tbl, schema: schema}, Predicate: eqSK, LeafLocal: true}
	rel.baseLeaf = leaf1
	p.IndexClauses = consumingIndexClauses(cat, tbl, composite, extractFilterConjuncts(leaf1))
	if ios, ok := createIndexScanPlan(p).(*IndexOnlyScan); !ok || ios.Key == nil || ios.Keys != nil {
		t.Fatalf("one-column prefix: want Key set and Keys nil, got %#v", createIndexScanPlan(p))
	}
}

// Slice 2: on the jointree pipeline a single-relation range band over an
// indexed column is planned by the search's restriction producer as a range
// IndexScan — LowKey/HighKey with the ORIGINAL strictness (a mirrored
// `const op col` is flipped to canonical form) — and, on a single-column
// index, with both bounds dropped from the reinstated Filter (PG's qpqual
// excludes quals redundant with the index quals).
func TestRestrictionRangeIndexScanOnJointreePipeline(t *testing.T) {
	prev := jointreePipeline
	jointreePipeline = true
	t.Cleanup(func() { jointreePipeline = prev })

	c := saopFixture(t)
	item, ok := c.LookupTable(parser.ObjectName{Name: "item"})
	if !ok {
		t.Fatal("item missing")
	}
	// A measured 100k-row table: the default range-band selectivity then
	// makes the index probe cheaper than the seq scan, as PG would find.
	item.Stats = &catalog.TableStats{RowCount: 100000, Pages: 10000, Analyzed: true}
	cases := []struct {
		sql           string
		lowOp, highOp parser.OpCode
		lowVal, hiVal int64
	}{
		{"SELECT i_item_id FROM item WHERE i_item_sk > 2 AND i_item_sk < 5", parser.OpGt, parser.OpLt, 2, 5},
		{"SELECT i_item_id FROM item WHERE 5 >= i_item_sk AND 2 <= i_item_sk", parser.OpGe, parser.OpLe, 2, 5},
		{"SELECT i_item_id FROM item WHERE i_item_sk BETWEEN 2 AND 5", parser.OpGe, parser.OpLe, 2, 5},
	}
	for _, tc := range cases {
		node, err := Plan(parseOne(t, tc.sql), c)
		if err != nil {
			t.Fatalf("%s: Plan: %v", tc.sql, err)
		}
		scan := findIndexScan(node)
		if scan == nil {
			t.Fatalf("%s: want a range IndexScan, got root %T", tc.sql, node)
		}
		lo, lok := scan.LowKey.(*IntegerConst)
		hi, hok := scan.HighKey.(*IntegerConst)
		if (scan.Index.Name != "item_pkey" && scan.Index.Name != "idx_item_sk_flag") || !lok || !hok || lo.Value != tc.lowVal || hi.Value != tc.hiVal ||
			scan.LowOp != tc.lowOp || scan.HighOp != tc.highOp || scan.Key != nil || len(scan.Keys) != 0 {
			t.Fatalf("%s: got IndexScan %s lo=%v/%v hi=%v/%v", tc.sql, scan.Index.Name, scan.LowKey, scan.LowOp, scan.HighKey, scan.HighOp)
		}
		// Single-column index: both bounds dropped from the Filter. Composite
		// (the two tie on cost here): the bounds stay as a recheck, since an
		// exclusive padded bound can admit trailing-column entries.
		f := findFilterOver(node, scan)
		if len(scan.Index.Columns) == 1 && f != nil {
			t.Fatalf("%s: both bounds are index quals on a single-column index; want no Filter over the scan, got %v", tc.sql, f.Predicate)
		}
		if len(scan.Index.Columns) > 1 && f == nil {
			t.Fatalf("%s: a composite-index range must keep its bounds as a Filter recheck", tc.sql)
		}
	}
}

// findFilterOver returns the *Filter whose child is scan, or nil.
func findFilterOver(n Node, scan Node) *Filter {
	for n != nil {
		switch v := n.(type) {
		case *Filter:
			if v.Child == scan {
				return v
			}
			n = v.Child
		case *Project:
			n = v.Child
		case *Sort:
			n = v.Child
		case *Limit:
			n = v.Child
		default:
			return nil
		}
	}
	return nil
}

// Slice 4: on the jointree pipeline a leading-column `IN (…)` is planned by
// the restriction producer as a multi-descent IndexScan (SAOPKeys, one per
// element), the IN dropped from the reinstated Filter and any other conjunct
// kept there; the gates trySAOPIndexScan applies (NOT IN, ALL, `!= ANY`, a
// non-column operand) decline, leaving no SAOP probe.
func TestRestrictionSAOPIndexScanOnJointreePipeline(t *testing.T) {
	prev := jointreePipeline
	jointreePipeline = true
	t.Cleanup(func() { jointreePipeline = prev })

	c := saopFixture(t)
	item, ok := c.LookupTable(parser.ObjectName{Name: "item"})
	if !ok {
		t.Fatal("item missing")
	}
	item.Stats = &catalog.TableStats{RowCount: 100000, Pages: 10000, Analyzed: true}

	node, err := Plan(parseOne(t, "SELECT i_item_id FROM item WHERE i_item_sk IN (2, 3) AND i_flag > 1"), c)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	scan := findIndexScan(node)
	if scan == nil || len(scan.SAOPKeys) != 2 || scan.Key != nil || len(scan.Keys) != 0 || scan.LowKey != nil || scan.HighKey != nil {
		t.Fatalf("want a 2-descent SAOP IndexScan and no other probe shape, got %+v", scan)
	}
	f := findFilterOver(node, scan)
	if f == nil {
		t.Fatal("i_flag > 1 is not an index qual; want it kept as a Filter over the scan")
	}
	if _, isIn := f.Predicate.(*InExpr); isIn {
		t.Fatalf("the IN is applied by the descents; want it dropped from the Filter, got %v", f.Predicate)
	}
	if bin, ok := f.Predicate.(*BinaryOp); !ok || bin.Op != parser.OpGt {
		t.Fatalf("want the Filter to be exactly i_flag > 1, got %T", f.Predicate)
	}

	for _, q := range []string{
		"SELECT i_item_id FROM item WHERE i_item_sk NOT IN (2, 3)",
		"SELECT i_item_id FROM item WHERE i_item_sk = ALL (ARRAY[2, 3])",
		"SELECT i_item_id FROM item WHERE i_item_sk != ANY (ARRAY[2, 3])",
		"SELECT i_item_id FROM item WHERE i_item_sk + 1 IN (2, 3)",
	} {
		node, err := Plan(parseOne(t, q), c)
		if err != nil {
			t.Fatalf("%s: Plan: %v", q, err)
		}
		if s := findIndexScan(node); s != nil && len(s.SAOPKeys) > 0 {
			t.Fatalf("%s: must not become a SAOP probe", q)
		}
	}
}
