package executor

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// The interval ordering asserted below was captured from the PostgreSQL 18.3
// reference cluster (bench/tpch/runtime, port 65432):
//
//	SELECT i FROM iv ORDER BY i;
//	 -infinity, -1 mons, -00:00:00.000001, 00:00:00, 1 day, 1 day 00:00:01,
//	 29 days 23:59:59, 1 mon, 2 mons, 1 year, 365 days, infinity
//
// Two rows in it are load-bearing:
//
//   - `29 days 23:59:59` sorts BELOW `1 mon`, and `1 year` (12 months = 360
//     days) BELOW `365 days` — a key that ordered by the month field first, or
//     that treated a month as a calendar month, would get both backwards.
//   - `1 mon` and `30 days` are EQUAL (`SELECT '1 mon'::interval = '30
//     days'::interval` → t), which is why the key is the span alone. `30 days`
//     is therefore held out of the ordering list and asserted separately.
var intervalKeyOrder = []string{
	"-infinity",
	"-1 mons",
	"-00:00:00.000001",
	"00:00:00",
	"1 day",
	"1 day 00:00:01",
	"29 days 23:59:59",
	"1 mon",
	"2 mons",
	"1 year",
	"365 days",
	"infinity",
}

func intervalCol() *catalog.Column {
	return &catalog.Column{Name: "i", Type: catalog.Type{Name: "interval"}}
}

// TestEncodeIntervalBTreeKeyMatchesPGOrder is the ordering gate: the encoded
// keys must sort exactly as the reference cluster sorts the values. Before this
// slice `CREATE INDEX ON t(interval_col)` raised 0A000 and there was no key at
// all.
func TestEncodeIntervalBTreeKeyMatchesPGOrder(t *testing.T) {
	col := intervalCol()
	var prev []byte
	for i, lit := range intervalKeyOrder {
		k, err := encodeBTreeKeyForColumn(nil, NewStringDatum(lit), col, 0)
		if err != nil {
			t.Fatalf("encode %q: %v", lit, err)
		}
		if len(k) != 16 {
			t.Fatalf("encode %q produced %d bytes, want the fixed 16-byte span key", lit, len(k))
		}
		if i > 0 && bytes.Compare(prev, k) >= 0 {
			t.Errorf("key(%s)=%x is not below key(%s)=%x, but PG orders them that way",
				intervalKeyOrder[i-1], prev, lit, k)
		}
		prev = k
	}
}

// TestIntervalKeyIsTheComparisonSpan pins the property that makes the key
// faithful rather than merely ordered: PG compares intervals by
// interval_cmp_value ALONE, so values with the same span are equal and must
// encode to identical bytes. A field-preserving key would pass the ordering
// gate above and still be wrong here — it would let a UNIQUE interval index
// accept a duplicate PG rejects.
func TestIntervalKeyIsTheComparisonSpan(t *testing.T) {
	col := intervalCol()
	equalPairs := [][2]string{
		{"1 mon", "30 days"},
		{"1 year", "360 days"},
		{"2 mons", "60 days"},
		{"1 day", "24:00:00"},
	}
	for _, p := range equalPairs {
		a, err := encodeBTreeKeyForColumn(nil, NewStringDatum(p[0]), col, 0)
		if err != nil {
			t.Fatalf("encode %q: %v", p[0], err)
		}
		b, err := encodeBTreeKeyForColumn(nil, NewStringDatum(p[1]), col, 0)
		if err != nil {
			t.Fatalf("encode %q: %v", p[1], err)
		}
		if !bytes.Equal(a, b) {
			t.Errorf("%q=%x and %q=%x encode differently, but PG compares them equal", p[0], a, p[1], b)
		}
	}
}

