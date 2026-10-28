package executor

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// drainIteratorRows runs plan through BuildFastIterator, the dispatch path
// that publishes the index-scan read sets (OpIterator.Open).
func drainIteratorRows(t *testing.T, ctx *Context, plan optimizer.Node) (rows []string) {
	t.Helper()
	it, err := BuildFastIterator(plan)
	if err != nil {
		t.Fatalf("BuildFastIterator: %v", err)
	}
	if err := it.Open(ctx); err != nil {
		t.Fatalf("Open: %v", err)
	}
	for {
		s, err := it.Next()
		if errors.Is(err, EOF) {
			break
		}
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		if s == nil {
			break
		}
		rows = append(rows, fmt.Sprint(s.Row()))
	}
	if err := it.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	return rows
}

// TestIndexReadSetsMatchFullDecode pins M0122-0012 C34: an index scan whose
// heap fetch walks past the columns no node above it reads returns rows that
// give exactly the results — order included — the full decode gives. Each
// query runs three ways: read sets on, read sets off, and on with the skipped
// cells poisoned (seqScanDeformPoison), where any read of a skipped column
// panics at the ColumnRef. The shapes are the ones the plan walk models:
// lateral index nested loops (the outer row read through OuterColumnRef),
// a projection and a sort key over an otherwise unread inner column, a
// residual on the inner, an aggregate, INTERSECT, a CTE read through IN, an
// uncorrelated scalar sublink in the probe's filter, LIMIT, a left join, a
// Gather; and the shapes that must decline (ctid, a whole-row reference).
func TestIndexReadSetsMatchFullDecode(t *testing.T) {
	applied := 0
	oldHook := indexReadSetApplied
	indexReadSetApplied = func() { applied++ }
	t.Cleanup(func() { indexReadSetApplied = oldHook })
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	runSQL(t, ctx, "CREATE TABLE irs_dim (id int PRIMARY KEY, code char(16), d1 date, note varchar(200), price numeric(7,2), brand int, bname char(50), cls int, cname char(50), cat int, tail text)")
	runSQL(t, ctx, "INSERT INTO irs_dim SELECT x, 'c' || x, DATE '2000-01-01' + x, repeat('n', x % 40), x * 0.5, x % 7, 'b' || (x % 7), x % 5, 'k' || (x % 5), x % 3, CASE WHEN x % 50 = 0 THEN NULL ELSE 't' || x END FROM generate_series(1, 3000) x")
	runSQL(t, ctx, "CREATE TABLE irs_fact (did int, fid int, qty int, amt numeric(9,2), memo text, PRIMARY KEY (did, fid))")
	runSQL(t, ctx, "INSERT INTO irs_fact SELECT x % 3000 + 1, x, x % 17, x * 0.25, 'm' || x FROM generate_series(1, 20000) x")
	runSQL(t, ctx, "CREATE TABLE irs_drv (did int, w int)")
	runSQL(t, ctx, "INSERT INTO irs_drv SELECT (x * 37) % 3000 + 1, x FROM generate_series(1, 600) x")
	runSQL(t, ctx, "ANALYZE irs_dim")
	runSQL(t, ctx, "ANALYZE irs_fact")
	runSQL(t, ctx, "ANALYZE irs_drv")
	ps := optimizer.DefaultPlannerSettings()
	ps.EnableHashJoin = false
	ps.EnableMergeJoin = false
	ps.EnableBitmapScan = false
	ps.MaxParallelWorkersPerGather = 0
	par := ps
	par.MaxParallelWorkersPerGather = 2
	type tc struct {
		sql     string
		ps      optimizer.PlannerSettings
		mayDecl bool // the plan may legitimately publish no skipping set
	}
	queries := []tc{
		{"SELECT d.brand, d.cls, d.cat, count(*) FROM irs_drv v JOIN irs_dim d ON d.id = v.did GROUP BY 1, 2, 3 ORDER BY 1, 2, 3", ps, false},
		{"SELECT v.w, d.price, d.bname FROM irs_drv v JOIN irs_dim d ON d.id = v.did ORDER BY v.w", ps, false},
		{"SELECT v.w, d.cat FROM irs_drv v JOIN irs_dim d ON d.id = v.did ORDER BY d.note, v.w LIMIT 50", ps, false},
		{"SELECT v.w FROM irs_drv v JOIN irs_dim d ON d.id = v.did AND d.price > v.w * 0.1 ORDER BY v.w", ps, false},
		{"SELECT d.cat, sum(f.qty), sum(f.amt) FROM irs_drv v JOIN irs_dim d ON d.id = v.did JOIN irs_fact f ON f.did = d.id GROUP BY d.cat ORDER BY d.cat", ps, false},
		{"SELECT d.brand, d.cls FROM irs_drv v JOIN irs_dim d ON d.id = v.did INTERSECT SELECT d.brand, d.cls FROM irs_fact f JOIN irs_dim d ON d.id = f.did WHERE f.qty = 3 ORDER BY 1, 2", ps, false},
		{"WITH c AS MATERIALIZED (SELECT d.id FROM irs_drv v JOIN irs_dim d ON d.id = v.did WHERE d.cat = 1) SELECT count(*), sum(f.amt) FROM irs_fact f WHERE f.did IN (SELECT id FROM c) AND f.did IN (SELECT id + 0 FROM c)", ps, false},
		{"SELECT v.w, d.cname FROM irs_drv v JOIN irs_dim d ON d.id = v.did AND d.cls = (SELECT max(cls) FROM irs_dim WHERE cat = 2) ORDER BY v.w", ps, false},
		{"SELECT v.w, d.tail FROM irs_drv v LEFT JOIN irs_dim d ON d.id = v.did AND d.cat = 0 ORDER BY v.w", ps, false},
		{"SELECT v.w, upper(d.cname), d.price * 2 FROM irs_drv v JOIN irs_dim d ON d.id = v.did WHERE v.w % 7 = 0 ORDER BY v.w LIMIT 30", ps, false},
		{"SELECT d.cat, count(*), sum(d.price) FROM irs_drv v JOIN irs_dim d ON d.id = v.did GROUP BY d.cat ORDER BY d.cat", par, true},
		{"SELECT v.w, d.ctid IS NOT NULL, d.cat FROM irs_drv v JOIN irs_dim d ON d.id = v.did ORDER BY v.w", ps, true},
		{"SELECT v.w, d FROM irs_drv v JOIN irs_dim d ON d.id = v.did ORDER BY v.w LIMIT 40", ps, true},
	}
	oldPoison := seqScanDeformPoison
	t.Cleanup(func() { seqScanDeformPoison = oldPoison; indexReadSetsDisabled = false })
	for _, q := range queries {
		applied = 0
		got := drainIteratorRows(t, ctx, planWithSettings(t, ctx, q.sql, q.ps))
		n := applied
		indexReadSetsDisabled = true
		want := drainIteratorRows(t, ctx, planWithSettings(t, ctx, q.sql, q.ps))
		indexReadSetsDisabled = false
		seqScanDeformPoison = true
		poisoned := drainIteratorRows(t, ctx, planWithSettings(t, ctx, q.sql, q.ps))
		seqScanDeformPoison = oldPoison
		if strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Fatalf("%s (applied %d): read sets differ from full decode\nsets %v\nfull %v", q.sql, n, got, want)
		}
		if strings.Join(poisoned, "\n") != strings.Join(want, "\n") {
			t.Fatalf("%s: poisoned run differs from full decode\npoison %v\nfull   %v", q.sql, poisoned, want)
		}
		if len(got) == 0 {
			t.Fatalf("%s: no rows", q.sql)
		}
		if n == 0 && !q.mayDecl {
			t.Errorf("%s: no index scan skipped a column", q.sql)
		}
		t.Logf("applied=%d rows=%d %s", n, len(got), q.sql)
	}
}
