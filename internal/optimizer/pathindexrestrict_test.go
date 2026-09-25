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

	// A one-column prefix probe uses Key. The producer declines it here —
	// i_flag is nullable, and the byte-key btree stores no entry with a NULL
	// key column — so the lowering is fed the prefix clauses directly.
	leaf1 := &Filter{Child: &SeqScan{Table: tbl, schema: schema}, Predicate: eqSK, LeafLocal: true}
	rel.baseLeaf = leaf1
	if got := consumingIndexClauses(cat, tbl, composite, extractFilterConjuncts(leaf1)); got != nil {
		t.Fatalf("a probe leaving nullable i_flag unbound must be declined, got %d clauses", len(got))
	}
	p.IndexClauses = restrictionEqualityPrefix(cat, tbl, composite, extractFilterConjuncts(leaf1))
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
	if scan == nil || len(scan.SAOPKeys) != 2 || scan.Key != nil || len(scan.Keys) != 0 || scan.HighKey != nil {
		t.Fatalf("want a 2-descent SAOP IndexScan, got %+v", scan)
	}
	// On the composite (i_item_sk, i_flag) the `i_flag > 1` bound joins the
	// probe — PG's own choice here, `Index Cond: ((i_item_sk = ANY (...)) AND
	// (i_flag > 1))`; on the single-column pkey there is no second column.
	if hasBound := scan.LowKey != nil; hasBound != (scan.Index.Name == "idx_item_sk_flag") {
		t.Fatalf("%s: a second-column bound belongs exactly to the composite index (LowKey=%v)", scan.Index.Name, scan.LowKey)
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

// Slice 2b: behind an equality prefix, range bounds on the NEXT index column
// become IndexScan.RangePrefix + LowKey/HighKey (PG's
// `Index Cond: ((i_item_sk = 2) AND (i_flag > 5))`). The prefix equality is
// dropped from the Filter; the bound stays there as a recheck (an open bound
// runs to the prefix's padded upper bound, past NULLs in the bounded column).
func TestRestrictionPrefixRangeIndexScanOnJointreePipeline(t *testing.T) {

	c := catalog.NewInMemory()
	tbl, err := c.CreateTable(parser.ObjectName{Name: "pr"}, []catalog.Column{
		{Name: "a", Type: catalog.Type{Name: "int4"}},
		{Name: "b", Type: catalog.Type{Name: "int4"}},
		{Name: "c", Type: catalog.Type{Name: "int4"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateIndex(parser.ObjectName{Name: "pr_ab"}, tbl, []string{"a", "b"}, false, "btree", false); err != nil {
		t.Fatal(err)
	}
	tbl.Stats = &catalog.TableStats{RowCount: 100000, Pages: 10000, Analyzed: true}

	node, err := Plan(parseOne(t, "SELECT c FROM pr WHERE a = 7 AND b > 90"), c)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	scan := findIndexScan(node)
	if scan == nil {
		t.Fatal("want an IndexScan on pr_ab")
	}
	if len(scan.RangePrefix) != 1 || scan.LowKey == nil || scan.LowOp != parser.OpGt || scan.HighKey != nil ||
		scan.Key != nil || len(scan.Keys) != 0 {
		t.Fatalf("want RangePrefix=[7] + LowKey > 90, got prefix=%d lo=%v/%v hi=%v key=%v keys=%d",
			len(scan.RangePrefix), scan.LowKey, scan.LowOp, scan.HighKey, scan.Key, len(scan.Keys))
	}
	f := findFilterOver(node, scan)
	if f == nil {
		t.Fatal("the range bound must stay as a Filter recheck")
	}
	if bin, ok := f.Predicate.(*BinaryOp); !ok || bin.Op != parser.OpGt {
		t.Fatalf("want the Filter to be exactly b > 90 (the prefix equality dropped), got %T", f.Predicate)
	}
}

// matchBitmapIndexQuals binds a GAPLESS leading prefix only: an equality on
// the second column with the first unbound is not an index qual (it used to
// produce a clause list that createBitmapIndexScanPlan panicked on).
func TestMatchBitmapIndexQualsIsGapless(t *testing.T) {
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
	eqFlag := &BinaryOp{Op: parser.OpEq, Left: &ColumnRef{Index: 2, Name: "i_flag"}, Right: &IntegerConst{Value: 7}}
	if got, _ := matchBitmapIndexQuals(composite, tbl, []Expr{eqFlag}, &scanIdentity{}); len(got) != 0 {
		t.Fatalf("i_flag = 7 alone binds no prefix of (i_item_sk, i_flag); got %d clauses", len(got))
	}
}

// SAOP + range: `a IN (7, 8) AND b > 90` on (a, b) plans one IndexScan with
// SAOPKeys on a and the bound on b (PG `Index Cond: ((a = ANY (...)) AND
// (b > 90))`); the bound stays in the Filter as a recheck, the IN does not.
func TestRestrictionSAOPPlusRangeOnJointreePipeline(t *testing.T) {

	c := prTestCatalog(t, []string{"a", "b"}, true)
	node, err := Plan(parseOne(t, "SELECT c FROM pr WHERE a IN (7, 8) AND b > 90"), c)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	scan := findIndexScan(node)
	if scan == nil || len(scan.SAOPKeys) != 2 || scan.LowKey == nil || scan.LowOp != parser.OpGt || scan.HighKey != nil {
		t.Fatalf("want SAOPKeys=[7 8] + LowKey > 90, got %+v", scan)
	}
	f := findFilterOver(node, scan)
	if f == nil {
		t.Fatal("the bound must stay as a Filter recheck")
	}
	if _, isIn := f.Predicate.(*InExpr); isIn {
		t.Fatal("the IN is applied by the descents; it must not stay in the Filter")
	}
}

// The byte-key btree stores no entry with a NULL key column, so a probe that
// leaves a NULLABLE key column unbound would miss rows: on (a, b, c) with c
// nullable, `a = 7 AND b > 90` must not use the index; with c NOT NULL it may.
func TestRestrictionProbeDeclinesUnboundNullableKeyColumn(t *testing.T) {

	for _, cNotNull := range []bool{false, true} {
		c := prTestCatalog(t, []string{"a", "b", "c"}, cNotNull)
		node, err := Plan(parseOne(t, "SELECT c FROM pr WHERE a = 7 AND b > 90"), c)
		if err != nil {
			t.Fatalf("Plan: %v", err)
		}
		scan := findIndexScan(node)
		if !cNotNull && scan != nil {
			t.Fatalf("c nullable: an index probe binding (a, b) would miss (7, 95, NULL); got IndexScan %s", scan.Index.Name)
		}
		if cNotNull && (scan == nil || len(scan.RangePrefix) != 1) {
			t.Fatalf("c NOT NULL: want the prefix+range probe, got %+v", scan)
		}
	}
}

// prTestCatalog builds pr(a, b, c int4) — c NOT NULL when cNotNull — with one
// btree index on idxCols and measured 100k-row statistics.
func prTestCatalog(t *testing.T, idxCols []string, cNotNull bool) *catalog.InMemory {
	t.Helper()
	c := catalog.NewInMemory()
	tbl, err := c.CreateTable(parser.ObjectName{Name: "pr"}, []catalog.Column{
		{Name: "a", Type: catalog.Type{Name: "int4"}},
		{Name: "b", Type: catalog.Type{Name: "int4"}},
		{Name: "c", Type: catalog.Type{Name: "int4"}, NotNull: cNotNull},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateIndex(parser.ObjectName{Name: "pr_idx"}, tbl, idxCols, false, "btree", false); err != nil {
		t.Fatal(err)
	}
	tbl.Stats = &catalog.TableStats{RowCount: 100000, Pages: 10000, Analyzed: true}
	return c
}
