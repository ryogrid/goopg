package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// ── Unit tests for ExtractLikePrefix ─────────────────────────────────────────

func TestExtractLikePrefix(t *testing.T) {
	cases := []struct {
		pattern    string
		wantPrefix string
		wantExact  bool
		wantOK     bool
	}{
		// Prefix patterns
		{"foo%", "foo", false, true},
		{"f%", "f", false, true},
		{"foo%bar", "foo", false, true},
		{"foo%bar%", "foo", false, true},
		// Exact match (no wildcards)
		{"foo", "foo", true, true},
		{"", "", true, false}, // empty pattern → no useful prefix (ok=false); exact is irrelevant
		// No prefix possible (starts with wildcard)
		{"%foo", "", false, false},
		{"_foo", "", false, false},
		{"%%", "", false, false},
		// Underscore wildcard stops prefix
		{"fo_bar%", "fo", false, true},
		// Escape sequences
		{"foo\\%bar", "foo%bar", true, true},  // escaped %, full literal
		{"foo\\_bar", "foo_bar", true, true},  // escaped _, full literal
		{"foo\\\\bar%", "foo\\bar", false, true}, // escaped backslash, then prefix
		{"\\%foo%", "%foo", false, true},      // starts with escaped %
		// Single-char prefix
		{"a%", "a", false, true},
	}
	for _, tc := range cases {
		prefix, exact, ok := ExtractLikePrefix(tc.pattern)
		if ok != tc.wantOK || exact != tc.wantExact || prefix != tc.wantPrefix {
			t.Errorf("ExtractLikePrefix(%q) = (%q, %v, %v), want (%q, %v, %v)",
				tc.pattern, prefix, exact, ok,
				tc.wantPrefix, tc.wantExact, tc.wantOK)
		}
	}
}

// ── Unit tests for IncrementString ───────────────────────────────────────────

func TestIncrementString(t *testing.T) {
	cases := []struct {
		in     string
		want   string
		wantOK bool
	}{
		{"foo", "fop", true},
		{"a", "b", true},
		{"az", "a{", true},  // last byte 'z'(122) → '{'(123); bytewise C-collation
		{"", "", false},    // empty string has no successor
		{"\xff", "", false},       // all 0xFF
		{"\xff\xff", "", false},   // all 0xFF
		{"a\xff", "b", true},      // last byte 0xFF → roll, increment 'a'
		{"foo\xff", "fop", true},  // 'o' can be incremented
		{"PROMO", "PROMP", true},  // TPC-H Q14 pattern prefix
	}
	for _, tc := range cases {
		got, ok := IncrementString(tc.in)
		if ok != tc.wantOK || got != tc.want {
			t.Errorf("IncrementString(%q) = (%q, %v), want (%q, %v)",
				tc.in, got, ok, tc.want, tc.wantOK)
		}
	}
}

// ── DoD integration tests ─────────────────────────────────────────────────────

