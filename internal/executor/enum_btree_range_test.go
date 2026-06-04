package executor

import (
	"fmt"
	"testing"
)

// TestEnumBTreeRangeScan verifies that btree range scans on enum columns
// return the correct rows via IndexOnlyScan. M0097-0022.
func TestEnumBTreeRangeScan(t *testing.T) {
	ctx, cleanup := newVMFixture(t)
	defer cleanup()

	// Create enum type and table.
	for _, stmt := range []string{
		`CREATE TYPE rainbow AS ENUM ('red', 'orange', 'yellow', 'green', 'blue', 'purple')`,
		`CREATE TABLE enumtest (col rainbow)`,
		`INSERT INTO enumtest VALUES ('red')`,
		`INSERT INTO enumtest VALUES ('orange')`,
		`INSERT INTO enumtest VALUES ('yellow')`,
		`INSERT INTO enumtest VALUES ('green')`,
		`INSERT INTO enumtest VALUES ('blue')`,
		`INSERT INTO enumtest VALUES ('purple')`,
		`CREATE UNIQUE INDEX enumtest_btree ON enumtest USING btree (col)`,
	} {
		if err := runDDL(t, ctx, stmt); err != nil {
			t.Fatalf("setup %q: %v", stmt, err)
		}
	}
	commitTx(t, ctx)
	beginTx(t, ctx)

	tests := []struct {
		sql      string
		expected []string
	}{
		{`SELECT col FROM enumtest WHERE col = 'orange'`, []string{"orange"}},
		{`SELECT col FROM enumtest WHERE col > 'yellow' ORDER BY col`, []string{"green", "blue", "purple"}},
		{`SELECT col FROM enumtest WHERE col >= 'yellow' ORDER BY col`, []string{"yellow", "green", "blue", "purple"}},
		{`SELECT col FROM enumtest WHERE col < 'green' ORDER BY col`, []string{"red", "orange", "yellow"}},
		{`SELECT col FROM enumtest WHERE col <= 'green' ORDER BY col`, []string{"red", "orange", "yellow", "green"}},
	}

	for _, tt := range tests {
		t.Run(tt.sql, func(t *testing.T) {
			rows := runQuery(t, ctx, tt.sql)
			var got []string
			for _, row := range rows {
				got = append(got, row[0].StringValue())
			}
			if fmt.Sprint(got) != fmt.Sprint(tt.expected) {
				t.Errorf("got %v want %v", got, tt.expected)
			}
		})
	}
}
