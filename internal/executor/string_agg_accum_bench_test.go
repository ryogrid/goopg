package executor

import (
	"fmt"
	"strings"
	"testing"
)

// BenchmarkStringAggUnordered measures the unordered string_agg accumulator
// (review/260831 EO2-1). It used to concatenate with `+=`, recopying the whole
// accumulator per input row (O(n^2) bytes); it now appends into a byte slice.
// The row count is deliberately large enough for the quadratic term to
// dominate — a regression here shows up as a super-linear slowdown.
func BenchmarkStringAggUnordered(b *testing.B) {
	ctx, cleanup := newVMFixture(b)
	defer cleanup()

	run := func(sql string) { benchExecSQL(b, ctx, sql) }

	run("CREATE TABLE sagg (id int, t text)")
	const n = 4000
	var vals strings.Builder
	for i := 0; i < n; i++ {
		if i > 0 {
			vals.WriteString(",")
		}
		fmt.Fprintf(&vals, "(%d, 'abcdefghijklmnopqrstuvwxyz')", i)
	}
	run("INSERT INTO sagg VALUES " + vals.String())

	b.ReportAllocs()
	for b.Loop() {
		run("SELECT string_agg(t, ',') FROM sagg")
	}
}
