package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

// regSortFixtureTables creates three tables whose NAME order and OID
// (creation) order deliberately DISAGREE: alphabetically "aaa_third" <
// "mmm_second" < "zzz_first", but they are created in the opposite order
// so the OIDs increase zzz_first < mmm_second < aaa_third. Any test that
// sorts by name instead of OID (the pre-fix bug) sees the reverse of the
// OID-correct order on this fixture.
func regSortFixtureTables(t *testing.T) (cat *catalog.InMemory, zzz, mmm, aaa *catalog.Table) {
	t.Helper()
	cat = catalog.NewInMemory()
	mk := func(name string) *catalog.Table {
		tbl, err := cat.CreateTable(parser.ObjectName{Name: name}, []catalog.Column{
			{Name: "a", Type: catalog.Type{Name: "int4"}},
		})
		if err != nil {
			t.Fatalf("CreateTable(%s): %v", name, err)
		}
		return tbl
	}
	zzz = mk("zzz_first")
	mmm = mk("mmm_second")
	aaa = mk("aaa_third")
	return
}

// regclassSortKey builds a single-key ORDER BY <col>::regclass SortKey over
// column index 0, matching the *optimizer.CastExpr shape sortOp.lessRows
// sees for `conrelid::regclass` (constraints.sql hunk 12's shape).
func regclassSortKey(desc bool) optimizer.SortKey {
	return optimizer.SortKey{
		Expr: &optimizer.CastExpr{
			Operand:    &optimizer.ColumnRef{Index: 0},
			TargetType: "regclass",
		},
		Desc: desc,
	}
}

func drainSortOIDs(t *testing.T, s *sortOp) []int64 {
	t.Helper()
	var got []int64
	for {
		slot, err := s.Next()
		if err == EOF {
			break
		}
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		got = append(got, slot.Row()[0].Int)
	}
	return got
}

