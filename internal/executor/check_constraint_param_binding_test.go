package executor

import "testing"

// TestCheckConstraintValuesBindAsParams pins review/260831-2 EO1-9:
// checkConstraints used to re-render each column value with Datum.Format()
// and interpolate it back into a synthetic
// `SELECT (expr) FROM (VALUES (…)) AS _chk(…)` statement. Format() is a
// DISPLAY rendering, not a literal guaranteed to re-parse to the same value
// in its own type — for a `date` column it renders "05-06-2020", which the
// re-parse rejects, so a perfectly valid INSERT (PG 18.3 accepts it) died
// with XX000 "internal error: could not evaluate check constraint". The
// values now ride as bound parameters, so no render/re-parse round trip
// happens at all.
func TestCheckConstraintValuesBindAsParams(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	cases := []struct{ name, ddl, ins string }{
		{"date", `CREATE TABLE ck_date (d date CHECK (d > '2000-01-01'))`, `INSERT INTO ck_date VALUES ('2020-05-06')`},
		{"bytea", `CREATE TABLE ck_bytea (b bytea CHECK (length(b) > 1))`, `INSERT INTO ck_bytea VALUES ('\x0102'::bytea)`},
		{"timestamp", `CREATE TABLE ck_ts (t timestamp CHECK (t > '2000-01-01'))`, `INSERT INTO ck_ts VALUES ('2020-03-04 05:06:07.891')`},
		{"interval", `CREATE TABLE ck_iv (i interval CHECK (i > interval '1 second'))`, `INSERT INTO ck_iv VALUES ('1 day 2 hours')`},
		{"float8", `CREATE TABLE ck_f8 (f float8 CHECK (f > 0))`, `INSERT INTO ck_f8 VALUES (1.5e-2)`},
		{"array", `CREATE TABLE ck_arr (a int[] CHECK (array_length(a, 1) > 1))`, `INSERT INTO ck_arr VALUES ('{1,2}')`},
		{"text-quotes", `CREATE TABLE ck_txt (s text CHECK (length(s) > 1))`, `INSERT INTO ck_txt VALUES (E'a\\b''c')`},
	}
	for _, c := range cases {
		if err := runDDL(t, ctx, c.ddl); err != nil {
			t.Fatalf("%s: DDL %q: %v", c.name, c.ddl, err)
		}
		if _, err := runSQLCtxErr(t, ctx, c.ins); err != nil {
			t.Errorf("%s: INSERT satisfying its CHECK failed: %v", c.name, err)
		}
	}

	// The constraint must still be ENFORCED — parameter binding is not a
	// way to skip evaluation.
	if _, err := runSQLCtxErr(t, ctx, `INSERT INTO ck_date VALUES ('1999-01-01')`); err == nil {
		t.Error("INSERT violating the date CHECK was accepted")
	} else if ee, ok := err.(*ExecError); !ok || ee.Code != "23514" {
		t.Errorf("violating INSERT: err = %v, want *ExecError{Code: 23514}", err)
	}
}

// TestDomainCheckDateIsEnforced pins the second half of EO1-9:
// evalDomainCheckExpr interpolated Format() text the same way, and it treats
// EVERY evaluation failure as a PASS — so a domain whose base type does not
// round-trip through Format() (date) was silently not enforced at all. PG
// 18.3 oracle: `ERROR: value for domain zzdd violates check constraint
// "zzdd_check"` (23514).
func TestDomainCheckDateIsEnforced(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE DOMAIN dd AS date CHECK (VALUE > '2000-01-01')`); err != nil {
		t.Fatalf("CREATE DOMAIN: %v", err)
	}
	if err := runDDL(t, ctx, `CREATE TABLE dt (d dd)`); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}

	_, err := runSQLCtxErr(t, ctx, `INSERT INTO dt VALUES ('1999-01-01')`)
	if err == nil {
		t.Fatal("INSERT violating the date domain CHECK was accepted")
	}
	if ee, ok := err.(*ExecError); !ok || ee.Code != "23514" {
		t.Errorf("err = %v, want *ExecError{Code: 23514}", err)
	}
	if _, err := runSQLCtxErr(t, ctx, `INSERT INTO dt VALUES ('2020-05-06')`); err != nil {
		t.Errorf("INSERT satisfying the date domain CHECK failed: %v", err)
	}
}
