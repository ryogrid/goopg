package executor

// End-to-end gate for the array arm of indexKeyIsDecodable (M0119-0006, design
// docs/design/0119-0006-array-index-key-decodability.md).

import "testing"

// TestArrayIndexOnlyScanReadsHeapForRefusedElement reproduces the defect this
// slice fixed. An `interval[]` (or `date[]`) column is indexable, the page is
// ALL_VISIBLE, and the planner promotes the query to an IndexOnlyScan — so
// before the fix the scan answered FROM the key, hit decodeArrayKeyElemText's
// refusal mid-decode, and failed the whole SELECT with
//
//	XX000: IOS decode: btree: interval key is the comparison span …
//
// The predicate now declines the index up front and the scan reads the heap,
// which is what the scalar `interval` case has done since the interval-key
// slice. Both element types are covered because they are refused for DIFFERENT
// reasons — interval's element key is not invertible at all, date's decodes but
// has no rendering that agrees with the heap-side array text.
func TestArrayIndexOnlyScanReadsHeapForRefusedElement(t *testing.T) {
	cases := []struct {
		name, colType, insert, probe, want string
	}{
		{"interval", "interval[]", "{1 mon,2 hours}", "{3 days}", `{"3 days"}`},
		{"date", "date[]", "{2020-01-02}", "{2021-03-04}", "{2021-03-04}"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx, cleanup := newVMFixture(t)
			defer cleanup()

			runComposite(t, ctx,
				"CREATE TABLE arr_ios (a "+c.colType+")",
				"CREATE INDEX arr_ios_idx ON arr_ios (a)",
				"INSERT INTO arr_ios VALUES ('"+c.insert+"')",
				"INSERT INTO arr_ios VALUES ('"+c.probe+"')",
			)
			vacuumThen(t, ctx, "arr_ios")

			q := "SELECT a FROM arr_ios WHERE a = '" + c.probe + "'"
			if ios := findIndexOnlyScan(planOne(t, q, ctx.Catalog)); ios == nil {
				t.Skip("planner did not promote to IndexOnlyScan; the fast path is not reachable")
			}
			rows := runQuery(t, ctx, q)
			if len(rows) != 1 {
				t.Fatalf("rows=%d want 1 (%v)", len(rows), rows)
			}
			if got := rows[0][0].Format(); got != c.want {
				t.Errorf("row=%q want %q", got, c.want)
			}
		})
	}
}
