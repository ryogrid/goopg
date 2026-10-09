package executor

import (
	"strings"
	"testing"
)

// TestMergeInsertFillsDefaultsAndChecksConstraints pins M0146-0075 against
// PG 18.3. MERGE's WHEN NOT MATCHED THEN INSERT evaluated only a weak subset
// of column defaults, so an omitted column whose DEFAULT is an ordinary
// expression (here upper('q')) was stored NULL. The INSERT action also:
//   - ran no NOT NULL, CHECK or domain constraint (a NULL key was stored);
//   - swallowed a value's evaluation error (division by zero stored NULL);
//   - left generated columns out of the default target list, shifting the
//     later values one column left;
//   - rejected DEFAULT in its VALUES list.
//
// The UPDATE action checked no constraint either. Every want is PG's output
// for the same script.
func TestMergeInsertFillsDefaultsAndChecksConstraints(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	for _, q := range []string{
		"CREATE SEQUENCE ms",
		"CREATE TABLE mv (a int PRIMARY KEY, k text NOT NULL DEFAULT upper('q'), s int DEFAULT nextval('ms'), " +
			"g int GENERATED ALWAYS AS (a * 2) STORED, b int CHECK (b > 0), d text DEFAULT 'dd' || 'x')",
		"CREATE TABLE src (a int)",
		"INSERT INTO src VALUES (1), (2)",
	} {
		runSQL(t, ctx, q)
	}
	want := func(label, wantRows string) {
		t.Helper()
		if got := strings.Join(renderRows(runSQL(t, ctx, "SELECT a, k, s, g, b, d FROM mv ORDER BY a")), ";"); got != wantRows {
			t.Errorf("%s:\ngot  %s\nwant %s", label, got, wantRows)
		}
	}
	runSQL(t, ctx, "MERGE INTO mv USING src ON mv.a = src.a WHEN NOT MATCHED THEN INSERT (a) VALUES (src.a)")
	want("omitted columns", "1|Q|1|2|NULL|ddx;2|Q|2|4|NULL|ddx")
	runSQL(t, ctx, "MERGE INTO mv USING (VALUES (3)) v(a) ON mv.a = v.a "+
		"WHEN NOT MATCHED THEN INSERT VALUES (v.a, DEFAULT, DEFAULT, DEFAULT, 7, DEFAULT)")
	want("DEFAULT markers, generated column in the target list", "1|Q|1|2|NULL|ddx;2|Q|2|4|NULL|ddx;3|Q|3|6|7|ddx")

	for _, c := range []struct{ sql, err string }{
		{"MERGE INTO mv USING (VALUES (4)) v(a) ON mv.a = v.a WHEN NOT MATCHED THEN INSERT (a, k) VALUES (v.a, NULL)",
			`null value in column "k" of relation "mv" violates not-null constraint`},
		{"MERGE INTO mv USING (VALUES (4)) v(a) ON mv.a = v.a WHEN NOT MATCHED THEN INSERT DEFAULT VALUES",
			`null value in column "a" of relation "mv" violates not-null constraint`},
		{"MERGE INTO mv USING (VALUES (4)) v(a) ON mv.a = v.a WHEN NOT MATCHED THEN INSERT (a, b) VALUES (v.a, -1)",
			`new row for relation "mv" violates check constraint "mv_b_check"`},
		{"MERGE INTO mv USING (VALUES (4)) v(a) ON mv.a = v.a WHEN NOT MATCHED THEN INSERT (a, b) VALUES (v.a, 1/0)",
			"division by zero"},
		{"MERGE INTO mv USING (VALUES (1)) v(a) ON mv.a = v.a WHEN MATCHED THEN UPDATE SET k = NULL",
			`null value in column "k" of relation "mv" violates not-null constraint`},
		{"MERGE INTO mv USING (VALUES (1)) v(a) ON mv.a = v.a WHEN MATCHED THEN UPDATE SET b = -3",
			`new row for relation "mv" violates check constraint "mv_b_check"`},
		{"MERGE INTO mv USING (VALUES (4)) v(a) ON mv.a = v.a WHEN NOT MATCHED THEN INSERT (a, g) VALUES (v.a, 8)",
			`cannot insert a non-DEFAULT value into column "g"`},
		{"MERGE INTO mv USING (VALUES (4)) v(a) ON mv.a = v.a WHEN NOT MATCHED THEN INSERT (a) VALUES (v.a, 1)",
			"INSERT has more expressions than target columns"},
		{"MERGE INTO mv USING (VALUES (4)) v(a) ON mv.a = v.a WHEN NOT MATCHED THEN INSERT (a, b) VALUES (v.a)",
			"INSERT has more target columns than expressions"},
	} {
		if _, err := runSQLCtxErr(t, ctx, c.sql); err == nil || !strings.Contains(err.Error(), c.err) {
			t.Errorf("%s: err = %v, want %q", c.sql, err, c.err)
		}
	}
}
