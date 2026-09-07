package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/storage/lmgr"
)

// E-17 / EX3-08 cut 2 tests. The scan is now the SOLE evaluator of a qual that
// sat on a Filter directly above it, so every responsibility the deleted
// filterOp carried has to be pinned here.

// TestScanAbsorbsQualAndDropsFilterOp pins the shape: no filterOp above a
// SeqScan whose qual was absorbed, the qual on the scan, and — for a qual the
// prefix cannot judge — the LATE position armed instead of the early one.
func TestScanAbsorbsQualAndDropsFilterOp(t *testing.T) {
	ctx, cat, cleanup := newStorageFixture(t)
	defer cleanup()
	tbl, _ := cat.LookupTable(parser.ObjectName{Name: "items"})
	seedItems(t, ctx, tbl)

	// items is (id, label) — two columns. A qual on id reads column 0 only,
	// so the prefix [0,1) can judge it: EARLY. A qual on label reads column
	// 1, i.e. the whole row, so nothing is saved by a two-phase deform and
	// the qual is evaluated LATE — the position filterOp used to occupy.
	cases := []struct {
		name      string
		sql       string
		wantEarly bool
	}{
		{"qual on leading column is early", "SELECT id FROM items WHERE id = 2", true},
		{"qual on last column is late", "SELECT id FROM items WHERE label = 'beta'", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stmts, err := parser.Parse(tc.sql)
			if err != nil {
				t.Fatal(err)
			}
			node, err := optimizer.Plan(stmts[0], ctx.Catalog)
			if err != nil {
				t.Fatal(err)
			}
			op, err := Build(node)
			if err != nil {
				t.Fatal(err)
			}
			// No filterOp anywhere in the tree: the scan absorbed the qual.
			if findFilterOpInTree(op) {
				t.Error("a filterOp survives above the scan — the qual is " +
					"evaluated TWICE, which is what cut 2 removes")
			}
			so := findSeqScanOpInTree(op)
			if so == nil {
				t.Fatal("no seqScanOp in the tree")
			}
			if !so.qualSet || so.qual == nil {
				t.Fatal("scan did not absorb the qual — with no filterOp above " +
					"it, the predicate would not be evaluated at all")
			}
			if so.prefilterSet != tc.wantEarly {
				t.Errorf("prefilterSet=%v, want %v (early vs late position)",
					so.prefilterSet, tc.wantEarly)
			}
		})
	}
}

// TestScanAbsorbedQualValuesMatchFilterOp is the values pin: for the same
// query, the absorbed-qual path must return exactly what the pre-E-17
// filterOp path returned. GOOPG_SCAN_PREFILTER=off restores the old seam
// (scan deforms everything, qual evaluated at the LATE position only), so the
// two positions are compared against each other on the same data.
//
// This is the test that would catch the failure mode row counts cannot see:
// a qual evaluated against a partially-deformed row reading a stale cell.
func TestScanAbsorbedQualValuesMatchFilterOp(t *testing.T) {
	quals := []string{
		"id = 2",
		"id > 1",
		"label = 'beta'",
		"id > 1 AND label = 'gamma'",
		"id < 3 AND label <> 'alpha'",
		"label IS NOT NULL",
	}
	for _, q := range quals {
		t.Run(q, func(t *testing.T) {
			var got [2][]Row
			for i, early := range []bool{true, false} {
				SetScanPrefilterEnabled(early)
				func() {
					defer SetScanPrefilterEnabled(true)
					ctx, cat, cleanup := newStorageFixture(t)
					defer cleanup()
					tbl, _ := cat.LookupTable(parser.ObjectName{Name: "items"})
					seedItems(t, ctx, tbl)
					rows, err := runForUpdate(t, ctx, "SELECT id, label FROM items WHERE "+q)
					if err != nil {
						t.Fatalf("early=%v: %v", early, err)
					}
					got[i] = rows
				}()
			}
			if len(got[0]) != len(got[1]) {
				t.Fatalf("row COUNT differs: early=%d late=%d", len(got[0]), len(got[1]))
			}
			for i := range got[0] {
				a, b := got[0][i], got[1][i]
				if len(a) != len(b) {
					t.Fatalf("row %d width differs: %d vs %d", i, len(a), len(b))
				}
				for j := range a {
					// Values, in order — not just counts. A dropped or
					// reordered row is the failure mode when qual evaluation
					// moves, and a count check is blind to it.
					if a[j].Format() != b[j].Format() {
						t.Errorf("row %d col %d: early=%q late=%q",
							i, j, a[j].Format(), b[j].Format())
					}
				}
			}
		})
	}
}

