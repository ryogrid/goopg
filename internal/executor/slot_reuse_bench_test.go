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

// BenchmarkGenerateSeries measures the SRF's emission path (review/260831
// EO1-10): every value used to allocate a one-column Row and a
// MaterializedSlot; both are reused now.
func BenchmarkGenerateSeries(b *testing.B) {
	ctx, cleanup := newVMFixture(b)
	defer cleanup()

	b.ReportAllocs()
	for b.Loop() {
		benchExecSQL(b, ctx, "SELECT count(*) FROM generate_series(1, 20000)")
	}
}

// BenchmarkMergeOnCondition measures MERGE's ON evaluation (review/260831
// EO2-12): the concatenated (target, source) row is built once per pair, and
// used to be allocated per pair.
func BenchmarkMergeOnCondition(b *testing.B) {
	ctx, cleanup := newVMFixture(b)
	defer cleanup()
	run := func(sql string) { benchExecSQL(b, ctx, sql) }

	run("CREATE TABLE mtgt (id int, v int)")
	run("CREATE TABLE msrc (id int, v int)")
	var tgt, src strings.Builder
	for i := 0; i < 400; i++ {
		if i > 0 {
			tgt.WriteString(",")
		}
		fmt.Fprintf(&tgt, "(%d, %d)", i, i)
	}
	for i := 0; i < 100; i++ {
		if i > 0 {
			src.WriteString(",")
		}
		fmt.Fprintf(&src, "(%d, %d)", i*3, i)
	}
	run("INSERT INTO mtgt VALUES " + tgt.String())
	run("INSERT INTO msrc VALUES " + src.String())

	b.ReportAllocs()
	for b.Loop() {
		run("MERGE INTO mtgt t USING msrc s ON t.id = s.id " +
			"WHEN MATCHED THEN UPDATE SET v = s.v")
	}
}

// BenchmarkGeneratedColumnInsert measures writing rows into a table with a
// generated column (review/260831 EO2-8): the stored expression text used to be
// re-parsed for every row.
func BenchmarkGeneratedColumnInsert(b *testing.B) {
	ctx, cleanup := newVMFixture(b)
	defer cleanup()
	run := func(sql string) { benchExecSQL(b, ctx, sql) }

	run("CREATE TABLE genbench (id int, v int, doubled int GENERATED ALWAYS AS (v * 2 + 1) STORED)")
	const n = 200
	var vals strings.Builder
	for i := 0; i < n; i++ {
		if i > 0 {
			vals.WriteString(",")
		}
		fmt.Fprintf(&vals, "(%d, %d)", i, i)
	}
	stmt := "INSERT INTO genbench (id, v) VALUES " + vals.String()

	b.ReportAllocs()
	for b.Loop() {
		run(stmt)
	}
}
