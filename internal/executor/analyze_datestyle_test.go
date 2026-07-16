package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestAnalyzeMCVHistogramHonorDateStyle pins the next M-NIGHTLY DateStyle
// follow-up slice (resume point recorded after the array_agg/string_agg/
// percentile_disc fix): computeColumnStats (operators_analyze.go) called
// Datum.Format() directly to render both the pg_stats MCV.Value and the
// histogram-bound strings for a DATE/TIMESTAMP column, hardcoding ISO/
// Postgres-MDY regardless of the session's `SET datestyle` in effect when
// ANALYZE ran — diverging from every other already-fixed output path.
// Fixed by routing both call sites through formatDatumDateStyle(d, dsCtx),
// threading the executor Context through analyzeRelationWith down to
// computeColumnStats.
func TestAnalyzeMCVHistogramHonorDateStyle(t *testing.T) {
	ctx, cleanup := newVMFixture(t)
	defer cleanup()
	ctx.GetSetting = func(name string) (string, bool) {
		if name == "datestyle" {
			return "German, DMY", true
		}
		return "", false
	}

	if err := runDDL(t, ctx, "CREATE TABLE danalyze (id int, d date)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	// 5 duplicates of the same date (forms the sole MCV entry) plus 5
	// distinct dates (forms the histogram from the non-MCV remainder) —
	// see computeColumnStats's mcvFreqMargin=1.25 admission rule.
	for i := 0; i < 5; i++ {
		if err := runDDL(t, ctx, "INSERT INTO danalyze VALUES (1, '2026-01-05')"); err != nil {
			t.Fatalf("INSERT dup %d: %v", i, err)
		}
	}
	distinct := []string{"2026-02-01", "2026-02-02", "2026-02-03", "2026-02-04", "2026-02-05"}
	for i, d := range distinct {
		if err := runDDL(t, ctx, "INSERT INTO danalyze VALUES (2, '"+d+"')"); err != nil {
			t.Fatalf("INSERT distinct %d: %v", i, err)
		}
	}

	// ANALYZE runs under a fresh transaction/snapshot (analyzeRelationWith
	// calls mgr.Begin), so the seeding inserts must be committed first —
	// they are otherwise invisible under ReadCommitted.
	commitTx(t, ctx)
	beginTx(t, ctx)

	if err := runDDL(t, ctx, "ANALYZE danalyze"); err != nil {
		t.Fatalf("ANALYZE: %v", err)
	}

	tbl, ok := ctx.Catalog.LookupTable(parser.ObjectName{Name: "danalyze"})
	if !ok {
		t.Fatal("danalyze not found after ANALYZE")
	}
	if tbl.Stats == nil || len(tbl.Stats.Columns) != 2 {
		t.Fatalf("Stats=%+v, want 2 populated columns", tbl.Stats)
	}
	dCol := tbl.Stats.Columns[1]

	if len(dCol.MCV) != 1 {
		t.Fatalf("MCV=%+v, want exactly 1 entry", dCol.MCV)
	}
	if got, want := dCol.MCV[0].Value, "05.01.2026"; got != want {
		t.Errorf("MCV[0].Value = %q, want %q (German DateStyle)", got, want)
	}

	wantHist := []string{"01.02.2026", "02.02.2026", "03.02.2026", "04.02.2026", "05.02.2026"}
	if len(dCol.Histogram) != len(wantHist) {
		t.Fatalf("Histogram=%v, want %v", dCol.Histogram, wantHist)
	}
	for i, want := range wantHist {
		if dCol.Histogram[i] != want {
			t.Errorf("Histogram[%d] = %q, want %q (German DateStyle)", i, dCol.Histogram[i], want)
		}
	}
}

// TestAnalyzeMCVHistogramNilCtxDefaultsISO confirms the test-only
// analyzeRelation wrapper (no session Context reachable, nil dsCtx threaded
// into computeColumnStats) still defaults to ISO/MDY, matching Format()'s
// pre-existing hardcoded behavior — so non-session callers are
// behavior-unchanged by this fix.
func TestAnalyzeMCVHistogramNilCtxDefaultsISO(t *testing.T) {
	ctx, cleanup := newVMFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE danalyze2 (id int, d date)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	for i := 0; i < 5; i++ {
		if err := runDDL(t, ctx, "INSERT INTO danalyze2 VALUES (1, '2026-01-05')"); err != nil {
			t.Fatalf("INSERT dup %d: %v", i, err)
		}
	}
	for i, d := range []string{"2026-02-01", "2026-02-02"} {
		if err := runDDL(t, ctx, "INSERT INTO danalyze2 VALUES (2, '"+d+"')"); err != nil {
			t.Fatalf("INSERT distinct %d: %v", i, err)
		}
	}

	commitTx(t, ctx)

	tbl, ok := ctx.Catalog.LookupTable(parser.ObjectName{Name: "danalyze2"})
	if !ok {
		t.Fatal("danalyze2 not found")
	}
	stats, err := analyzeRelation(ctx.Pool, ctx.TxnMgr, ctx.Catalog, tbl)
	if err != nil {
		t.Fatalf("analyzeRelation: %v", err)
	}
	dCol := stats.Columns[1]
	if len(dCol.MCV) != 1 {
		t.Fatalf("MCV=%+v, want exactly 1 entry", dCol.MCV)
	}
	if got, want := dCol.MCV[0].Value, "2026-01-05"; got != want {
		t.Errorf("MCV[0].Value = %q, want %q (ISO default, no session GUC)", got, want)
	}
}
