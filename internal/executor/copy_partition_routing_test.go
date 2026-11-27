package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

// TestCopyFromRoutesIntoPartitions pins M0149-0059b against PG 18.3: COPY
// into a partitioned table routes every row to the leaf its key selects
// (copyfrom.c ExecFindPartition), including a leaf whose columns are in a
// different order, and a row no partition accepts fails the COPY. Before,
// goopg wrote the rows to the partitioned parent's own relfilenode,
// reported "COPY 3" and a scan of the table returned nothing.
func TestCopyFromRoutesIntoPartitions(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	for _, q := range []string{
		"CREATE TABLE cpr (a int PRIMARY KEY, b int NOT NULL) PARTITION BY RANGE (a)",
		"CREATE TABLE cpr_1 PARTITION OF cpr FOR VALUES FROM (MINVALUE) TO (10)",
		"CREATE TABLE cpr_2 (b int NOT NULL, a int NOT NULL)",
		"ALTER TABLE cpr ATTACH PARTITION cpr_2 FOR VALUES FROM (10) TO (20)",
	} {
		if err := runDDL(t, ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	tbl, ok := cat.LookupTable(parser.ObjectName{Name: "cpr"})
	if !ok {
		t.Fatal("cpr not found")
	}
	copyLines := func(lines ...string) (int64, error) {
		cf, err := NewCopyFromExecutor(ctx, &optimizer.Copy{
			Direction:   optimizer.CopyFrom,
			Table:       tbl,
			ColumnIndex: []int{0, 1},
			Endpoint:    optimizer.CopyEndpointStdin,
		})
		if err != nil {
			return 0, err
		}
		if cf.multiInsert {
			t.Fatal("a partitioned target took the multi-insert buffer")
		}
		for _, l := range lines {
			if err := cf.PushLine([]byte(l)); err != nil {
				return cf.RowsInserted(), err
			}
		}
		return cf.RowsInserted(), cf.Finish()
	}
	n, err := copyLines("1\t100", "2\t200", "15\t150")
	if err != nil || n != 3 {
		t.Fatalf("COPY = %d, %v; want 3 rows", n, err)
	}
	for _, tc := range []struct {
		q    string
		want int64
	}{
		{"SELECT count(*) FROM cpr", 3},
		{"SELECT count(*) FROM cpr_1", 2},
		{"SELECT count(*) FROM cpr_2 WHERE a = 15 AND b = 150", 1},
		{"SELECT count(*) FROM cpr WHERE a = 15 AND b = 150", 1},
	} {
		if rows := runSQL(t, ctx, tc.q); len(rows) != 1 || rows[0][0].Int != tc.want {
			t.Errorf("%s = %v, want %d", tc.q, rows, tc.want)
		}
	}
	if _, err := copyLines("30\t1"); err == nil || !strings.Contains(err.Error(), `no partition of relation "cpr" found for row`) {
		t.Errorf("unroutable row: err = %v, want PG's no-partition error", err)
	}
}
