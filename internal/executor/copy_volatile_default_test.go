package executor

import (
	"strings"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

// TestCopyFromEvaluatesDefaultsPerRow pins M0146-0054 against PG 18.3. COPY
// FROM filled an omitted column through applyDefaultsForMissing's
// lightweight evaluator, which knew a handful of functions and returned NULL
// for anything else: `DEFAULT random()` and `DEFAULT clock_timestamp()`
// stored NULL in every row, where PG's BeginCopyFrom prepares each omitted
// column's default and evaluates it per row (copyfrom.c defexprs). The
// defaults now run through the full evaluator, so a volatile default gets a
// fresh value per row, and an evaluation error fails the COPY rather than
// storing NULL. The non-volatile expected values are PG's output for the
// same table.
func TestCopyFromEvaluatesDefaultsPerRow(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	runSQL(t, ctx, `CREATE TABLE copy_vd (a int, b text, c float8 DEFAULT random(),
		d timestamptz DEFAULT clock_timestamp(), e numeric DEFAULT 2.5,
		h text DEFAULT 'x' || 'y', i int DEFAULT (1+2)*3,
		k text NOT NULL DEFAULT upper('q'))`)
	tbl, ok := cat.LookupTable(parser.ObjectName{Name: "copy_vd"})
	if !ok {
		t.Fatal("copy_vd not found")
	}
	cf, err := NewCopyFromExecutor(ctx, &optimizer.Copy{
		Direction: optimizer.CopyFrom, Table: tbl, ColumnIndex: []int{0, 1},
		Endpoint: optimizer.CopyEndpointStdin,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{"1\tone", "2\ttwo", "3\tthree"} {
		if err := cf.PushLine([]byte(line)); err != nil {
			t.Fatalf("PushLine(%q): %v", line, err)
		}
		// clock_timestamp() has microsecond resolution; keep the rows apart
		// so each row's value is distinct by construction.
		time.Sleep(time.Millisecond)
	}
	if err := cf.Finish(); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ sql, want string }{
		// Volatile: a value per row, every row distinct (PG: 3|3|3|3).
		{"SELECT count(c), count(DISTINCT c), count(d), count(DISTINCT d) FROM copy_vd", "3|3|3|3"},
		{"SELECT count(*) FROM copy_vd WHERE c >= 0 AND c < 1", "3"},
		{"SELECT a, e, h, i, k FROM copy_vd ORDER BY a", "1|2.5|xy|9|Q;2|2.5|xy|9|Q;3|2.5|xy|9|Q"},
	} {
		if got := strings.Join(renderRows(runSQL(t, ctx, c.sql)), ";"); got != c.want {
			t.Errorf("%s\ngot  %s\nwant %s", c.sql, got, c.want)
		}
	}

	// A default that fails to evaluate fails the COPY (PG: ERROR division
	// by zero) — never a silent NULL.
	runSQL(t, ctx, `CREATE TABLE copy_vd_err (a int, b int DEFAULT 1/0)`)
	etbl, _ := cat.LookupTable(parser.ObjectName{Name: "copy_vd_err"})
	cf2, err := NewCopyFromExecutor(ctx, &optimizer.Copy{
		Direction: optimizer.CopyFrom, Table: etbl, ColumnIndex: []int{0},
		Endpoint: optimizer.CopyEndpointStdin,
	})
	if err != nil {
		t.Fatal(err)
	}
	err = cf2.PushLine([]byte("1"))
	if xe, ok := err.(*ExecError); !ok || xe.Code != "22012" {
		t.Fatalf("COPY with DEFAULT 1/0: err=%v, want 22012 division by zero", err)
	}
	if cf2.RowsInserted() != 0 {
		t.Errorf("RowsInserted=%d want 0", cf2.RowsInserted())
	}
}