// TestFindFilterPredRecoversAbsorbedQual is the EPQ-recheck pin at unit
// grain. lockRowsOp recovers the scan-level qual through findFilterPred and
// re-applies it to the latest row version during EvalPlanQual. Before cut 2
// the qual was always on a filterOp; now it may be on the scan, and a
// findFilterPred that misses it returns nil — which makes epqRecheckFilter a
// silent NO-OP and yields WRONG ROWS for SELECT ... FOR UPDATE against a
// concurrently updated row. That is a wrong answer, not a lost optimisation,
// which is why it is pinned separately from any values suite.
func TestFindFilterPredRecoversAbsorbedQual(t *testing.T) {
	ctx, cat, cleanup := newStorageFixture(t)
	defer cleanup()
	tbl, _ := cat.LookupTable(parser.ObjectName{Name: "items"})
	seedItems(t, ctx, tbl)

	for _, sql := range []string{
		"SELECT id FROM items WHERE id = 2 FOR UPDATE",
		"SELECT id FROM items WHERE label = 'beta' FOR UPDATE",
	} {
		t.Run(sql, func(t *testing.T) {
			stmts, err := parser.Parse(sql)
			if err != nil {
				t.Fatal(err)
			}
			node, err := optimizer.Plan(stmts[0], ctx.Catalog)
			if err != nil {
				t.Fatal(err)
			}
			op, err := Build(node)
			if err != nil {
				t.Fatal(err)
			}
			lr := findLockRowsOpInTree(op)
			if lr == nil {
				t.Fatal("no lockRowsOp in the tree")
			}
			if pred := findFilterPred(lr.child); pred == nil {
				t.Fatal("findFilterPred returned nil for an absorbed qual: " +
					"EPQ recheck would silently skip the WHERE clause and " +
					"return a row the predicate no longer matches")
			}
		})
	}
}

// TestEPQRecheckAppliesAbsorbedQual is the end-to-end half of the above. A
// concurrent, committed UPDATE moves the row OUT of the predicate while the
// FOR UPDATE waits on it; when the wait ends the locker must re-test the
// LATEST version and skip it. With the qual absorbed into the scan and
// findFilterPred not taught about it, this returns the stale row instead.
func TestEPQRecheckAppliesAbsorbedQual(t *testing.T) {
	ctx, cat, cleanup := newStorageFixture(t)
	defer cleanup()
	tbl, _ := cat.LookupTable(parser.ObjectName{Name: "items"})
	seedItems(t, ctx, tbl)
	if err := ctx.TxnMgr.Commit(ctx.Tx); err != nil {
		t.Fatal(err)
	}
	lm := lmgr.New()

	// Session 2 updates the row so it no longer satisfies `label = 'beta'`,
	// and COMMITS.
	s2tx, err := ctx.TxnMgr.Begin(0)
	if err != nil {
		t.Fatal(err)
	}
	s2snap, err := ctx.TxnMgr.SnapshotFor(s2tx)
	if err != nil {
		t.Fatal(err)
	}
	s2 := makeCtx(lm, 2)
	s2.Pool, s2.Catalog, s2.TxnMgr, s2.Tx, s2.Snap = ctx.Pool, ctx.Catalog, ctx.TxnMgr, s2tx, s2snap
	if _, err := runForUpdate(t, s2, "UPDATE items SET label = 'moved' WHERE id = 2"); err != nil {
		t.Fatalf("session-2 UPDATE: %v", err)
	}
	if err := ctx.TxnMgr.Commit(s2tx); err != nil {
		t.Fatal(err)
	}

	// Session 1 starts AFTER the commit but re-reads under its own snapshot;
	// the FOR UPDATE path re-fetches the latest version and must apply the
	// absorbed qual to it.
	s1tx, err := ctx.TxnMgr.Begin(0)
	if err != nil {
		t.Fatal(err)
	}
	s1snap, err := ctx.TxnMgr.SnapshotFor(s1tx)
	if err != nil {
		t.Fatal(err)
	}
	s1 := makeCtx(lm, 1)
	s1.Pool, s1.Catalog, s1.TxnMgr, s1.Tx, s1.Snap = ctx.Pool, ctx.Catalog, ctx.TxnMgr, s1tx, s1snap
	defer ctx.TxnMgr.Rollback(s1tx)

	rows, err := runForUpdate(t, s1, "SELECT id, label FROM items WHERE label = 'beta' FOR UPDATE")
	if err != nil {
		t.Fatalf("session-1 FOR UPDATE: %v", err)
	}
	for _, r := range rows {
		if len(r) > 1 && r[1].Format() != "beta" {
			t.Fatalf("FOR UPDATE returned a row the qual does not match: %q — "+
				"the absorbed qual was not re-applied on EPQ recheck", r[1].Format())
		}
	}
}

