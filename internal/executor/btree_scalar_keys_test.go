package executor

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// The orderings asserted below were captured from the PostgreSQL 18.3 reference
// cluster (bench/tpch/runtime, port 65432), not derived by reading the
// comparators:
//
//	int2 : -32768 -1 0 1 32767
//	oid  : 0 1 2147483647 2147483648 4294967295
//	bool : false true
//	bytea: (empty) 00 0001 01 ff
//	time : 00:00:00 00:00:00.000001 12:30:00 23:59:59.999999
//	timetz: 00:00:00+14 00:00:00+00 12:00:00+05:30 13:00:00+01 12:00:00+00
//	        11:00:00-01 00:00:00-12 23:59:59.999999-12
//
// The oid row is the load-bearing one: PG compares OIDs UNSIGNED, so 2147483648
// sorts ABOVE 2147483647. An int4-shaped key (the obvious choice for a 4-byte
// type) would put it below OID 0 instead.
//
// The timetz row carries two properties at once. Its middle three values are
// the SAME instant (13:00+01, 12:00+00 and 11:00-01 are all 12:00 GMT), so they
// pin the tie-break: PG breaks it by the zone in seconds WEST of UTC, which is
// the negation of the seconds-east offset goopg's Datum carries — order by the
// datum's own offset and those three come out exactly reversed. The outer
// values pin the primary part: 00:00:00+14 is the smallest even though its
// local clock reads midnight, because GMT-equivalent it is 10:00 the previous
// day.

// scalarKeyCase is one column type plus its values in PG's order.
type scalarKeyCase struct {
	name   string // Go-side label
	typ    string // catalog type name
	values []Datum
	lits   []string // the same values as SQL literal text, PG order
}

func timeDatumOfDay(micros int64) Datum { return NewTimeDatum(pgTimeFromMicros(micros)) }

// timeTzDatumOfDay builds a timetz Datum from the LOCAL microseconds-of-day and
// the offset in seconds EAST of UTC (goopg's Datum.Scale convention, the
// negation of upstream TimeTzADT.zone).
func timeTzDatumOfDay(micros int64, offsetEastSecs int) Datum {
	return NewTimeTZDatum(pgTimeFromMicros(micros), offsetEastSecs)
}

// sqlLiteralForKeyType writes the value as an INSERT literal — quoted for every
// type including boolean. The bool arm used to be excepted because goopg's codec
// demanded KindBool strictly and `VALUES ('true')` raised XX000; that gap is
// closed (pgBoolIn), so the quoted form now exercises the same path PG does.
func sqlLiteralForKeyType(lit string) string {
	return "'" + lit + "'"
}

func scalarKeyCases() []scalarKeyCase {
	return []scalarKeyCase{
		{
			name:   "int2",
			typ:    "smallint",
			values: []Datum{NewIntDatum(-32768), NewIntDatum(-1), NewIntDatum(0), NewIntDatum(1), NewIntDatum(32767)},
			lits:   []string{"-32768", "-1", "0", "1", "32767"},
		},
		{
			name: "oid",
			typ:  "oid",
			values: []Datum{NewIntDatum(0), NewIntDatum(1), NewIntDatum(2147483647),
				NewIntDatum(2147483648), NewIntDatum(4294967295)},
			lits: []string{"0", "1", "2147483647", "2147483648", "4294967295"},
		},
		{
			name:   "bool",
			typ:    "boolean",
			values: []Datum{NewBoolDatum(false), NewBoolDatum(true)},
			lits:   []string{"false", "true"},
		},
		{
			name: "bytea",
			typ:  "bytea",
			values: []Datum{NewBytesDatum([]byte{}), NewBytesDatum([]byte{0x00}),
				NewBytesDatum([]byte{0x00, 0x01}), NewBytesDatum([]byte{0x01}), NewBytesDatum([]byte{0xff})},
			lits: []string{`\x`, `\x00`, `\x0001`, `\x01`, `\xff`},
		},
		{
			name: "time",
			typ:  "time",
			values: []Datum{timeDatumOfDay(0), timeDatumOfDay(1),
				timeDatumOfDay(12*3600*1000000 + 30*60*1000000), timeDatumOfDay(86400*1000000 - 1)},
			lits: []string{"00:00:00", "00:00:00.000001", "12:30:00", "23:59:59.999999"},
		},
		{
			name: "timetz",
			typ:  "timetz",
			values: []Datum{
				timeTzDatumOfDay(0, 14*3600),
				timeTzDatumOfDay(0, 0),
				timeTzDatumOfDay(12*3600*1000000, 5*3600+30*60),
				timeTzDatumOfDay(13*3600*1000000, 3600),
				timeTzDatumOfDay(12*3600*1000000, 0),
				timeTzDatumOfDay(11*3600*1000000, -3600),
				timeTzDatumOfDay(0, -12*3600),
				timeTzDatumOfDay(86400*1000000-1, -12*3600),
			},
			lits: []string{"00:00:00+14", "00:00:00+00", "12:00:00+05:30", "13:00:00+01",
				"12:00:00+00", "11:00:00-01", "00:00:00-12", "23:59:59.999999-12"},
		},
	}
}

