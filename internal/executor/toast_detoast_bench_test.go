package executor

import (
	"fmt"
	"strings"
	"testing"
)

// BenchmarkDetoastValue measures reading one out-of-line value back
// (review/260831 ES-17). goopg's TOAST relation has no chunk index, so the
// chunks are found by scanning it; the scan now stops once the value has been
// reassembled, which is what separates the two sub-benchmarks: the first row's
// chunks sit at the start of the relation, the last row's at the end.
func BenchmarkDetoastValue(b *testing.B) {
	ctx, cleanup := newVMFixture(b)
	defer cleanup()
	run := func(sql string) { benchExecSQL(b, ctx, sql) }

	run("CREATE TABLE toastbench (id int, t text)")
	const nrows = 40
	blob := strings.Repeat("abcdefghij", 800) // 8000 bytes: forced out of line
	for i := 0; i < nrows; i++ {
		run(fmt.Sprintf("INSERT INTO toastbench VALUES (%d, '%s')", i, blob))
	}

	b.Run("first-row", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			run("SELECT length(t) FROM toastbench WHERE id = 0")
		}
	})
	b.Run("last-row", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			run(fmt.Sprintf("SELECT length(t) FROM toastbench WHERE id = %d", nrows-1))
		}
	})
}
