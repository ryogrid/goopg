package executor

import "testing"

// TestDateTimeReservedLiteral pins the RESERV keyword spellings PG's
// DecodeDateTime / DecodeTimeOnly (postgres/src/backend/utils/adt/datetime.c
// datetbl) accept for date/time/timestamp(tz) input beyond the ±infinity
// pair (already covered by TestTimestampInfinityLiteral / date's sibling):
// 'now' (DTK_NOW), 'today' (DTK_TODAY), 'tomorrow' (DTK_TOMORROW),
// 'yesterday' (DTK_YESTERDAY) and 'epoch' (DTK_EPOCH). Before M0134-0182
// none of these parsed at all — `'today'::date` raised 22007 — which is
// what made regress/type_sanity.sql's `tab_core_types` CTAS never create
// the table (case sized live: M0134-0182).
//
// Every assertion is self-consistent WITHIN one statement (comparing the
// literal against now()/current_date computed in the SAME statement,
// sharing the SAME captured ctx.Now) rather than against a hardcoded wall
// clock value, since these literals are inherently time-relative.
func TestDateTimeReservedLiteral(t *testing.T) {
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
		// date domain
		{"SELECT 'today'::date = current_date FROM t", "t"},
		{"SELECT 'now'::date = current_date FROM t", "t"},
		{"SELECT 'tomorrow'::date = current_date + 1 FROM t", "t"},
		{"SELECT 'yesterday'::date = current_date - 1 FROM t", "t"},
		{"SELECT 'epoch'::date FROM t", "01-01-1970"},
		{"SELECT date 'TODAY' = current_date FROM t", "t"}, // case-insensitive
		{"SELECT '  today  '::date = current_date FROM t", "t"}, // trimmed
		// timestamp / timestamptz domain
		{"SELECT 'now'::timestamp = now()::timestamp FROM t", "t"},
		{"SELECT 'now'::timestamptz = now() FROM t", "t"},
		{"SELECT 'today'::timestamp = current_date::timestamp FROM t", "t"},
		{"SELECT 'tomorrow'::timestamptz = (current_date + 1)::timestamptz FROM t", "t"},
		{"SELECT 'yesterday'::timestamp = (current_date - 1)::timestamp FROM t", "t"},
		{"SELECT 'epoch'::timestamp FROM t", "1970-01-01 00:00:00.000000"},
		{"SELECT timestamp 'now' = now()::timestamp FROM t", "t"},
		// time / timetz domain — only 'now' is a RESERV token here.
		{"SELECT 'now'::time = now()::time FROM t", "t"},
		{"SELECT 'now'::timetz IS NOT NULL FROM t", "t"},
		{"SELECT time 'now' = now()::time FROM t", "t"},
		// pg_input_is_valid recognises the new spellings too (M0134-0182).
		{"SELECT pg_input_is_valid('today', 'date') FROM t", "t"},
		{"SELECT pg_input_is_valid('now', 'timestamp') FROM t", "t"},
		{"SELECT pg_input_is_valid('now', 'time') FROM t", "t"},
		{"SELECT pg_input_is_valid('gibberish', 'date') FROM t", "f"},
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