// TestIntervalKeyStringAndIntervalDatumAgree is the probe-symmetry gate over
// the two runtime shapes an interval arrives in: a stored row decodes to
// KindString (goopg holds an interval column as text) while expression
// evaluation produces KindInterval. If the two encoded differently, an
// `interval '1 day'` probe would not find the row `'1 day'` stored.
func TestIntervalKeyStringAndIntervalDatumAgree(t *testing.T) {
	col := intervalCol()
	cases := []struct {
		lit          string
		months, days int32
		micros       int64
	}{
		{"1 day", 0, 1, 0},
		{"1 mon", 1, 0, 0},
		{"-1 mons", -1, 0, 0},
		{"29 days 23:59:59", 0, 29, 86399000000},
		{"-00:00:00.000001", 0, 0, -1},
	}
	for _, c := range cases {
		fromText, err := encodeBTreeKeyForColumn(nil, NewStringDatum(c.lit), col, 0)
		if err != nil {
			t.Fatalf("encode text %q: %v", c.lit, err)
		}
		fromDatum, err := encodeBTreeKeyForColumn(nil, 
			NewIntervalDatumFull(c.months, c.days, c.micros), col, 0)
		if err != nil {
			t.Fatalf("encode datum %q: %v", c.lit, err)
		}
		if !bytes.Equal(fromText, fromDatum) {
			t.Errorf("%q: text key %x != KindInterval key %x", c.lit, fromText, fromDatum)
		}
	}
}

// TestIntervalKeyRejectsUnparseableText asserts a value the interval parser
// refuses raises PG's 22007 instead of being indexed as something else. The
// bulk build surfaces the error; the runtime maintain path swallows it, which
// is exactly why it must not be a silent success.
func TestIntervalKeyRejectsUnparseableText(t *testing.T) {
	_, err := encodeBTreeKeyForColumn(nil, NewStringDatum("not an interval"), intervalCol(), 0)
	if err == nil {
		t.Fatalf("encoding %q succeeded; want 22007", "not an interval")
	}
	if err.Code != "22007" {
		t.Errorf("SQLSTATE %s, want 22007 (%s)", err.Code, err.Message)
	}
}

// TestIntervalKeyDecodeIsRefused covers both key-decode siblings
// (decodeIndexKeyColumn for the composite/amcheck walk, decodeBTreeKeyToDatum
// for the single-column index-only scan). Neither may invent a value from a
// span that has none, and — the reason this is a test and not a comment — an
// UNROUTED 16-byte key would NOT error: it would reach the siblings' shared
// `default:` arm, decode 8 bytes as an enum float8, and desynchronize the
// composite walk by half the key.
func TestIntervalKeyDecodeIsRefused(t *testing.T) {
	col := *intervalCol()
	key, encErr := encodeBTreeKeyForColumn(nil, NewStringDatum("1 mon"), &col, 0)
	if encErr != nil {
		t.Fatalf("encode: %v", encErr)
	}
	if _, _, err := decodeIndexKeyColumn(key, col); err == nil {
		t.Errorf("decodeIndexKeyColumn accepted an interval key; want a refusal")
	}
	if _, err := decodeBTreeKeyToDatum(key, col); err == nil {
		t.Errorf("decodeBTreeKeyToDatum accepted an interval key; want a refusal")
	}
}

// TestIntervalIndexBuildAndMaintainKeys is the end-to-end gate over both
// stored-key writers (Hard-won Rule #2): the CREATE INDEX bulk build (rows
// first) and the runtime maintain path on INSERT (index first). Every row must
// be indexed, by both paths, under identical bytes and in PG's order. Each
// CREATE INDEX here failed with 0A000 before this slice.
func TestIntervalIndexBuildAndMaintainKeys(t *testing.T) {
	withBlobIndexKeys(t)
	ctx, cleanup := newVMFixture(t)
	defer cleanup()
	ctx.CurrentDatabaseOid = catalog.DefaultDBOid

	// Insert rotated so a pass cannot come from insertion order accidentally
	// matching PG's.
	ins := make([]string, 0, len(intervalKeyOrder))
	for i := range intervalKeyOrder {
		ins = append(ins, intervalKeyOrder[(i+len(intervalKeyOrder)/2)%len(intervalKeyOrder)])
	}
	stmts := []string{
		"CREATE TABLE iv_m (i interval)",
		"CREATE INDEX iv_m_idx ON iv_m (i)",
	}
	for _, lit := range ins {
		stmts = append(stmts, fmt.Sprintf("INSERT INTO iv_m VALUES ('%s')", lit))
	}
	stmts = append(stmts, "CREATE TABLE iv_b (i interval)")
	for _, lit := range ins {
		stmts = append(stmts, fmt.Sprintf("INSERT INTO iv_b VALUES ('%s')", lit))
	}
	stmts = append(stmts, "CREATE INDEX iv_b_idx ON iv_b (i)")
	for _, sql := range stmts {
		if err := runDDL(t, ctx, sql); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}

	col := intervalCol()
	want := make([][]byte, len(intervalKeyOrder))
	for i, lit := range intervalKeyOrder {
		k, err := encodeBTreeKeyForColumn(nil, NewStringDatum(lit), col, 0)
		if err != nil {
			t.Fatalf("encode %q: %v", lit, err)
		}
		want[i] = k
	}
	for _, p := range []struct{ label, idx string }{
		{"maintain", "iv_m_idx"}, {"build", "iv_b_idx"},
	} {
		got := scanArrayIndexKeys(t, ctx, p.idx)
		if len(got) != len(want) {
			t.Fatalf("%s path indexed %d of %d rows (%x)", p.label, len(got), len(want), got)
		}
		for i := range want {
			if !bytes.Equal(got[i], want[i]) {
				t.Errorf("%s path key[%d]=%x, want %x (%s)", p.label, i, got[i], want[i], intervalKeyOrder[i])
			}
		}
	}
}

