package executor

import (
	"strings"
	"testing"
)

// TestDomainTypmodIsEnforced pins M0149-0001h1a39 against PG 18.3's results: a
// domain's declared length and precision apply to its columns and casts.
func TestDomainTypmodIsEnforced(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	exec := func(sql string) error {
		_, err := runSQLCtxErr(t, ctx, sql)
		return err
	}
	mustExec := func(sql string) {
		t.Helper()
		if err := exec(sql); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	mustFailTooLong := func(sql string) {
		t.Helper()
		err := exec(sql)
		if err == nil || !strings.Contains(err.Error(), "value too long for type character varying(5)") {
			t.Fatalf("%s: error = %v, want value too long", sql, err)
		}
	}
	one := func(sql string) string {
		t.Helper()
		rows, err := runQueryErr(t, ctx, sql)
		if err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		if len(rows) != 1 || len(rows[0]) != 1 {
			t.Fatalf("%s: %d rows, want one value", sql, len(rows))
		}
		return rows[0][0].Format()
	}

	mustExec(`CREATE DOMAIN d5 AS varchar(5)`)
	mustExec(`CREATE DOMAIN d5b AS d5`)
	mustExec(`CREATE DOMAIN dn AS numeric(4,1)`)
	mustExec(`CREATE DOMAIN dp AS numeric(3)`)
	mustExec(`CREATE DOMAIN db AS bit(3)`)
	mustExec(`CREATE TABLE td (a d5, b d5b, n dn)`)

	// Columns, directly and through a domain over a domain.
	mustFailTooLong(`INSERT INTO td (a) VALUES ('123456')`)
	mustFailTooLong(`INSERT INTO td (b) VALUES ('123456')`)
	mustExec(`INSERT INTO td (a, n) VALUES ('12345', 123.456)`)
	if got := one(`SELECT n FROM td`); got != "123.5" {
		t.Errorf("numeric(4,1) domain column stored %s, want 123.5", got)
	}
	mustFailTooLong(`UPDATE td SET a = '123456'`)

	// An explicit cast truncates and rounds instead of failing.
	for sql, want := range map[string]string{
		`SELECT 'abcdefg'::d5`:  "abcde",
		`SELECT 'abcdefg'::d5b`: "abcde",
		`SELECT 1.26::dn`:       "1.3",
		`SELECT 12.6::dp`:       "13",
		`SELECT '1111'::db`:     "111",
	} {
		if got := one(sql); got != want {
			t.Errorf("%s = %s, want %s", sql, got, want)
		}
	}

	// ADD COLUMN and ALTER COLUMN TYPE resolve the domain as CREATE TABLE does.
	mustExec(`CREATE TABLE ta (x int)`)
	mustExec(`ALTER TABLE ta ADD COLUMN a d5`)
	mustFailTooLong(`INSERT INTO ta (a) VALUES ('123456')`)
	mustExec(`CREATE TABLE tc (a varchar(10))`)
	mustExec(`INSERT INTO tc VALUES ('12')`)
	mustExec(`ALTER TABLE tc ALTER COLUMN a TYPE d5`)
	mustFailTooLong(`INSERT INTO tc VALUES ('123456')`)
}

// TestAlterColumnTypeFailureKeepsRows: a value the new typmod rejects must fail
// before the heap is rewritten. The check used to run after the truncate, so the
// failed statement left the table empty.
func TestAlterColumnTypeFailureKeepsRows(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	for _, sql := range []string{
		`CREATE TABLE tk (a varchar(10))`,
		`INSERT INTO tk VALUES ('1234567'), ('12')`,
	} {
		if _, err := runSQLCtxErr(t, ctx, sql); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	_, err := runSQLCtxErr(t, ctx, `ALTER TABLE tk ALTER COLUMN a TYPE varchar(5)`)
	if err == nil || !strings.Contains(err.Error(), "value too long") {
		t.Fatalf("ALTER error = %v, want value too long", err)
	}
	rows, err := runQueryErr(t, ctx, `SELECT a FROM tk`)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("%d rows after the failed ALTER, want 2", len(rows))
	}
}