// TestEncodeScalarBTreeKeyMatchesPGOrder asserts the encoded keys sort in the
// oracle order above. Before M0119-0006 none of these types could be indexed at
// all — isSupportedBTreeKeyType rejected them, so CREATE INDEX raised
// `0A000 btree v0 only supports int4 / numeric keys`.
func TestEncodeScalarBTreeKeyMatchesPGOrder(t *testing.T) {
	for _, tc := range scalarKeyCases() {
		t.Run(tc.name, func(t *testing.T) {
			col := &catalog.Column{Name: "k", Type: catalog.Type{Name: tc.typ}}
			var prev []byte
			for i, v := range tc.values {
				k, err := encodeBTreeKeyForColumn(v, col, 0)
				if err != nil {
					t.Fatalf("encode %s: %v", tc.lits[i], err)
				}
				if len(k) == 0 {
					// encodeIndexKeyFromCols reads a zero-length key as "no
					// key" and drops the row from the index entirely.
					t.Fatalf("encode %s produced an empty key", tc.lits[i])
				}
				if i > 0 && bytes.Compare(prev, k) >= 0 {
					t.Errorf("key(%s)=%x is not below key(%s)=%x, but PG orders them that way",
						tc.lits[i-1], prev, tc.lits[i], k)
				}
				prev = k
			}
		})
	}
}

// TestScalarBTreeKeyProbeMatchesStoredKey is the probe-symmetry gate: a WHERE
// literal arrives as KindString (bare literals are typed `unknown`), so the
// probe key must coerce to the SAME bytes the stored row produced from its
// decoded Datum. Without the coercion arm the probe would either error 42804 or,
// worse, encode a different shape and silently find nothing.
func TestScalarBTreeKeyProbeMatchesStoredKey(t *testing.T) {
	for _, tc := range scalarKeyCases() {
		t.Run(tc.name, func(t *testing.T) {
			col := &catalog.Column{Name: "k", Type: catalog.Type{Name: tc.typ}}
			for i, v := range tc.values {
				stored, err := encodeBTreeKeyForColumn(v, col, 0)
				if err != nil {
					t.Fatalf("encode stored %s: %v", tc.lits[i], err)
				}
				probe, err := encodeBTreeKeyForColumn(NewStringDatum(tc.lits[i]), col, 0)
				if err != nil {
					t.Fatalf("encode probe %s: %v", tc.lits[i], err)
				}
				if !bytes.Equal(stored, probe) {
					t.Errorf("%s: probe key %x != stored key %x", tc.lits[i], probe, stored)
				}
			}
		})
	}
}