// TestIntervalCompositeKeyIsSelfDelimiting pins the property that makes
// interval safe in a non-final composite position: the span key is FIXED width,
// so the trailing column's bytes start at a known offset and decide the order
// whenever the intervals are equal — including for two spellings of the same
// span, which is where a field-preserving key would order by the wrong column.
func TestIntervalCompositeKeyIsSelfDelimiting(t *testing.T) {
	icol := intervalCol()
	kcol := &catalog.Column{Name: "k", Type: catalog.Type{Name: "int4"}}
	build := func(lit string, k int64) []byte {
		iv, err := encodeBTreeKeyForColumn(nil, NewStringDatum(lit), icol, 0)
		if err != nil {
			t.Fatalf("encode %q: %v", lit, err)
		}
		if len(iv) != 16 {
			t.Fatalf("interval key for %q is %d bytes, want a fixed 16", lit, len(iv))
		}
		tail, err := encodeBTreeKeyForColumn(nil, NewIntDatum(k), kcol, 0)
		if err != nil {
			t.Fatalf("encode tail %d: %v", k, err)
		}
		return append(append([]byte{}, iv...), tail...)
	}
	// PG order for (i, k): ('1 day',7) < ('1 mon',1) = ('30 days',1) tie broken
	// by k, so ('1 mon',1) < ('30 days',2) < ('2 mons',0).
	ordered := [][]byte{
		build("1 day", 7),
		build("1 mon", 1),
		build("30 days", 2),
		build("2 mons", 0),
	}
	for i := 1; i < len(ordered); i++ {
		if bytes.Compare(ordered[i-1], ordered[i]) >= 0 {
			t.Errorf("composite key %d (%x) is not below %d (%x)", i-1, ordered[i-1], i, ordered[i])
		}
	}
}

// TestIntervalIndexOnlyScanReadsHeap is the gate on the decode-from-key fast
// path being declined for a non-invertible key. The page IS ALL_VISIBLE here,
// so without indexKeyIsDecodable the scan would take the fast path and fail the
// whole query with `XX000 IOS decode: btree: interval key …` — the interval
// column is indexable but its key holds no interval to decode.
func TestIntervalIndexOnlyScanReadsHeap(t *testing.T) {
	withBlobIndexKeys(t)
	ctx, cleanup := newVMFixture(t)
	defer cleanup()

	runComposite(t, ctx,
		"CREATE TABLE iv_ios (i interval)",
		"CREATE INDEX iv_ios_idx ON iv_ios (i)",
		"INSERT INTO iv_ios VALUES ('1 day')",
		"INSERT INTO iv_ios VALUES ('2 mons')",
	)
	vacuumThen(t, ctx, "iv_ios")

	const q = "SELECT i FROM iv_ios WHERE i = '2 mons'"
	if ios := findIndexOnlyScan(planOne(t, q, ctx.Catalog)); ios == nil {
		t.Skip("planner did not promote to IndexOnlyScan; the fast path is not reachable")
	}
	rows := runQuery(t, ctx, q)
	if len(rows) != 1 {
		t.Fatalf("rows=%d want 1 (%v)", len(rows), rows)
	}
	if got := rows[0][0].Format(); got != "2 mons" {
		t.Errorf("row=%q want %q", got, "2 mons")
	}
}
