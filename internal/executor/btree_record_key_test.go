package executor

import (
	"bytes"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestRecordBTreeKeyOrder pins the record_ops key (M0149-0046b): keys of a
// (float8, text, int4) composite compare like record_cmp — field by field
// with each field's own ordering (float8 by value, not by its text), a NULL
// field after every non-NULL value — and value-equal records with different
// spellings (1.0 vs 1) encode identically, so a unique composite key sees
// them as duplicates exactly as PG does.
func TestRecordBTreeKeyOrder(t *testing.T) {
	ctx, _, cleanup := newStorageFixture(t)
	defer cleanup()
	if err := runDDL(t, ctx, "CREATE TYPE tst_comp_basic_t AS (a FLOAT, b TEXT, c INTEGER)"); err != nil {
		t.Fatalf("CREATE TYPE: %v", err)
	}
	col := &catalog.Column{Name: "a", Type: catalog.Type{Name: "tst_comp_basic_t"}}
	key := func(rec string) []byte {
		t.Helper()
		k, err := encodeBTreeKeyForColumn(ctx, NewStringDatum(rec), col, 0)
		if err != nil {
			t.Fatalf("encode %s: %v", rec, err)
		}
		return k
	}
	// Ascending in record_cmp order.
	ordered := []string{
		"(-1,z,9)",
		"(1,a,1)",
		"(1,a,2)",
		"(1,a,)", // NULL c sorts after every c
		"(1,b,0)",
		"(2.5,a,1)",
		"(10,a,1)", // 10 > 2.5 by value, though "10" < "2.5" as text
		"(,a,1)",   // NULL a sorts last
	}
	for i := 1; i < len(ordered); i++ {
		if bytes.Compare(key(ordered[i-1]), key(ordered[i])) >= 0 {
			t.Errorf("key(%s) >= key(%s), want <", ordered[i-1], ordered[i])
		}
	}
	if !bytes.Equal(key("(1.0,a,1)"), key("(1,a,1)")) {
		t.Error("(1.0,a,1) and (1,a,1) encode differently; record_cmp calls them equal")
	}
	if _, err := encodeBTreeKeyForColumn(ctx, NewStringDatum("(1,a)"), col, 0); err == nil || err.Code != "22P02" {
		t.Errorf("2-field literal for a 3-field type: err %v, want 22P02", err)
	}
}