// TestScalarBTreeKeyDecodeSiblingParity covers Hard-won Rule #2 over the two
// key-decode siblings: decodeIndexKeyColumn (composite walk / amcheck) and
// decodeBTreeKeyToDatum (single-column index-only scan). Both must invert the
// encoding identically AND the composite one must report the exact byte width,
// or a following key column is read from the wrong offset.
//
// Non-vacuity note: without the routing added in this slice, oid and time keys
// (8 bytes) would NOT error here — they would fall through the shared
// `default:` arm and decode as an enum float8.
func TestScalarBTreeKeyDecodeSiblingParity(t *testing.T) {
	for _, tc := range scalarKeyCases() {
		t.Run(tc.name, func(t *testing.T) {
			col := catalog.Column{Name: "k", Type: catalog.Type{Name: tc.typ}}
			for i, v := range tc.values {
				key, err := encodeBTreeKeyForColumn(v, &col, 0)
				if err != nil {
					t.Fatalf("encode %s: %v", tc.lits[i], err)
				}
				// Composite walk: append a trailing int4 column so the decoder
				// sees a key that does NOT end at this column's boundary.
				tail, err := encodeBTreeKeyForColumn(NewIntDatum(7),
					&catalog.Column{Name: "t", Type: catalog.Type{Name: "int4"}}, 0)
				if err != nil {
					t.Fatalf("encode tail: %v", err)
				}
				composite := append(append([]byte{}, key...), tail...)
				dc, n, dErr := decodeIndexKeyColumn(composite, col)
				if dErr != nil {
					t.Fatalf("decodeIndexKeyColumn(%s): %v", tc.lits[i], dErr)
				}
				if n != len(key) {
					t.Errorf("%s: composite walk consumed %d bytes, key is %d", tc.lits[i], n, len(key))
				}
				ds, sErr := decodeBTreeKeyToDatum(key, col)
				if sErr != nil {
					t.Fatalf("decodeBTreeKeyToDatum(%s): %v", tc.lits[i], sErr)
				}
				for label, got := range map[string]Datum{"composite": dc, "single": ds} {
					if got.Kind != v.Kind {
						t.Errorf("%s %s: kind %d, want %d", tc.lits[i], label, got.Kind, v.Kind)
					}
					// Re-encoding the decoded datum must reproduce the key —
					// the value-level identity that matters for both callers.
					re, err := encodeBTreeKeyForColumn(got, &col, 0)
					if err != nil {
						t.Fatalf("%s %s: re-encode: %v", tc.lits[i], label, err)
					}
					if !bytes.Equal(re, key) {
						t.Errorf("%s %s: re-encoded %x != %x", tc.lits[i], label, re, key)
					}
				}
			}
		})
	}
}

// TestScalarIndexBuildAndMaintainKeys is the end-to-end gate over both stored-key
// writers (Hard-won Rule #2): the CREATE INDEX bulk build (rows first) and the
// runtime maintain path on INSERT (index first). Every row must be indexed, by
// both paths, under identical bytes and in PG's order.
//
// This is also the CREATE INDEX acceptance gate: each of these statements
// failed with 0A000 before this slice.
func TestScalarIndexBuildAndMaintainKeys(t *testing.T) {
	withBlobIndexKeys(t)
	for _, tc := range scalarKeyCases() {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cleanup := newVMFixture(t)
			defer cleanup()
			ctx.CurrentDatabaseOid = catalog.DefaultDBOid

			mt := "sk_m_" + tc.name
			bt := "sk_b_" + tc.name
			// Insert in a rotated order so a pass cannot come from insertion
			// order accidentally matching PG's.
			ins := make([]string, 0, len(tc.lits))
			for i := range tc.lits {
				ins = append(ins, tc.lits[(i+len(tc.lits)/2)%len(tc.lits)])
			}
			stmts := []string{
				fmt.Sprintf("CREATE TABLE %s (k %s)", mt, tc.typ),
				fmt.Sprintf("CREATE INDEX %s_idx ON %s (k)", mt, mt),
			}
			for _, lit := range ins {
				stmts = append(stmts, fmt.Sprintf("INSERT INTO %s VALUES (%s)", mt, sqlLiteralForKeyType(lit)))
			}
			stmts = append(stmts, fmt.Sprintf("CREATE TABLE %s (k %s)", bt, tc.typ))
			for _, lit := range ins {
				stmts = append(stmts, fmt.Sprintf("INSERT INTO %s VALUES (%s)", bt, sqlLiteralForKeyType(lit)))
			}
			stmts = append(stmts, fmt.Sprintf("CREATE INDEX %s_idx ON %s (k)", bt, bt))
			for _, sql := range stmts {
				if err := runDDL(t, ctx, sql); err != nil {
					t.Fatalf("%s: %v", sql, err)
				}
			}

			col := &catalog.Column{Name: "k", Type: catalog.Type{Name: tc.typ}}
			want := make([][]byte, len(tc.values))
			for i, v := range tc.values {
				k, err := encodeBTreeKeyForColumn(v, col, 0)
				if err != nil {
					t.Fatalf("encode %s: %v", tc.lits[i], err)
				}
				want[i] = k
			}
			for _, p := range []struct{ label, idx string }{
				{"maintain", mt + "_idx"}, {"build", bt + "_idx"},
			} {
				got := scanArrayIndexKeys(t, ctx, p.idx)
				if len(got) != len(want) {
					t.Fatalf("%s path indexed %d of %d rows (%x)", p.label, len(got), len(want), got)
				}
				for i := range want {
					if !bytes.Equal(got[i], want[i]) {
						t.Errorf("%s path key[%d]=%x, want %x (%s)", p.label, i, got[i], want[i], tc.lits[i])
					}
				}
			}
		})
	}
}