// TestLikeToRangeDoD_PrefixPattern is the primary DoD test:
// SELECT * FROM t WHERE label LIKE 'foo%' with a B-tree index on label
// must produce a Filter(IndexScan{LowKey:'foo', HighKey:'fop'}, pred).
func TestLikeToRangeDoD_PrefixPattern(t *testing.T) {
	cat := catalog.NewInMemory()
	tbl, err := cat.CreateTable(parser.ObjectName{Name: "t"}, []catalog.Column{
		{Name: "id", Type: catalog.Type{Name: "int4"}},
		{Name: "label", Type: catalog.Type{Name: "varchar"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cat.CreateIndex(parser.ObjectName{Name: "t_label_idx"}, tbl,
		[]string{"label"}, false, "btree", false); err != nil {
		t.Fatal(err)
	}

	stmts, err := parser.Parse("SELECT * FROM t WHERE label LIKE 'foo%'")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	node, err := Plan(stmts[0], cat)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}

	// Expected: Project(Filter(IndexScan{LowKey, HighKey}, pred)) — unwrap Project.
	if proj, ok := node.(*Project); ok {
		node = proj.Child
	}
	filt, ok := node.(*Filter)
	if !ok {
		t.Fatalf("root (below Project): want *Filter, got %T", node)
	}
	idxScan, ok := filt.Child.(*IndexScan)
	if !ok {
		t.Fatalf("Filter.Child: want *IndexScan, got %T — LIKE range injection did not fire", filt.Child)
	}
	// LowKey must be 'foo' (inclusive lower bound from col >= 'foo')
	if lo, ok := idxScan.LowKey.(*StringConst); !ok || lo.Value != "foo" {
		t.Errorf("LowKey: want StringConst('foo'), got %T %v", idxScan.LowKey, idxScan.LowKey)
	}
	// HighKey must be 'fop' (exclusive upper bound from col < 'fop')
	if hi, ok := idxScan.HighKey.(*StringConst); !ok || hi.Value != "fop" {
		t.Errorf("HighKey: want StringConst('fop'), got %T %v", idxScan.HighKey, idxScan.HighKey)
	}
}

// TestLikeToRangeQ20Shape (M0054-0009 audit) confirms M0051-0004's
// prefix→range translation activates correctly for TPC-H Q20's
// `p_name LIKE 'forest%'` shape WHEN an index on p_name exists.
// This pins the integration: the rewriter is correct;
// HammerDB's standard schema does NOT create an index on p_name
// (see analysis/tpch-additional-indexes.md), which is why the
// production EXPLAIN for Q20 shows SeqScan on part. The audit
// verdict is therefore: M0051-0004 is correct; the production
// gap is the missing index, which is by-design upstream and
// out of scope for M0054 (would require diverging from HammerDB
// schema fidelity).
func TestLikeToRangeQ20Shape(t *testing.T) {
	cat := catalog.NewInMemory()
	tbl, err := cat.CreateTable(parser.ObjectName{Name: "part"}, []catalog.Column{
		{Name: "p_partkey", Type: catalog.Type{Name: "int4"}, NotNull: true},
		{Name: "p_name", Type: catalog.Type{Name: "varchar", Args: []int64{55}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Synthetic index on p_name — NOT created by HammerDB by
	// default. This test asserts the planner WOULD pick IndexScan
	// if such an index were present, validating the rewriter for
	// the Q20 expression shape.
	if _, err := cat.CreateIndex(parser.ObjectName{Name: "idx_part_name"}, tbl,
		[]string{"p_name"}, false, "btree", false); err != nil {
		t.Fatal(err)
	}
	stmts, err := parser.Parse("SELECT p_partkey FROM part WHERE p_name LIKE 'forest%'")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	node, err := Plan(stmts[0], cat)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if proj, ok := node.(*Project); ok {
		node = proj.Child
	}
	filt, ok := node.(*Filter)
	if !ok {
		t.Fatalf("expected Filter (below Project), got %T", node)
	}
	idx, ok := filt.Child.(*IndexScan)
	if !ok {
		t.Fatalf("expected Filter(IndexScan), got Filter(%T) — Q20 LIKE-prefix range integration broken", filt.Child)
	}
	if lo, ok := idx.LowKey.(*StringConst); !ok || lo.Value != "forest" {
		t.Errorf("LowKey: want 'forest', got %T %v", idx.LowKey, idx.LowKey)
	}
	if hi, ok := idx.HighKey.(*StringConst); !ok || hi.Value != "fores\x75" /* 'forest'+1 → "forest"[:5]+'u' = "foresu" */ {
		// Strict-greater successor of 'forest' is 'foresu' (last byte 't'=0x74 → 0x75='u').
		// Re-check by reading the actual value if literal mismatch.
		t.Errorf("HighKey: want 'foresu' (forest+1), got %T %v", idx.HighKey, idx.HighKey)
	}
	if idx.Index == nil || idx.Index.Name != "idx_part_name" {
		t.Errorf("expected idx_part_name on inner; got %v", idx.Index)
	}
}

// TestLikeToRangeQ20ShapeNoIndex (M0054-0009 negative case) —
// when no index on p_name exists, the plan stays SeqScan. This is
// the production state with HammerDB's stock schema. The result
// is correct (LIKE filter applied via Filter on top of SeqScan);
// it is NOT a planner bug.
func TestLikeToRangeQ20ShapeNoIndex(t *testing.T) {
	cat := catalog.NewInMemory()
	if _, err := cat.CreateTable(parser.ObjectName{Name: "part"}, []catalog.Column{
		{Name: "p_partkey", Type: catalog.Type{Name: "int4"}, NotNull: true},
		{Name: "p_name", Type: catalog.Type{Name: "varchar", Args: []int64{55}}},
	}); err != nil {
		t.Fatal(err)
	}
	stmts, err := parser.Parse("SELECT p_partkey FROM part WHERE p_name LIKE 'forest%'")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	node, err := Plan(stmts[0], cat)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if proj, ok := node.(*Project); ok {
		node = proj.Child
	}
	filt, ok := node.(*Filter)
	if !ok {
		t.Fatalf("expected Filter (below Project), got %T", node)
	}
	if _, ok := filt.Child.(*SeqScan); !ok {
		t.Fatalf("expected Filter(SeqScan) when no index on p_name; got Filter(%T)", filt.Child)
	}
}

// TestLikeToRangeDoD_ExactPattern verifies that LIKE 'foo' (no wildcards)
// produces a range IndexScan with bounds ['foo', 'fop'). The LIKE post-filter
// ensures only exact 'foo' is returned; the range narrows the B-tree scan.
func TestLikeToRangeDoD_ExactPattern(t *testing.T) {
	cat := catalog.NewInMemory()
	tbl, err := cat.CreateTable(parser.ObjectName{Name: "t"}, []catalog.Column{
		{Name: "label", Type: catalog.Type{Name: "varchar"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cat.CreateIndex(parser.ObjectName{Name: "t_label_idx"}, tbl,
		[]string{"label"}, false, "btree", false); err != nil {
		t.Fatal(err)
	}

	stmts, err := parser.Parse("SELECT * FROM t WHERE label LIKE 'foo'")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	node, err := Plan(stmts[0], cat)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}

	// Exact pattern: LIKE 'foo' → inject ['foo', 'fop') range → Filter(IndexScan).
	if proj, ok := node.(*Project); ok {
		node = proj.Child
	}
	filt, ok := node.(*Filter)
	if !ok {
		t.Fatalf("expected Filter (below Project), got %T", node)
	}
	// Accept either IndexScan or IndexOnlyScan (single-column table uses the latter).
	var loKey, hiKey Expr
	switch is := filt.Child.(type) {
	case *IndexScan:
		loKey, hiKey = is.LowKey, is.HighKey
	case *IndexOnlyScan:
		loKey, hiKey = is.LowKey, is.HighKey
	default:
		t.Fatalf("expected Filter(IndexScan or IndexOnlyScan), got Filter(%T)", filt.Child)
	}
	if lo, ok := loKey.(*StringConst); !ok || lo.Value != "foo" {
		t.Errorf("LowKey: want 'foo', got %T %v", loKey, loKey)
	}
	if hi, ok := hiKey.(*StringConst); !ok || hi.Value != "fop" {
		t.Errorf("HighKey: want 'fop', got %T %v", hiKey, hiKey)
	}
}

// TestLikeToRangeDoD_NoPrefix verifies that LIKE '%foo%' (no derivable prefix)
// does NOT produce an IndexScan — a SeqScan or Filter(SeqScan) is expected.
func TestLikeToRangeDoD_NoPrefix(t *testing.T) {
	cat := catalog.NewInMemory()
	tbl, err := cat.CreateTable(parser.ObjectName{Name: "t"}, []catalog.Column{
		{Name: "label", Type: catalog.Type{Name: "varchar"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cat.CreateIndex(parser.ObjectName{Name: "t_label_idx"}, tbl,
		[]string{"label"}, false, "btree", false); err != nil {
		t.Fatal(err)
	}

	stmts, err := parser.Parse("SELECT * FROM t WHERE label LIKE '%foo%'")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	node, err := Plan(stmts[0], cat)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}

	// No prefix → must NOT be an IndexScan.
	checkNoIndexScan(t, node, "LIKE '%foo%' should not produce IndexScan")
}

// TestLikeToRangeDoD_UnderscoreWildcard verifies that LIKE '_foo%' (starts
// with underscore) does NOT produce an IndexScan.
func TestLikeToRangeDoD_UnderscoreWildcard(t *testing.T) {
	cat := catalog.NewInMemory()
	tbl, err := cat.CreateTable(parser.ObjectName{Name: "t"}, []catalog.Column{
		{Name: "label", Type: catalog.Type{Name: "varchar"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cat.CreateIndex(parser.ObjectName{Name: "t_label_idx"}, tbl,
		[]string{"label"}, false, "btree", false); err != nil {
		t.Fatal(err)
	}

	stmts, err := parser.Parse("SELECT * FROM t WHERE label LIKE '_foo%'")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	node, err := Plan(stmts[0], cat)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}

	checkNoIndexScan(t, node, "LIKE '_foo%' should not produce IndexScan")
}

// TestLikeToRangeDoD_NoIndex verifies that LIKE 'foo%' WITHOUT an index
// does not error — it falls back to a SeqScan+Filter.
func TestLikeToRangeDoD_NoIndex(t *testing.T) {
	cat := catalog.NewInMemory()
	_, err := cat.CreateTable(parser.ObjectName{Name: "t"}, []catalog.Column{
		{Name: "label", Type: catalog.Type{Name: "varchar"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	// No index created.

	stmts, err := parser.Parse("SELECT * FROM t WHERE label LIKE 'foo%'")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	node, err := Plan(stmts[0], cat)
	if err != nil {
		t.Fatalf("Plan (no index): %v", err)
	}

	// Should be a Filter(SeqScan), not an IndexScan.
	checkNoIndexScan(t, node, "LIKE 'foo%' without index must fall back to SeqScan")
}

// TestLikeToRangeTPCHQ14Shape verifies a pattern matching TPC-H Q14's
// p_type LIKE 'PROMO%' produces an IndexScan with PROMO / PROMP bounds.
func TestLikeToRangeTPCHQ14Shape(t *testing.T) {
	cat := catalog.NewInMemory()
	tbl, err := cat.CreateTable(parser.ObjectName{Name: "part"}, []catalog.Column{
		{Name: "p_partkey", Type: catalog.Type{Name: "int4"}},
		{Name: "p_type", Type: catalog.Type{Name: "varchar"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cat.CreateIndex(parser.ObjectName{Name: "part_type_idx"}, tbl,
		[]string{"p_type"}, false, "btree", false); err != nil {
		t.Fatal(err)
	}

	stmts, err := parser.Parse("SELECT * FROM part WHERE p_type LIKE 'PROMO%'")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	node, err := Plan(stmts[0], cat)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}

	if proj, ok := node.(*Project); ok {
		node = proj.Child
	}
	filt, ok := node.(*Filter)
	if !ok {
		t.Fatalf("root (below Project): want *Filter, got %T", node)
	}
	idxScan, ok := filt.Child.(*IndexScan)
	if !ok {
		t.Fatalf("child: want *IndexScan, got %T", filt.Child)
	}
	if lo, ok := idxScan.LowKey.(*StringConst); !ok || lo.Value != "PROMO" {
		t.Errorf("LowKey: want 'PROMO', got %T %v", idxScan.LowKey, idxScan.LowKey)
	}
	if hi, ok := idxScan.HighKey.(*StringConst); !ok || hi.Value != "PROMP" {
		t.Errorf("HighKey: want 'PROMP', got %T %v", idxScan.HighKey, idxScan.HighKey)
	}
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func checkNoIndexScan(t *testing.T, node Node, msg string) {
	t.Helper()
	switch n := node.(type) {
	case *IndexScan:
		t.Errorf("%s: got IndexScan", msg)
	case *Filter:
		if _, ok := n.Child.(*IndexScan); ok {
			t.Errorf("%s: got Filter(IndexScan)", msg)
		}
	}
}
