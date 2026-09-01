package executor

import (
	"fmt"
	"strings"
	"testing"
)

// BenchmarkWindowPartitionSort measures windowOp.Open's input sort
// (review/260831 EO2-2). The comparator used to evaluate every PARTITION BY /
// ORDER BY expression on both sides of every comparison, so the expressions
// were evaluated O(n log n) times instead of once per row; the keys are now
// precomputed and a permutation is sorted over them. A regression shows up as
// a slowdown that grows with the row count.
func BenchmarkWindowPartitionSort(b *testing.B) {
	ctx, cleanup := newVMFixture(b)
	defer cleanup()

	run := func(sql string) { benchExecSQL(b, ctx, sql) }

	run("CREATE TABLE wsort (g int, t text, v int)")
	const n = 4000
	var vals strings.Builder
	for i := 0; i < n; i++ {
		if i > 0 {
			vals.WriteString(",")
		}
		fmt.Fprintf(&vals, "(%d, 'row%06d', %d)", i%8, (i*7919)%n, i)
	}
	run("INSERT INTO wsort VALUES " + vals.String())

	b.ReportAllocs()
	for b.Loop() {
		run("SELECT row_number() OVER (PARTITION BY g ORDER BY t) FROM wsort")
	}
}
