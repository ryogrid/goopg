package executor

import (
	"strings"
	"testing"
)

// TestNumericColumnTypmodAppliedOnWrite pins M0146-0087 against PG 18.3.
//
// A numeric(p,s) column's typmod was never applied when a value was stored:
// 1.26 into numeric(4,1) stayed 1.26 (PG 1.3), 2.5 into numeric(6,2) read
// back as 2.5 (PG 2.50), and an out-of-range value was stored instead of
// raising 22003 `numeric field overflow`. PG's apply_typmod (numeric.c) runs
// on every write: INSERT (VALUES, SELECT, DEFAULT), UPDATE, MERGE, ON
// CONFLICT, COPY FROM and ALTER COLUMN TYPE. Every want is PG's output.
func TestNumericColumnTypmodAppliedOnWrite(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	for _, q := range []string{
		"CREATE TABLE nt (id int PRIMARY KEY, n numeric(6,2), m numeric(4,1), k numeric(3), d numeric(5,2) DEFAULT 1.5, u numeric)",
		"INSERT INTO nt VALUES (1, 2.5, 1.26, 4.5, DEFAULT, 1.50)",
		"INSERT INTO nt VALUES (2, '3', 7, '-2.5', 9.999, 2.500)",
		"INSERT INTO nt (id, n) SELECT 3, 1.005",
		"UPDATE nt SET n = 1.234 WHERE id = 1",
		"UPDATE nt SET m = m + 0.05 WHERE id = 2",
		"INSERT INTO nt (id, n) VALUES (3, 1) ON CONFLICT (id) DO UPDATE SET n = 7.777",
		"MERGE INTO nt USING (SELECT 4 AS id, 2.226 AS v) s ON nt.id = s.id WHEN NOT MATCHED THEN INSERT (id, n) VALUES (s.id, s.v)",
		"MERGE INTO nt USING (SELECT 4 AS id, 3.335 AS v) s ON nt.id = s.id WHEN MATCHED THEN UPDATE SET m = s.v",
	} {
		runSQL(t, ctx, q)
	}
	if got := strings.Join(renderRows(runSQL(t, ctx, "SELECT id, n, m, k, d, u FROM nt ORDER BY id")), ";"); got !=
		"1|1.23|1.3|5|1.50|1.50;2|3.00|7.1|-3|10.00|2.500;3|7.78|NULL|NULL|1.50|NULL;4|2.23|3.3|NULL|1.50|NULL" {
		t.Errorf("stored values = %s\nwant PG's 1|1.23|1.3|5|1.50|1.50;2|3.00|7.1|-3|10.00|2.500;3|7.78|NULL|NULL|1.50|NULL;4|2.23|3.3|NULL|1.50|NULL", got)
	}
	for _, c := range []struct{ sql, detail string }{
		{"INSERT INTO nt (id, m) VALUES (10, 999.95)", "A field with precision 4, scale 1 must round to an absolute value less than 10^3."},
		{"INSERT INTO nt (id, k) VALUES (11, 1000)", "A field with precision 3, scale 0 must round to an absolute value less than 10^3."},
		{"UPDATE nt SET n = 10000 WHERE id = 1", "A field with precision 6, scale 2 must round to an absolute value less than 10^4."},
	} {
		_, err := runSQLCtxErr(t, ctx, c.sql)
		ee, ok := err.(*ExecError)
		if !ok || ee.Code != "22003" || ee.Message != "numeric field overflow" || ee.Detail != c.detail {
			t.Errorf("%s: err = %v, want 22003 numeric field overflow / %s", c.sql, err, c.detail)
		}
	}

	// ALTER COLUMN TYPE to the same type name with a new typmod rewrites the
	// rows; it used to be skipped as a no-op.
	runSQL(t, ctx, "CREATE TABLE nc (x numeric(6,3))")
	runSQL(t, ctx, "INSERT INTO nc VALUES (1.2345), (2.5)")
	runSQL(t, ctx, "ALTER TABLE nc ALTER COLUMN x TYPE numeric(5,1)")
	if got := strings.Join(renderRows(runSQL(t, ctx, "SELECT x FROM nc ORDER BY x")), ";"); got != "1.2;2.5" {
		t.Errorf("after ALTER TYPE numeric(5,1) = %s, want PG's 1.2;2.5", got)
	}

	// apply_typmod edge cases: zero never overflows, numeric(2,2) holds
	// values below 1, rounding can raise the integer digit count.
	for _, c := range []struct {
		in         Datum
		prec, sc   int
		want, werr string
	}{
		{NewNumericInt64Datum(0, 0), 1, 1, "0.0", ""},
		{NewNumericInt64Datum(994, 3), 2, 2, "0.99", ""},
		{NewNumericInt64Datum(995, 3), 2, 2, "", "less than 1."},
		{NewNumericInt64Datum(-1255, 3), 4, 2, "-1.26", ""},
		{NewIntDatum(7), 4, 1, "7.0", ""},
		{NewStringDatum("1.005"), 6, 2, "1.01", ""},
	} {
		got, err := applyNumericTypmod(c.in, c.prec, c.sc)
		if c.werr != "" {
			if ee, ok := err.(*ExecError); !ok || !strings.HasSuffix(ee.Detail, c.werr) {
				t.Errorf("applyNumericTypmod(%s, %d, %d) err = %v, want overflow %q", c.in.Format(), c.prec, c.sc, err, c.werr)
			}
			continue
		}
		if err != nil || got.Format() != c.want {
			t.Errorf("applyNumericTypmod(%s, %d, %d) = %s, %v; want PG's %s", c.in.Format(), c.prec, c.sc, got.Format(), err, c.want)
		}
	}
}