// --- tree walkers, test-local -------------------------------------------

func findFilterOpInTree(op Operator) bool {
	switch v := op.(type) {
	case *filterOp:
		return true
	case *instrumentedOp:
		return findFilterOpInTree(v.inner)
	case *projectOp:
		return findFilterOpInTree(v.child)
	case *lockRowsOp:
		return findFilterOpInTree(v.child)
	}
	return false
}

func findSeqScanOpInTree(op Operator) *seqScanOp {
	switch v := op.(type) {
	case *seqScanOp:
		return v
	case *instrumentedOp:
		return findSeqScanOpInTree(v.inner)
	case *projectOp:
		return findSeqScanOpInTree(v.child)
	case *filterOp:
		return findSeqScanOpInTree(v.child)
	case *lockRowsOp:
		return findSeqScanOpInTree(v.child)
	}
	return nil
}

func findLockRowsOpInTree(op Operator) *lockRowsOp {
	switch v := op.(type) {
	case *lockRowsOp:
		return v
	case *instrumentedOp:
		return findLockRowsOpInTree(v.inner)
	case *projectOp:
		return findLockRowsOpInTree(v.child)
	}
	return nil
}

// TestScanAbsorbedQualErrorPosition is the error-position pin the row
// demands, one per absorbed abstain path. The qual below is early-eligible
// (pure integer arithmetic over the leading column) and divides by zero on
// the second row, AFTER the first row took an early keep verdict:
//
//   - with the prefilter ON the error fires at the EARLY position, Next
//     promotes the row to the LATE position, and the error surfaces from
//     exactly where the deleted filterOp raised it;
//   - with the prefilter OFF the qual runs late from the start.
//
// Both arms must fail identically (SQLSTATE + message): error position,
// message and ordering are byte-identical to a build with the prefilter
// disabled, which is the contract the old "fall through and let filterOp
// raise it" comment kept. The needsDetoastPrefix abstain path needs no
// error case of its own — its guard is preserved verbatim from the
// pre-cut-2 code and only the destination changed (LATE instead of
// filterOp) — but it shares this test's late position, so a regression
// that moved the late evaluation would fail here too.
func TestScanAbsorbedQualErrorPosition(t *testing.T) {
	// Non-vacuity: the qual below must actually take the EARLY position
	// when enabled, or the early arm proves nothing about promotion.
	SetScanPrefilterEnabled(true)
	func() {
		ctx, cat, cleanup := newStorageFixture(t)
		defer cleanup()
		tbl, _ := cat.LookupTable(parser.ObjectName{Name: "items"})
		seedItems(t, ctx, tbl)
		stmts, err := parser.Parse("SELECT id FROM items WHERE 10/(id-2) > -20")
		if err != nil {
			t.Fatal(err)
		}
		node, err := optimizer.Plan(stmts[0], ctx.Catalog)
		if err != nil {
			t.Fatal(err)
		}
		op, err := Build(node)
		if err != nil {
			t.Fatal(err)
		}
		so := findSeqScanOpInTree(op)
		if so == nil {
			t.Fatal("no seqScanOp in the tree")
		}
		if !so.prefilterSet {
			t.Fatal("qual not early-eligible: the early arm below would run late and prove nothing")
		}
	}()
	for _, early := range []bool{true, false} {
		t.Run(map[bool]string{true: "early", false: "late"}[early], func(t *testing.T) {
			SetScanPrefilterEnabled(early)
			defer SetScanPrefilterEnabled(true)
			ctx, cat, cleanup := newStorageFixture(t)
			defer cleanup()
			tbl, _ := cat.LookupTable(parser.ObjectName{Name: "items"})
			seedItems(t, ctx, tbl)
			// id=1: 10/(1-2) = -10 > -20 TRUE (early keep verdict);
			// id=2: division by zero.
			_, err := runForUpdate(t, ctx, "SELECT id FROM items WHERE 10/(id-2) > -20")
			if err == nil {
				t.Fatalf("early=%v: expected division-by-zero, got no error", early)
			}
			ee, ok := err.(*ExecError)
			if !ok {
				t.Fatalf("early=%v: error type %T, want *ExecError", early, err)
			}
			if ee.Code != "22012" {
				t.Fatalf("early=%v: SQLSTATE %q, want 22012", early, ee.Code)
			}
		})
	}
}
