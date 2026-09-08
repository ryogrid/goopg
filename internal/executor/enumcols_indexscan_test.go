package executor

import (
	"fmt"
	"testing"
)

// TestIndexScanEnumOrderingSurvivesPrecompute pins the behaviour that the
// per-row LookupEnum loop in indexScanOp.Next existed to provide, now that it
// is resolved once in openPrep instead (see resolveEnumColumns).
//
// The property: an enum column must compare and order by DECLARATION order,
// not by label text. Those two orders are deliberately opposite here --
// alphabetically the labels sort 'alpha' < 'mid' < 'zeta', while the
// declaration order is 'zeta' < 'mid' < 'alpha' -- so a scan that forgets to
// inject KindEnum returns the alphabetical answer and this test fails.
//
// MUTATION-CHECKED: with the openPrep resolution disabled (o.enumTypes forced
// nil), the ORDER BY subtest below returns [alpha mid zeta] and fails. That is
// what makes this test non-vacuous; no TPC-H or TPC-DS query exercises enums
// at all, so the corpus gates cannot catch a regression here.
func TestIndexScanEnumOrderingSurvivesPrecompute(t *testing.T) {
	ctx, cleanup := newVMFixture(t)
	defer cleanup()

	for _, stmt := range []string{
		`CREATE TYPE decl_order AS ENUM ('zeta', 'mid', 'alpha')`,
		`CREATE TABLE enumidx (id int, col decl_order)`,
		`INSERT INTO enumidx VALUES (1, 'alpha')`,
		`INSERT INTO enumidx VALUES (2, 'zeta')`,
		`INSERT INTO enumidx VALUES (3, 'mid')`,
		`CREATE INDEX enumidx_col ON enumidx USING btree (col)`,
	} {
		if err := runDDL(t, ctx, stmt); err != nil {
			t.Fatalf("setup %q: %v", stmt, err)
		}
	}
	commitTx(t, ctx)
	beginTx(t, ctx)

	tests := []struct {
		name     string
		sql      string
		expected []string
	}{
		{
			// Declaration order, NOT alphabetical. This is the assertion the
			// whole item rests on.
			name:     "order by follows declaration order",
			sql:      `SELECT col FROM enumidx ORDER BY col`,
			expected: []string{"zeta", "mid", "alpha"},
		},
		{
			// A range predicate resolved through the index: 'mid' and
			// everything after it in DECLARATION order.
			name:     "range predicate uses declaration order",
			sql:      `SELECT col FROM enumidx WHERE col >= 'mid' ORDER BY col`,
			expected: []string{"mid", "alpha"},
		},
		{
			name:     "equality still matches by label",
			sql:      `SELECT col FROM enumidx WHERE col = 'zeta'`,
			expected: []string{"zeta"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rows := runQuery(t, ctx, tt.sql)
			var got []string
			for _, row := range rows {
				got = append(got, row[0].StringValue())
			}
			if fmt.Sprint(got) != fmt.Sprint(tt.expected) {
				t.Errorf("%s: got %v want %v", tt.sql, got, tt.expected)
			}
		})
	}
}
