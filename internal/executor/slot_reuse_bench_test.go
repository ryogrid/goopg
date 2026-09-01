package executor

import (
	"fmt"
	"strings"
	"testing"
)

// BenchmarkAggregateEmit measures the aggregate operator's emission path
// (review/260831 EO2-24): every row used to be handed out in a freshly
// allocated MaterializedSlot; the operator reuses its own slot now, the way
// indexScanOp has since M0092-0007. The query produces many groups so the
// emission loop, not the aggregation, dominates.
func BenchmarkAggregateEmit(b *testing.B) {
	ctx, cleanup := newVMFixture(b)
	defer cleanup()

	run := func(sql string) { benchExecSQL(b, ctx, sql) }
	run("CREATE TABLE aggemit (g int, v int)")
	const n = 4000
	var vals strings.Builder
	for i := 0; i < n; i++ {
		if i > 0 {
			vals.WriteString(",")
		}
		fmt.Fprintf(&vals, "(%d, %d)", i%1000, i)
	}
	run("INSERT INTO aggemit VALUES " + vals.String())

	b.ReportAllocs()
	for b.Loop() {
		run("SELECT g, count(*) FROM aggemit GROUP BY g")
	}
}
