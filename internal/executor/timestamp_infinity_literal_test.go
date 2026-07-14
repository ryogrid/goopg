package executor

import (
	"encoding/binary"
	"math"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestTimestampInfinityLiteral pins the timestamp-'infinity' LITERAL INPUT path
// (unimplemented_feat #5(d-iv) follow-up). Before this, goopg could only reach
// the ±infinity KindTime sentinel through arithmetic short-circuits
// (`timestamp ± interval 'infinity'`); the typed-literal (`timestamp 'infinity'`),
// cast (`'infinity'::timestamp`) and pg_input_is_valid paths all funnelled
// through parseCopyTimestamp / the evalTypedStringLit layout list, neither of
// which recognises PG's special 'infinity' / '+infinity' (DTK_LATE) and
// '-infinity' (DTK_EARLY) spellings (datetime.c datetbl RESERV tokens). All are
// now intercepted by parseTimestampInfinityLiteral. Every `want` matches
// upstream PostgreSQL 18.3.
func TestTimestampInfinityLiteral(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE t (id int)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if err := runDDL(t, ctx, "INSERT INTO t VALUES (1)"); err != nil {
		t.Fatalf("INSERT: %v", err)
	}

	cases := []struct {
		sql  string
		want string
	}{
		// Typed-literal grammar (`timestamp 'infinity'`, TypedStringLit path).
		{"SELECT timestamp 'infinity' FROM t", "infinity"},
		{"SELECT timestamp '-infinity' FROM t", "-infinity"},
		{"SELECT timestamp '+infinity' FROM t", "infinity"},
		{"SELECT timestamptz 'infinity' FROM t", "infinity"},
		{"SELECT timestamptz '-infinity' FROM t", "-infinity"},
		// Case-insensitive + surrounding whitespace (RESERV token lookup
		// lowercases and the tokenizer trims).
		{"SELECT timestamp 'Infinity' FROM t", "infinity"},
		{"SELECT timestamp '  -INFINITY  ' FROM t", "-infinity"},
		// Cast grammar (`'infinity'::timestamp`, evalCast path).
		{"SELECT 'infinity'::timestamp FROM t", "infinity"},
		{"SELECT '-infinity'::timestamp FROM t", "-infinity"},
		{"SELECT CAST('infinity' AS timestamptz) FROM t", "infinity"},
		// isfinite() over the freshly-parsed literal sentinel (already wired,
		// exercised here end-to-end from literal input).
		{"SELECT isfinite(timestamp 'infinity') FROM t", "f"},
		{"SELECT isfinite('-infinity'::timestamp) FROM t", "f"},
		// pg_input_is_valid recognises the infinity spellings.
		{"SELECT pg_input_is_valid('infinity', 'timestamp') FROM t", "t"},
		{"SELECT pg_input_is_valid('-infinity', 'timestamptz') FROM t", "t"},
		{"SELECT pg_input_is_valid('not-a-ts', 'timestamp') FROM t", "f"},
		// Ordering: the +infinity sentinel sorts above every finite instant,
		// -infinity below (compareDatum KindTime orders by the Int sentinel).
		{"SELECT timestamp 'infinity' > timestamp '2262-01-01' FROM t", "t"},
		{"SELECT timestamp '-infinity' < timestamp '1900-01-01' FROM t", "t"},
	}
	for _, c := range cases {
		t.Run(c.sql, func(t *testing.T) {
			rows := runQuery(t, ctx, c.sql)
			if len(rows) != 1 {
				t.Fatalf("%s: got %d rows, want 1", c.sql, len(rows))
			}
			if rows[0][0].IsNull() {
				t.Fatalf("%s: got NULL, want %q", c.sql, c.want)
			}
			if got := rows[0][0].Format(); got != c.want {
				t.Errorf("%s = %q, want %q", c.sql, got, c.want)
			}
		})
	}
}

// TestTimestampInfinityWireCodec pins the binary wire codec round-trip for the
// ±infinity sentinels. Upstream timestamp_send emits DT_NOEND / DT_NOBEGIN =
// PG_INT64_MAX / PG_INT64_MIN microseconds; naively adding the PG-epoch offset
// on decode would overflow, so both encode and decode intercept the sentinel.
func TestTimestampInfinityWireCodec(t *testing.T) {
	for _, tc := range []struct {
		name       string
		d          Datum
		wantMicros int64
		wantPosInf bool
		wantNegInf bool
	}{
		{"posinf", NewTimestampInfinity(true), math.MaxInt64, true, false},
		{"neginf", NewTimestampInfinity(false), math.MinInt64, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			typ := catalog.Type{Name: "timestamp"}
			enc, err := encodeValuePG(typ, tc.d)
			if err != nil {
				t.Fatalf("encode: %v", err)
			}
			if len(enc) != 8 {
				t.Fatalf("encoded len = %d, want 8", len(enc))
			}
			got := int64(binary.LittleEndian.Uint64(enc))
			if got != tc.wantMicros {
				t.Errorf("encoded micros = %d, want %d", got, tc.wantMicros)
			}
			dec, n, err := decodePhysicalPGValueMctx(typ, enc, nil)
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if n != 8 {
				t.Errorf("decoded %d bytes, want 8", n)
			}
			if dec.IsTimestampPosInf() != tc.wantPosInf || dec.IsTimestampNegInf() != tc.wantNegInf {
				t.Errorf("decoded posinf=%v neginf=%v, want posinf=%v neginf=%v",
					dec.IsTimestampPosInf(), dec.IsTimestampNegInf(), tc.wantPosInf, tc.wantNegInf)
			}
		})
	}
}