// TestByteaIndexKeyIsSelfDelimiting pins the property that makes bytea safe as a
// composite key column: EncodeVarchar escapes 0x00/0x01, so an embedded NUL
// cannot forge the terminator, and a shorter value still sorts before its own
// extensions whatever follows in the next column (PG's byteacmp: memcmp, then
// shorter-first).
func TestByteaIndexKeyIsSelfDelimiting(t *testing.T) {
	bcol := &catalog.Column{Name: "b", Type: catalog.Type{Name: "bytea"}}
	icol := &catalog.Column{Name: "i", Type: catalog.Type{Name: "int4"}}
	compose := func(b []byte, i int64) []byte {
		bk, err := encodeBTreeKeyForColumn(NewBytesDatum(b), bcol, 0)
		if err != nil {
			t.Fatalf("encode %x: %v", b, err)
		}
		ik, err := encodeBTreeKeyForColumn(NewIntDatum(i), icol, 0)
		if err != nil {
			t.Fatalf("encode %d: %v", i, err)
		}
		return append(append([]byte{}, bk...), ik...)
	}
	short := compose([]byte{0x00}, 99)
	long := compose([]byte{0x00, 0x00}, 0)
	if bytes.Compare(short, long) >= 0 {
		t.Errorf("('\\x00',99)=%x is not below ('\\x0000',0)=%x", short, long)
	}
}

// TestTimeTzIndexKeyIsTwoPart pins the shape that makes timetz representable
// after all (this slice supersedes the earlier deliberate refusal). Three
// things must hold together:
//
//  1. timetz keeps its OWN encoding — isTimeOfDayType must not claim it, or a
//     timetz key would be the 8-byte plain-time key and the zone would vanish
//     from the order entirely.
//  2. The key is a fixed 12 bytes: the int8 GMT part then the int4 zone part.
//     Fixed width is what makes it safe in a composite key.
//  3. The tie-break runs the way timetz_cmp_internal runs it — by seconds WEST
//     of UTC. This is the direction that is easy to get backwards, so it is
//     asserted directly on same-instant values rather than only through the
//     whole-ordering table.
func TestTimeTzIndexKeyIsTwoPart(t *testing.T) {
	if isTimeOfDayType("timetz") || isTimeOfDayType("time with time zone") {
		t.Fatal("timetz must not be routed through the plain time-of-day key encoding")
	}
	if !isSupportedBTreeKeyType("timetz") || !isSupportedBTreeKeyType("time with time zone") {
		t.Fatal("timetz must be accepted as a B-tree key type")
	}
	col := &catalog.Column{Name: "k", Type: catalog.Type{Name: "timetz"}}

	// Same instant (12:00 GMT), three different zones. PG: 13:00+01 < 12:00+00
	// < 11:00-01. Ordering by the datum's seconds-EAST offset reverses this.
	sameInstant := []struct {
		lit string
		d   Datum
	}{
		{"13:00:00+01", timeTzDatumOfDay(13*3600*1000000, 3600)},
		{"12:00:00+00", timeTzDatumOfDay(12*3600*1000000, 0)},
		{"11:00:00-01", timeTzDatumOfDay(11*3600*1000000, -3600)},
	}
	var prev []byte
	for i, c := range sameInstant {
		k, err := encodeBTreeKeyForColumn(c.d, col, 0)
		if err != nil {
			t.Fatalf("encode %s: %v", c.lit, err)
		}
		if len(k) != 12 {
			t.Fatalf("timetz key for %s is %d bytes, want 12", c.lit, len(k))
		}
		if i > 0 {
			if !bytes.Equal(prev[:8], k[:8]) {
				t.Errorf("%s and %s are the same instant but their GMT key parts differ: %x vs %x",
					sameInstant[i-1].lit, c.lit, prev[:8], k[:8])
			}
			if bytes.Compare(prev, k) >= 0 {
				t.Errorf("key(%s)=%x is not below key(%s)=%x, but PG orders them that way",
					sameInstant[i-1].lit, prev, c.lit, k)
			}
		}
		prev = k
	}
}

