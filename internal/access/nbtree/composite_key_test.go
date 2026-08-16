package nbtree

import (
	"bytes"
	"math/big"
	"testing"
	"time"
)

// pgEpochForCompositeTest is 2000-01-01 UTC — the PostgreSQL epoch.
var pgEpochForCompositeTest = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)

// makeCompositeKey mirrors encodeCompositeBTreeKey in operators_ddl.go:
// concatenate per-column encodings with no separator.
func makeCompositeKey(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// dateMicrosComposite converts a calendar date to microseconds relative
// to pgEpochForCompositeTest.
func dateMicrosComposite(year, month, day int) int64 {
	t := time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC)
	return t.Sub(pgEpochForCompositeTest).Microseconds()
}

// TestCompositeKeyVarcharInt4 verifies that (varchar, int4) composite
// keys sort correctly: col1 is the primary sort key; col2 breaks ties.
func TestCompositeKeyVarcharInt4(t *testing.T) {
	// Sorted SQL order: ("A",1) < ("A",2) < ("B",1) < ("Z",0)
	type row struct{ s string; i int32 }
	rows := []row{
		{"A", 1},
		{"A", 2},
		{"B", 1},
		{"Z", 0},
	}
	keys := make([][]byte, len(rows))
	for i, r := range rows {
		keys[i] = makeCompositeKey(EncodeVarchar([]byte(r.s)), EncodeInt4(r.i))
	}
	for i := 0; i < len(keys)-1; i++ {
		if bytes.Compare(keys[i], keys[i+1]) >= 0 {
			t.Fatalf("(varchar,int4) ordering violated at index %d: rows[%d]=%v >= rows[%d]=%v\n  keys[%d]=%v\n  keys[%d]=%v",
				i, i, rows[i], i+1, rows[i+1], i, keys[i], i+1, keys[i+1])
		}
	}
}

// TestCompositeKeyTimestampNumeric verifies (timestamp, numeric) —
// the canonical TPC-H (l_shipdate, l_orderkey) shape.
func TestCompositeKeyTimestampNumeric(t *testing.T) {
	type row struct {
		year, month, day int
		orderKey         int64
	}
	// SQL order: same date rows sorted by orderKey; different dates sorted by date.
	rows := []row{
		{1994, 12, 31, 1},
		{1995, 1, 1, 1},
		{1995, 1, 1, 2},
		{1995, 1, 1, 100},
		{1995, 6, 15, 1},
		{1998, 12, 31, 9999},
	}
	keys := make([][]byte, len(rows))
	for i, r := range rows {
		micros := dateMicrosComposite(r.year, r.month, r.day)
		numKey := EncodeNumericKey(big.NewInt(r.orderKey), 0)
		keys[i] = makeCompositeKey(EncodeTimestamp(micros), numKey)
	}
	for i := 0; i < len(keys)-1; i++ {
		if bytes.Compare(keys[i], keys[i+1]) >= 0 {
			t.Fatalf("(timestamp,numeric) ordering violated at index %d: rows[%d]=%v >= rows[%d]=%v",
				i, i, rows[i], i+1, rows[i+1])
		}
	}
}

// TestCompositeKeyCharInt8 verifies (char, int8) — the TPC-H
// (c_mktsegment char(10), c_custkey bigint) shape.  Padded and
// unpadded forms of the same segment value must produce equal prefixes
// so the int8 column becomes the tie-breaker.
func TestCompositeKeyCharInt8(t *testing.T) {
	// Padded and unpadded BUILDING rows should produce the same first
	// key component; the int8 discriminates.
	kPadded1 := makeCompositeKey(EncodeChar([]byte("BUILDING  ")), EncodeInt8(5))
	kPadded2 := makeCompositeKey(EncodeChar([]byte("BUILDING  ")), EncodeInt8(7))
	kUnpad1 := makeCompositeKey(EncodeChar([]byte("BUILDING")), EncodeInt8(5))
	kFurniture := makeCompositeKey(EncodeChar([]byte("FURNITURE ")), EncodeInt8(1))

	// Padded and unpadded forms of the same first-column value must
	// produce the same prefix → the int8 portion decides.
	if !bytes.Equal(kPadded1, kUnpad1) {
		t.Fatalf("padded and unpadded char encode differently in composite key:\n  padded=%v\n  unpadded=%v", kPadded1, kUnpad1)
	}
	if bytes.Compare(kPadded1, kPadded2) >= 0 {
		t.Fatalf("(BUILDING,5) should < (BUILDING,7)")
	}
	if bytes.Compare(kPadded2, kFurniture) >= 0 {
		t.Fatalf("(BUILDING,7) should < (FURNITURE,1)")
	}
}

