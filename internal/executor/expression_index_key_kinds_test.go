package executor

import (
	"bytes"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/catalog"
)

// TestEncodeArbiterExprKeyCoversNonTextKinds pins the widened kind coverage of
// encodeArbiterExprKey (M0119-0006). The encoder used to handle KindString and
// KindInt only, so an expression key column whose result was numeric, a
// timestamp, a boolean, an enum or bytea encoded to nil — and every caller
// treats nil as "row not indexable", which silently dropped the row from the
// index on BOTH the runtime maintain path and the CREATE INDEX/REINDEX bulk
// build. A nil here is therefore data loss, not a benign fallback.
func TestEncodeArbiterExprKeyCoversNonTextKinds(t *testing.T) {
	ts := time.Date(2024, 3, 5, 6, 7, 8, 0, time.UTC)
	for _, tc := range []struct {
		name string
		v    Datum
	}{
		{"string", NewStringDatum("abc")},
		{"int", NewIntDatum(42)},
		{"numeric", Datum{Kind: KindNumeric, Int: 12345, Scale: 2}},
		{"timestamp", NewTimeDatum(ts)},
		{"bool", NewBoolDatum(true)},
		{"enum", NewEnumDatum(3, "green")},
		{"bytes", NewBytesDatum([]byte{0x00, 0x01, 0x7f})},
	} {
		if got := encodeArbiterExprKey(tc.v, 0); got == nil {
			t.Errorf("%s: encodeArbiterExprKey returned nil — rows with this "+
				"expression result kind would be dropped from the index", tc.name)
		}
	}
}

// TestEncodeArbiterExprKeyOrderPreserving asserts the property the B-tree
// actually depends on: bytewise comparison of two encoded keys of the same kind
// must reproduce the SQL ordering of the values. A kind arm that returns
// non-nil bytes in the wrong order is worse than returning nil — the index
// would be built and then searched incorrectly.
func TestEncodeArbiterExprKeyOrderPreserving(t *testing.T) {
	for _, tc := range []struct {
		name   string
		lo, hi Datum
	}{
		{"numeric", Datum{Kind: KindNumeric, Int: -150, Scale: 2}, Datum{Kind: KindNumeric, Int: 150, Scale: 2}},
		{"numeric-same-scale", Datum{Kind: KindNumeric, Int: 100, Scale: 2}, Datum{Kind: KindNumeric, Int: 12345, Scale: 2}},
		{"timestamp", NewTimeDatum(time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC)),
			NewTimeDatum(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))},
		{"bool", NewBoolDatum(false), NewBoolDatum(true)},
		{"enum", NewEnumDatum(1, "red"), NewEnumDatum(2, "green")},
		{"bytes", NewBytesDatum([]byte{0x01, 0x02}), NewBytesDatum([]byte{0x02})},
		{"int", NewIntDatum(-5), NewIntDatum(5)},
	} {
		lo := encodeArbiterExprKey(tc.lo, 0)
		hi := encodeArbiterExprKey(tc.hi, 0)
		if lo == nil || hi == nil {
			t.Fatalf("%s: unexpected nil encoding", tc.name)
		}
		if bytes.Compare(lo, hi) >= 0 {
			t.Errorf("%s: encoded keys are not order-preserving (%x >= %x)", tc.name, lo, hi)
		}
	}
}

// TestExpressionIndexBuildNonTextKeyKinds is the end-to-end sibling check: both
// the bulk-build path (CREATE INDEX over pre-existing rows) and the runtime
// maintain path (post-build INSERT) must index every row of an expression index
// whose key expression yields a non-text, non-integer result. Before the widened
// encoder these indexes built EMPTY, exactly like the text case that
// TestExpressionIndexBuildIndexesExistingRows pins.
func TestExpressionIndexBuildNonTextKeyKinds(t *testing.T) {
	ctx, cleanup := newVMFixture(t)
	defer cleanup()
	ctx.CurrentDatabaseOid = catalog.DefaultDBOid

	for _, sql := range []string{
		"CREATE TABLE exprkind_t (a int4, n numeric)",
		"INSERT INTO exprkind_t VALUES (1, 10.25)",
		"INSERT INTO exprkind_t VALUES (2, -3.50)",
		"INSERT INTO exprkind_t VALUES (3, 7.00)",
		// numeric-valued key expression, built over the rows above.
		"CREATE INDEX exprkind_num ON exprkind_t ((n * 2))",
		// boolean-valued key expression.
		"CREATE INDEX exprkind_bool ON exprkind_t ((a > 1))",
	} {
		if err := runDDL(t, ctx, sql); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}

	for _, name := range []string{"exprkind_num", "exprkind_bool"} {
		if got := countIndexEntries(t, ctx, lookupIndexByName(t, ctx, name)); got != 3 {
			t.Errorf("%s: %d index entries after build over 3 pre-existing rows, want 3", name, got)
		}
	}

	if err := runDDL(t, ctx, "INSERT INTO exprkind_t VALUES (4, 1.75)"); err != nil {
		t.Fatalf("INSERT after build: %v", err)
	}
	for _, name := range []string{"exprkind_num", "exprkind_bool"} {
		if got := countIndexEntries(t, ctx, lookupIndexByName(t, ctx, name)); got != 4 {
			t.Errorf("%s: %d entries after post-build INSERT, want 4", name, got)
		}
	}
}