// TestTimeTzCompositeKeyIsSelfDelimiting is the composite-position gate: a
// timetz key column is fixed-width, so a FOLLOWING key column's bytes can never
// be mistaken for part of it. The pair below shares the timetz value's primary
// (GMT) part and differs only in the trailing int4 column, so the composite key
// must still order by that trailing column.
func TestTimeTzCompositeKeyIsSelfDelimiting(t *testing.T) {
	tcol := &catalog.Column{Name: "k", Type: catalog.Type{Name: "timetz"}}
	icol := &catalog.Column{Name: "i", Type: catalog.Type{Name: "int4"}}
	compose := func(d Datum, i int64) []byte {
		tk, err := encodeBTreeKeyForColumn(d, tcol, 0)
		if err != nil {
			t.Fatalf("encode timetz: %v", err)
		}
		ik, err := encodeBTreeKeyForColumn(NewIntDatum(i), icol, 0)
		if err != nil {
			t.Fatalf("encode int4: %v", err)
		}
		return append(append([]byte{}, tk...), ik...)
	}
	noon := timeTzDatumOfDay(12*3600*1000000, 0)
	if lo, hi := compose(noon, 1), compose(noon, 2); bytes.Compare(lo, hi) >= 0 {
		t.Errorf("('12:00:00+00',1)=%x is not below ('12:00:00+00',2)=%x", lo, hi)
	}
	// And the timetz column still dominates a later column, whatever it holds.
	early := timeTzDatumOfDay(11*3600*1000000, 0)
	if lo, hi := compose(early, 9999), compose(noon, -9999); bytes.Compare(lo, hi) >= 0 {
		t.Errorf("('11:00:00+00',9999)=%x is not below ('12:00:00+00',-9999)=%x", lo, hi)
	}
	// The composite WALK must resynchronize on the same boundary — the failure
	// the array-decode slice found is a decoder that consumes the wrong width
	// and reads every LATER column from a shifted offset. Both key columns are
	// checked, not just the widths.
	key := compose(noon, -9999)
	d, n, err := decodeIndexKeyColumn(key, catalog.Column{Name: "k", Type: catalog.Type{Name: "timetz"}})
	if err != nil {
		t.Fatalf("composite walk over timetz: %v", err)
	}
	if n != 12 {
		t.Fatalf("composite walk consumed %d bytes of the timetz column, want 12", n)
	}
	if re, rErr := encodeBTreeKeyForColumn(d, tcol, 0); rErr != nil {
		t.Fatalf("re-encode decoded timetz: %v", rErr)
	} else if !bytes.Equal(re, key[:12]) {
		t.Errorf("re-encoded timetz %x != %x", re, key[:12])
	}
	tail, tn, err := decodeIndexKeyColumn(key[n:], catalog.Column{Name: "i", Type: catalog.Type{Name: "int4"}})
	if err != nil {
		t.Fatalf("composite walk over the trailing int4: %v", err)
	}
	if tn != 4 || tail.Kind != KindInt || tail.Int != -9999 {
		t.Errorf("trailing int4 decoded as (kind %d, %d) over %d bytes, want (KindInt, -9999) over 4",
			tail.Kind, tail.Int, tn)
	}
}