// TestCompositeKeyVarcharVarchar verifies that two variable-length
// columns compose correctly. This is the trickiest case because both
// columns need the 0x00 terminator to distinguish column boundaries.
func TestCompositeKeyVarcharVarchar(t *testing.T) {
	// SQL order: ("Customer#1","13-1") < ("Customer#10","12-9") < ("Customer#2","11-1")
	type row struct{ name, phone string }
	rows := []row{
		{"Customer#1", "13-1"},
		{"Customer#10", "12-9"}, // "Customer#10" > "Customer#1" (compare byte 10: '0' > terminator)
		{"Customer#2", "11-1"},  // "Customer#2" > "Customer#10" ('2' > '1')
	}
	keys := make([][]byte, len(rows))
	for i, r := range rows {
		keys[i] = makeCompositeKey(EncodeVarchar([]byte(r.name)), EncodeVarchar([]byte(r.phone)))
	}
	for i := 0; i < len(keys)-1; i++ {
		if bytes.Compare(keys[i], keys[i+1]) >= 0 {
			t.Fatalf("(varchar,varchar) ordering violated at index %d: rows[%d]=%v >= rows[%d]=%v\n  keys[%d]=%v\n  keys[%d]=%v",
				i, i, rows[i], i+1, rows[i+1], i, keys[i], i+1, keys[i+1])
		}
	}
}

// TestCompositeKeyThreeColumns verifies a 3-column (timestamp, varchar, int4)
// compound key, covering the o_orderdate + text + orderkey pattern.
func TestCompositeKeyThreeColumns(t *testing.T) {
	type row struct {
		year, month, day int
		status           string
		orderKey         int32
	}
	rows := []row{
		{1994, 12, 31, "F", 1},
		{1994, 12, 31, "F", 2},
		{1994, 12, 31, "O", 1}, // same date+status prefix broken by orderKey first? No: "O" > "F"
		{1995, 1, 1, "F", 1},
	}
	keys := make([][]byte, len(rows))
	for i, r := range rows {
		micros := dateMicrosComposite(r.year, r.month, r.day)
		keys[i] = makeCompositeKey(
			EncodeTimestamp(micros),
			EncodeVarchar([]byte(r.status)),
			EncodeInt4(r.orderKey),
		)
	}
	for i := 0; i < len(keys)-1; i++ {
		if bytes.Compare(keys[i], keys[i+1]) >= 0 {
			t.Fatalf("(timestamp,varchar,int4) ordering violated at index %d: rows[%d]=%v >= rows[%d]=%v",
				i, i, rows[i], i+1, rows[i+1])
		}
	}
}

// TestCompositeKeyVarcharPrefixSafety demonstrates the critical property
// that makes two-variable-length composite keys work: without the 0x00
// terminator, ("A", "BC") and ("AB", "C") would produce the same bytes.
// With the terminator they are distinct.
func TestCompositeKeyVarcharPrefixSafety(t *testing.T) {
	// ("A", "BC") → [0x41, 0x00, 0x42, 0x43, 0x00]
	k1 := makeCompositeKey(EncodeVarchar([]byte("A")), EncodeVarchar([]byte("BC")))
	// ("AB", "C") → [0x41, 0x42, 0x00, 0x43, 0x00]
	k2 := makeCompositeKey(EncodeVarchar([]byte("AB")), EncodeVarchar([]byte("C")))

	if bytes.Equal(k1, k2) {
		t.Fatalf("prefix safety violated: ('A','BC') and ('AB','C') produce identical composite keys")
	}
	// In SQL order, ("A","BC") < ("AB","C") because "A" < "AB".
	if bytes.Compare(k1, k2) >= 0 {
		t.Fatalf("('A','BC') should sort before ('AB','C'): k1=%v k2=%v", k1, k2)
	}
}

// TestCompositeKeyAllTypes exercises every supported type in one
// compound key to confirm they compose without mutual interference.
func TestCompositeKeyAllTypes(t *testing.T) {
	// (int4=1, int8=100, numeric=3.14, varchar="hi", char="A  ", timestamp=1995-01-01)
	// vs
	// (int4=1, int8=100, numeric=3.14, varchar="hi", char="B  ", timestamp=1995-01-01)
	// The char column is the tie-breaker here.
	common := [][]byte{
		EncodeInt4(1),
		EncodeInt8(100),
		EncodeNumericKey(big.NewInt(314), 2), // 3.14
		EncodeVarchar([]byte("hi")),
	}
	ts := EncodeTimestamp(dateMicrosComposite(1995, 1, 1))

	kA := makeCompositeKey(append(common, EncodeChar([]byte("A  ")), ts)...)
	kB := makeCompositeKey(append(common, EncodeChar([]byte("B  ")), ts)...)

	if bytes.Compare(kA, kB) >= 0 {
		t.Fatalf("(…,char='A',…) should < (…,char='B',…)")
	}
}
