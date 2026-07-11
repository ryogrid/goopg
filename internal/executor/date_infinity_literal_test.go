package executor

import (
	"encoding/binary"
	"math"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestDateInfinityLiteral pins the date-'infinity' LITERAL INPUT path
// (unimplemented_feat #5(d-iv) follow-up, the last open row of the interval/
// infinity group). The date type shares the KindTime INT64-extremes sentinel
// carrier with timestamp for its internal value, but PG's date_in maps the
// special 'infinity'/'+infinity' (DTK_LATE) and '-infinity' (DTK_EARLY)
// spellings to a DIFFERENT wire domain — DATEVAL_NOEND / DATEVAL_NOBEGIN =
// PG_INT32_MAX / PG_INT32_MIN days (date.h). Before this the typed-literal
// (`date 'infinity'`), cast (`'infinity'::date`), pg_input_is_valid and codec
// paths all funnelled through time.Parse / parseCopyTimestamp, none of which
// recognises those spellings. All are now intercepted by
// parseDateInfinityLiteral. Every `want` matches upstream PostgreSQL 18.3.
func TestDateInfinityLiteral(t *testing.T) {
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
		// Typed-literal grammar (`date 'infinity'`, TypedStringLit path).
		{"SELECT date 'infinity' FROM t", "infinity"},
		{"SELECT date '-infinity' FROM t", "-infinity"},
		{"SELECT date '+infinity' FROM t", "infinity"},
		// Case-insensitive + surrounding whitespace.
		{"SELECT date 'Infinity' FROM t", "infinity"},
		{"SELECT date '  -INFINITY  ' FROM t", "-infinity"},
		// Cast grammar (`'infinity'::date`, evalCast path).
		{"SELECT 'infinity'::date FROM t", "infinity"},
		{"SELECT '-infinity'::date FROM t", "-infinity"},
		{"SELECT CAST('infinity' AS date) FROM t", "infinity"},
		// A ±infinity TIMESTAMP casts to the same-signed date infinity.
		{"SELECT (timestamp 'infinity')::date FROM t", "infinity"},
		{"SELECT (timestamp '-infinity')::date FROM t", "-infinity"},
		// isfinite() over the freshly-parsed date sentinel.
		{"SELECT isfinite(date 'infinity') FROM t", "f"},
		{"SELECT isfinite('-infinity'::date) FROM t", "f"},
		// pg_input_is_valid recognises the infinity spellings for date.
		{"SELECT pg_input_is_valid('infinity', 'date') FROM t", "t"},
		{"SELECT pg_input_is_valid('-infinity', 'date') FROM t", "t"},
		{"SELECT pg_input_is_valid('not-a-date', 'date') FROM t", "f"},
		// Ordering: +infinity sorts above every finite date, -infinity below
		// (compareDatum KindTime orders by the Int sentinel).
		{"SELECT date 'infinity' > date '9999-12-31' FROM t", "t"},
		{"SELECT date '-infinity' < date '0001-01-01' FROM t", "t"},
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

// TestDateInfinityWireCodec pins the binary wire codec round-trip for the
// ±infinity date sentinels. Upstream date_send emits DATEVAL_NOEND /
// DATEVAL_NOBEGIN = PG_INT32_MAX / PG_INT32_MIN days; naively applying the
// PG-epoch day arithmetic on decode would overflow, so both encode and decode
// intercept the sentinel. Note the encoded value is INT32 days (4 bytes),
// distinct from timestamp's INT64 micros — the whole point of the date domain.
func TestDateInfinityWireCodec(t *testing.T) {
	for _, tc := range []struct {
		name       string
		d          Datum
		wantDays   int32
		wantPosInf bool
		wantNegInf bool
	}{
		{"posinf", NewDateInfinity(true), math.MaxInt32, true, false},
		{"neginf", NewDateInfinity(false), math.MinInt32, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			typ := catalog.Type{Name: "date"}
			enc, err := encodeValuePG(typ, tc.d)
			if err != nil {
				t.Fatalf("encode: %v", err)
			}
			if len(enc) != 4 {
				t.Fatalf("encoded len = %d, want 4", len(enc))
			}
			got := int32(binary.LittleEndian.Uint32(enc))
			if got != tc.wantDays {
				t.Errorf("encoded days = %d, want %d", got, tc.wantDays)
			}
			dec, n, err := decodePhysicalPGValueMctx(typ, enc, nil)
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if n != 4 {
				t.Errorf("decoded %d bytes, want 4", n)
			}
			if dec.IsTimestampPosInf() != tc.wantPosInf || dec.IsTimestampNegInf() != tc.wantNegInf {
				t.Errorf("decoded posinf=%v neginf=%v, want posinf=%v neginf=%v",
					dec.IsTimestampPosInf(), dec.IsTimestampNegInf(), tc.wantPosInf, tc.wantNegInf)
			}
		})
	}
}
