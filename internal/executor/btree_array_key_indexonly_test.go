package executor

// End-to-end gate for the array arm of indexKeyIsDecodable (M0119-0006, design
// docs/design/0119-0006-array-index-key-decodability.md).

import (
	"strings"
	"testing"
)

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
// slice. Both element types here are refused for the SAME structural reason —
// their B-tree key is upstream's lossy comparison span (interval_cmp_value, and
// timetz's GMT-equivalent time, which has folded the zone away) — so there is
// nothing left to render. `date` used to be a third case, refused for the other
// reason (no heap element image to agree with); the 26th slice gave it one and
// the 27th re-armed it, so it moved to TestArrayIndexOnlyScanAnswersFromKey.
func TestArrayIndexOnlyScanReadsHeapForRefusedElement(t *testing.T) {
	cases := []struct {
		name, colType, insert, probe, want string
	}{
		{"interval", "interval[]", "{1 mon,2 hours}", "{3 days}", `{"3 days"}`},
		{"timetz", "timetz[]", "{01:02:03+01}", "{04:05:06+02}", "{04:05:06+02}"},
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

// TestNumericIndexOnlyScanKeepsDisplayScale is the defect the 34th slice fixed,
// and it is a wrong-ANSWER defect rather than a failed query: `{2.70}` came back
// as `{2.7}` whenever the planner promoted the scan to an IndexOnlyScan over an
// ALL_VISIBLE page, and as `{2.70}` otherwise — the same stored row printing two
// ways depending on the plan. PG 18.3 prints `{2.70}` (numeric carries its
// display scale), which is also what goopg's own heap decode prints.
//
// The cause is not repairable in the encoder: EncodeNumericKey strips trailing
// mantissa zeros so numerically equal values encode to identical bytes, and that
// byte-identity is exactly what makes a UNIQUE index on numeric reject `1.00`
// after `1.0` (TestNumericUniqueCollapsesDisplayScale below holds that end
// down). The key cannot carry the scale and stay an equality-collapsing key, so
// the scan reads the heap instead — the same resolution `interval[]` gets.
//
// The scalar arm is here too because it is a DIFFERENT code path with the same
// exposure: it takes the single-column lane of decodeRowFromKey rather than the
// composite walk, and an index stored in the PG tuple-image format bypasses the
// blob decode entirely (the predicate is asked only of the blob format).
// Whichever route this fixture's index takes, the printed text must be the
// heap's.
func TestNumericIndexOnlyScanKeepsDisplayScale(t *testing.T) {
	cases := []struct {
		name, colType, insert, probe, want string
	}{
		{"scalar", "numeric", "1.50", "2.70", "2.70"},
		{"array", "numeric[]", "{1.50}", "{2.70}", "{2.70}"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx, cleanup := newVMFixture(t)
			defer cleanup()

			runComposite(t, ctx,
				"CREATE TABLE num_ios (a "+c.colType+")",
				"CREATE INDEX num_ios_idx ON num_ios (a)",
				"INSERT INTO num_ios VALUES ('"+c.insert+"')",
				"INSERT INTO num_ios VALUES ('"+c.probe+"')",
			)
			vacuumThen(t, ctx, "num_ios")

			q := "SELECT a FROM num_ios WHERE a = '" + c.probe + "'"
			if ios := findIndexOnlyScan(planOne(t, q, ctx.Catalog)); ios == nil {
				t.Skip("planner did not promote to IndexOnlyScan; the fast path is not reachable")
			}
			rows := runQuery(t, ctx, q)
			if len(rows) != 1 {
				t.Fatalf("rows=%d want 1 (%v)", len(rows), rows)
			}
			if got := rows[0][0].Format(); got != c.want {
				t.Errorf("row=%q want %q (the heap's spelling)", got, c.want)
			}
		})
	}
}

// TestNumericUniqueCollapsesDisplayScale is the other half of the trade, held
// down so a later attempt to "fix" EncodeNumericKey by appending the display
// scale fails HERE instead of silently admitting a duplicate. PG compares
// numerics with numeric_cmp, which ignores display scale, so `1.00` after `1.0`
// is a duplicate key; goopg gets that from byte-identical keys, and appending
// scale bytes — trailing or not — would make the two keys differ.
func TestNumericUniqueCollapsesDisplayScale(t *testing.T) {
	ctx, cleanup := newVMFixture(t)
	defer cleanup()

	runComposite(t, ctx,
		"CREATE TABLE num_uq (a numeric UNIQUE)",
		"INSERT INTO num_uq VALUES ('1.0')",
	)
	_, err := runQueryErr(t, ctx, "INSERT INTO num_uq VALUES ('1.00')")
	if err == nil {
		t.Fatal("INSERT 1.00 after 1.0 succeeded — UNIQUE on numeric no longer " +
			"collapses display scale; PG raises 23505")
	}
	if !strings.Contains(err.Error(), "23505") {
		t.Errorf("err=%v, want a 23505 duplicate-key error", err)
	}
}

// TestArrayIndexOnlyScanAnswersFromKey is the payoff side of the same predicate
// (27th slice): for the five element types whose heap image is now upstream's
// own (date/time/timestamp/timestamptz/bytea — 26th slice), the scan is allowed
// back onto the ALL_VISIBLE fast path and must answer the query FROM THE KEY,
// with the text PG prints. Before this slice each of these declined the fast
// path and paid a heap fetch for every row.
//
// The `want` strings are PG 18.3's array_out spellings (timestamp/timestamptz
// contain a space and are therefore quoted; bytea's `\x` form is quoted because
// of the backslash) — the point of the test is that the KEY path produces them,
// not merely that some path does.
func TestArrayIndexOnlyScanAnswersFromKey(t *testing.T) {
	cases := []struct {
		name, colType, insert, probe, want string
	}{
		{"date", "date[]", "{2020-01-02}", "{2021-03-04}", "{2021-03-04}"},
		{"time", "time[]", "{01:02:03}", "{04:05:06}", "{04:05:06}"},
		{"timestamp", "timestamp[]", "{2020-01-02 03:04:05}", "{2021-03-04 05:06:07}",
			`{"2021-03-04 05:06:07"}`},
		{"timestamptz", "timestamptz[]", "{2020-01-02 03:04:05+00}", "{2021-03-04 05:06:07+00}",
			`{"2021-03-04 05:06:07+00"}`},
		// array_out escapes the backslash inside the quoted element, so PG prints
		// the two-character run `\\` before the hex: {"\\x0304"}.
		{"bytea", "bytea[]", `{\x0102}`, `{\x0304}`, `{"\\x0304"}`},
		// reg* family: the element's key is the 8-byte unsigned OID, and the
		// NAME literal is resolved to that OID on encode through the SAME
		// regIdentifierInput the heap element path uses. The `want` is the name
		// array_out prints — pg_catalog relations render unqualified, regtype
		// prints the SQL alias (int8 → bigint), regproc the quoted routine name,
		// regprocedure the name with its arg list, regrole/regcollation the
		// quoted role/collation name. M0119-0006-0006.
		{"regclass", "regclass[]", "{pg_class}", "{arr_iosk}", "{arr_iosk}"},
		{"regtype", "regtype[]", "{int4}", "{int8}", "{bigint}"},
		{"regproc", "regproc[]", "{age}", "{int4eq}", "{int4eq}"},
		{"regprocedure", "regprocedure[]", "{age(timestamptz)}", "{int4recv(internal)}", "{int4recv(internal)}"},
		{"regrole", "regrole[]", "{pg_monitor}", "{pg_signal_backend}", "{pg_signal_backend}"},
		{"regcollation", "regcollation[]", "{default}", "{ucs_basic}", "{ucs_basic}"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx, cleanup := newVMFixture(t)
			defer cleanup()

			runComposite(t, ctx,
				"CREATE TABLE arr_iosk (a "+c.colType+")",
				"CREATE INDEX arr_iosk_idx ON arr_iosk (a)",
				"INSERT INTO arr_iosk VALUES ('"+c.insert+"')",
				"INSERT INTO arr_iosk VALUES ('"+c.probe+"')",
			)
			vacuumThen(t, ctx, "arr_iosk")

			q := "SELECT a FROM arr_iosk WHERE a = '" + c.probe + "'"
			if ios := findIndexOnlyScan(planOne(t, q, ctx.Catalog)); ios == nil {
				t.Fatalf("%s[] was NOT promoted to IndexOnlyScan — the re-armed fast path is unreachable", c.name)
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