// TestRegclassSortOrdersByOIDNotName is the FAIL-pre/PASS-post guard for
// M0134-0005aj (hunk 12): ORDER BY <oid-col>::regclass must sort by the
// underlying OID (PG's btoidcmp fallback — see operators.go's
// isRegSortFamilyTypeName doc comment), not by the rendered relation NAME.
// Pre-fix, sortOp.lessRows evaluated the full CastExpr (KindString, the
// relation name) and did a byte-wise string compare, which — on this
// fixture, constructed so OID order and NAME order disagree — produces the
// REVERSE of the expected order.
func TestRegclassSortOrdersByOIDNotName(t *testing.T) {
	cat, zzz, mmm, aaa := regSortFixtureTables(t)
	ctx := &Context{Catalog: cat}

	// Scrambled input order (not already OID-sorted), so a passing test
	// proves the sort actually reordered rows rather than leaving input
	// order untouched.
	rows := []Row{
		{NewIntDatum(int64(aaa.OID))},
		{NewIntDatum(int64(zzz.OID))},
		{NewIntDatum(int64(mmm.OID))},
	}

	s := &sortOp{
		child: &fakeBorrowSource{rows: rows},
		keys:  []optimizer.SortKey{regclassSortKey(false)},
	}
	if err := s.Open(ctx); err != nil {
		t.Fatalf("Open: %v", err)
	}
	got := drainSortOIDs(t, s)
	want := []int64{int64(zzz.OID), int64(mmm.OID), int64(aaa.OID)} // ascending OID = creation order
	if len(got) != len(want) {
		t.Fatalf("row count = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ASC order[%d] = %d, want %d (got %v, want %v — NAME-order would be %v)",
				i, got[i], want[i], got, want,
				[]int64{int64(aaa.OID), int64(mmm.OID), int64(zzz.OID)})
		}
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// TestRegclassSortDescOrdersByOIDNotName is the DESC twin: ORDER BY
// <oid-col>::regclass DESC must yield descending OID order.
func TestRegclassSortDescOrdersByOIDNotName(t *testing.T) {
	cat, zzz, mmm, aaa := regSortFixtureTables(t)
	ctx := &Context{Catalog: cat}

	rows := []Row{
		{NewIntDatum(int64(mmm.OID))},
		{NewIntDatum(int64(aaa.OID))},
		{NewIntDatum(int64(zzz.OID))},
	}

	s := &sortOp{
		child: &fakeBorrowSource{rows: rows},
		keys:  []optimizer.SortKey{regclassSortKey(true)},
	}
	if err := s.Open(ctx); err != nil {
		t.Fatalf("Open: %v", err)
	}
	got := drainSortOIDs(t, s)
	want := []int64{int64(aaa.OID), int64(mmm.OID), int64(zzz.OID)} // descending OID
	if len(got) != len(want) {
		t.Fatalf("row count = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("DESC order[%d] = %d, want %d (got %v, want %v)", i, got[i], want[i], got, want)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// TestRegclassSortMixedWithPlainKey is the "ORDER BY 1, 2" acceptance case:
// when the first (reg*-cast) key ties, the second, ordinary key must still
// break the tie exactly as before the fix — the reg*-family special-case in
// evalSortKeyValue must not interfere with a non-CastExpr sibling key.
func TestRegclassSortMixedWithPlainKey(t *testing.T) {
	cat, zzz, _, _ := regSortFixtureTables(t)
	ctx := &Context{Catalog: cat}

	// Both rows reference the SAME table (tie on key 1); key 2 (plain int
	// column, index 1) must decide the order.
	rows := []Row{
		{NewIntDatum(int64(zzz.OID)), NewIntDatum(2)},
		{NewIntDatum(int64(zzz.OID)), NewIntDatum(1)},
	}

	s := &sortOp{
		child: &fakeBorrowSource{rows: rows},
		keys: []optimizer.SortKey{
			regclassSortKey(false),
			{Expr: &optimizer.ColumnRef{Index: 1}, Desc: false},
		},
	}
	if err := s.Open(ctx); err != nil {
		t.Fatalf("Open: %v", err)
	}
	var got []int64
	for {
		slot, err := s.Next()
		if err == EOF {
			break
		}
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		got = append(got, slot.Row()[1].Int)
	}
	want := []int64{1, 2}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("mixed-key tiebreak order = %v, want %v", got, want)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// TestRegclassSortStringAndIntSourceAgree is acceptance criterion 2:
// `'name'::regclass` (string-literal source, KindString operand) and
// `<oid-col>::regclass` (int source, KindInt operand) must produce the SAME
// sort value for the same underlying relation — both branches of
// evalSortKeyValue resolve to the raw OID as a KindInt datum, so the two
// source shapes compare consistently against each other (the sibling
// divergence this slice removes; see brief M0134-0005aj).
func TestRegclassSortStringAndIntSourceAgree(t *testing.T) {
	cat, zzz, _, _ := regSortFixtureTables(t)
	ctx := &Context{Catalog: cat}

	ceInt := &optimizer.CastExpr{
		Operand:    &optimizer.IntegerConst{Value: int64(zzz.OID)},
		TargetType: "regclass",
	}
	ceStr := &optimizer.CastExpr{
		Operand:    &optimizer.StringConst{Value: zzz.Name},
		TargetType: "regclass",
	}

	vInt, err := evalSortKeyValue(ceInt, nil, ctx)
	if err != nil {
		t.Fatalf("evalSortKeyValue(int source): %v", err)
	}
	vStr, err := evalSortKeyValue(ceStr, nil, ctx)
	if err != nil {
		t.Fatalf("evalSortKeyValue(string source): %v", err)
	}
	if vInt.Kind != KindInt {
		t.Fatalf("int-source value Kind = %v, want KindInt", vInt.Kind)
	}
	if vStr.Kind != KindInt {
		t.Fatalf("string-source value Kind = %v, want KindInt", vStr.Kind)
	}
	if vInt.Int != vStr.Int {
		t.Fatalf("int-source OID = %d, string-source OID = %d, want equal", vInt.Int, vStr.Int)
	}
	if vInt.Int != int64(zzz.OID) {
		t.Fatalf("resolved OID = %d, want %d", vInt.Int, zzz.OID)
	}
}
