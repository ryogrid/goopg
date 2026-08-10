package executor

import "testing"

// TestArraySubscriptYieldsElementTypeDatum is the defect this slice exists for
// (M0119-0006, deferral-ledger row 2026-08-11).
//
// The array-element storage slice made `interval[]` / `numeric[]` hold PG's own
// element images, but `c[1] = c[2]` over `ARRAY['1 mon','30 days']::interval[]`
// still answered `f` where PG 18.3 answers `t`. Storage was no longer the cause:
// the array-subscript evaluator sliced the array's TEXT and handed back a
// KindString, so compareDatum never reached its interval_cmp_value / numeric
// ladders and compared the two element SPELLINGS instead of their values.
//
// Two halves had to move together, which is why this test is end-to-end rather
// than a compareDatum unit test:
//   - planner exprType's array_subscript arm only recognised an element type
//     spelled "_elem"; a user array column is catalog.Type{Name:<ELEM>,
//     IsArray:true}, so it reported text. resolveExpr now stamps the computed
//     element type onto FuncCall.ReturnType.
//   - the executor arm re-types the element text through that element type's
//     input path (arraySubscriptElemDatum).
//
// Every `want` below was captured from the PG 18.3 reference cluster (port
// 65432) on 2026-08-11; the ones marked WAS-WRONG were the pre-fix answers.
func TestArraySubscriptYieldsElementTypeDatum(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE ai (c interval[], n numeric[], u uuid[], s text[], f float8[], g float4[], d date[], b bool[])"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if err := runDDL(t, ctx, "INSERT INTO ai VALUES ("+
		"ARRAY['1 mon','30 days','2 hours']::interval[], "+
		"ARRAY['1.50','1.5','10','9']::numeric[], "+
		"ARRAY['00000000-0000-0000-0000-000000000001','00000000-0000-0000-0000-000000000002']::uuid[], "+
		"ARRAY['b','a'], "+
		"ARRAY[9.5,10.2]::float8[], "+
		"ARRAY[9.5,10.2]::float4[], "+
		"ARRAY['2026-01-05','2026-01-10']::date[], "+
		"ARRAY[true,false])"); err != nil {
		t.Fatalf("INSERT: %v", err)
	}

	cases := []struct {
		sql  string
		want string
	}{
		// WAS-WRONG (f): a month and 30 days are equal to interval_cmp_value,
		// but "1 mon" sorts before "30 days" bytewise.
		{"SELECT c[1] = c[2] FROM ai", "t"},
		// Not a wrong answer before (value and spelling happen to agree here),
		// but pinned so the re-typing cannot invert an ordering it used to get
		// right by accident.
		{"SELECT c[3] > c[2] FROM ai", "f"},
		{"SELECT c[2] > c[3] FROM ai", "t"},
		// Rendering must not regress: the subscript still prints the element
		// exactly as the array codec spelled it (interval_out).
		{"SELECT c[1] FROM ai", "1 mon"},
		{"SELECT c[3] FROM ai", "02:00:00"},
		// WAS-WRONG (f): numeric equality is value-based, so 1.50 = 1.5.
		{"SELECT n[1] = n[2] FROM ai", "t"},
		// WAS-WRONG (f): '10' > '9' numerically, '1' < '9' as text.
		{"SELECT n[3] > n[4] FROM ai", "t"},
		// Display scale survives the re-typing (PG prints 1.50, not 1.5).
		{"SELECT n[1] FROM ai", "1.50"},
		// Arithmetic on a subscript now runs the numeric path, not a string.
		{"SELECT n[1] + n[3] FROM ai", "11.50"},
		// Unchanged element classes: uuid and text already compared correctly
		// as text (their spellings sort like their values), and must stay so.
		{"SELECT u[1] < u[2] FROM ai", "t"},
		{"SELECT s[1] > s[2] FROM ai", "t"},
		{"SELECT u[1] FROM ai", "00000000-0000-0000-0000-000000000001"},
		// Out-of-range and NULL handling are untouched by the re-typing.
		{"SELECT c[9] IS NULL FROM ai", "t"},
		// WAS-WRONG (t): float elements shared the defect — "9.5" > "10.2"
		// bytewise. goopg has no KindFloat, so a float8 COLUMN is already
		// KindNumeric and the element now agrees with it.
		{"SELECT f[1] > f[2] FROM ai", "f"},
		{"SELECT f[1] FROM ai", "9.5"},
		{"SELECT f[1] + f[2] FROM ai", "19.7"},
		{"SELECT g[1] > g[2] FROM ai", "f"},
		{"SELECT g[2] FROM ai", "10.2"},
		// date and bool elements are NOT re-typed (their spellings already sort
		// like their values); pinned so the scope note stays honest.
		{"SELECT d[1] < d[2] FROM ai", "t"},
		{"SELECT d[1] FROM ai", "2026-01-05"},
		{"SELECT b[1] > b[2] FROM ai", "t"},
	}
	for _, c := range cases {
		t.Run(c.sql, func(t *testing.T) {
			rows := runQuery(t, ctx, c.sql)
			if len(rows) != 1 {
				t.Fatalf("%s: got %d rows, want 1", c.sql, len(rows))
			}
			if got := rows[0][0].Format(); got != c.want {
				t.Errorf("%s = %q, want %q (PG 18.3)", c.sql, got, c.want)
			}
		})
	}
}

// TestArraySubscriptIntAndConstructorUnchanged guards the two subscript shapes
// that already worked, because the planner half of the fix changed what
// exprType reports for EVERY array subscript, not just interval/numeric: an
// int4[] element must still come back as an integer Datum (psql alignment +
// numeric comparison), and an ARRAY[...] constructor subscript — which has no
// column type behind it — must still infer from the element text.
func TestArraySubscriptIntAndConstructorUnchanged(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE ai2 (a int4[], s text[])"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if err := runDDL(t, ctx, "INSERT INTO ai2 VALUES (ARRAY[10,9,2], ARRAY['x','y'])"); err != nil {
		t.Fatalf("INSERT: %v", err)
	}

	cases := []struct {
		sql  string
		want string
	}{
		{"SELECT a[1] > a[2] FROM ai2", "t"},
		{"SELECT a[1] + a[3] FROM ai2", "12"},
		{"SELECT a[2] FROM ai2", "9"},
		{"SELECT s[2] FROM ai2", "y"},
		{"SELECT (ARRAY[10,9,2])[1] > (ARRAY[10,9,2])[2] FROM ai2", "t"},
		{"SELECT (ARRAY['b','a'])[1] > (ARRAY['b','a'])[2] FROM ai2", "t"},
	}
	for _, c := range cases {
		t.Run(c.sql, func(t *testing.T) {
			rows := runQuery(t, ctx, c.sql)
			if len(rows) != 1 {
				t.Fatalf("%s: got %d rows, want 1", c.sql, len(rows))
			}
			if got := rows[0][0].Format(); got != c.want {
				t.Errorf("%s = %q, want %q (PG 18.3)", c.sql, got, c.want)
			}
		})
	}
}
