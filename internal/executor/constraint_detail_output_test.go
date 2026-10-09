package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestConstraintDetailUsesTypeOutput pins M0146-0083 against PG 18.3.
//
// Constraint-violation DETAILs rendered each value with Datum.Format: a date
// as 01-02-2020, a timestamp with six fraction digits, a time with a
// 1970-01-01 date, and a DEFAULT still in its input text ('1 day 2 hours').
// PG runs each value through its type's output function under the session
// DateStyle (ExecBuildSlotValueDescription, BuildIndexValueDescription,
// ri_ReportViolation). Every want is PG's output.
func TestConstraintDetailUsesTypeOutput(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	dateStyle := "ISO, MDY"
	ctx.GetSetting = func(name string) (string, bool) {
		switch name {
		case "datestyle":
			return dateStyle, true
		case "timezone":
			return "UTC", true
		}
		return "", false
	}
	for _, q := range []string{
		`CREATE TABLE dz (a int NOT NULL, d date DEFAULT '2020-01-02', ts timestamp DEFAULT '2020-01-02 03:04:05.5',
			tm time DEFAULT '10:11', iv interval DEFAULT '1 day 2 hours', b bool DEFAULT true)`,
		"CREATE TABLE cz (a int, d date, CHECK (a > 0))",
		"CREATE TABLE uz (d date PRIMARY KEY)",
		"INSERT INTO uz VALUES ('2020-01-02')",
		"CREATE TABLE fkc (d date REFERENCES uz)",
	} {
		runSQL(t, ctx, q)
	}
	cases := []struct{ sql, iso, sqlDMY string }{
		{"INSERT INTO dz (d) VALUES (DEFAULT)",
			"Failing row contains (null, 2020-01-02, 2020-01-02 03:04:05.5, 10:11:00, 1 day 02:00:00, t).",
			"Failing row contains (null, 02/01/2020, 02/01/2020 03:04:05.5, 10:11:00, 1 day 02:00:00, t)."},
		{"INSERT INTO cz VALUES (0, '2020-01-02')",
			"Failing row contains (0, 2020-01-02).",
			"Failing row contains (0, 02/01/2020)."},
		{"INSERT INTO uz VALUES ('2020-01-02')",
			"Key (d)=(2020-01-02) already exists.",
			"Key (d)=(02/01/2020) already exists."},
		{"INSERT INTO fkc VALUES ('2021-03-04')",
			`Key (d)=(2021-03-04) is not present in table "uz".`,
			`Key (d)=(04/03/2021) is not present in table "uz".`},
	}
	for _, style := range []string{"ISO, MDY", "SQL, DMY"} {
		dateStyle = style
		for _, c := range cases {
			want := c.iso
			if style == "SQL, DMY" {
				want = c.sqlDMY
			}
			_, err := runSQLCtxErr(t, ctx, c.sql)
			ee, ok := err.(*ExecError)
			if !ok {
				t.Errorf("[%s] %s: err = %v, want a constraint violation", style, c.sql, err)
				continue
			}
			if ee.Detail != want {
				t.Errorf("[%s] %s\nDETAIL %q\nwant PG's %q", style, c.sql, ee.Detail, want)
			}
		}
	}

	// COPY TO's text encoder had no interval arm ("kind 6 cannot encode as
	// interval in COPY TEXT").
	iv := NewIntervalDatumFull(0, 1, 2*3600*1000000)
	if s, err := datumToCopyText(catalog.Type{Name: "interval"}, iv, "ISO", "MDY", "", "hex", nil, false); err != nil || s != "1 day 02:00:00" {
		t.Errorf("COPY text of interval = %q, %v; want PG's \"1 day 02:00:00\"", s, err)
	}
	if !strings.Contains(formatRowForDetail(nil, []catalog.Column{{Name: "x", Type: catalog.Type{Name: "int4"}}}, Row{NullDatum}), "(null)") {
		t.Errorf("a NULL value must render as null")
	}
}
