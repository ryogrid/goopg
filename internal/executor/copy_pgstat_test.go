package executor

// M-NIGHTLY "COPY skips the pgstat row counters" (filed 2026-10-31 by the
// relpages recon loop). goopg's COPY FROM never fed the cumulative relation
// counters: heap_insert ends with pgstat_count_heap_insert(relation, 1)
// (heapam.c:2251) and heap_multi_insert with
// pgstat_count_heap_insert(rel, ntuples) (heapam.c:2702), so a COPY FROM must
// land the same counts recordRelInsert lands for INSERT. Before, a
// COPY-loaded table carried ins=0 / mod=0, and the autovacuum insert-growth
// triggers (needsVacuum ins arm, needsAnalyze mod arm) never fired for
// pgbench -i style loads.
//
// The fixture ctx is not an explicit transaction block, so recordRelInsert
// folds staging into pending at record time; relStats.flush then mirrors
// pg_stat_force_next_flush() into the shared store the pg_stat_get_* getters
// read. relStats is process-global and fixtures recycle relation OIDs, so
// every assertion below is a DELTA around the COPY, not an absolute value.

import (
	"fmt"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

func TestCopyFromFeedsPgstatRowCounters(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()

	for _, q := range []string{
		"CREATE TABLE cps_multi (a int)",
		"CREATE TABLE cps_single (a int)",
		"CREATE TABLE cps_supp (a int)",
		"CREATE FUNCTION cps_noop() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NEW; END $$",
		// BEFORE ROW trigger: forces the CIM_SINGLE arm AND suppresses every
		// even row (RETURN NULL) — a suppressed row is not inserted and must
		// not be counted (storeCopyRow's M0146-0055 rule; in PG the tuple
		// never reaches heap_insert, so pgstat_count_heap_insert is never
		// reached for it).
		"CREATE FUNCTION cps_supp_fn() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF new.a % 2 = 0 THEN RETURN NULL; END IF; RETURN NEW; END $$",
		"CREATE TRIGGER cps_supp_trg BEFORE INSERT ON cps_supp FOR EACH ROW EXECUTE FUNCTION cps_supp_fn()",
		"CREATE TRIGGER cps_single_trg BEFORE INSERT ON cps_single FOR EACH ROW EXECUTE FUNCTION cps_noop()",
	} {
		if err := runDDL(t, ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}

	const rows = 5000
	for _, tc := range []struct {
		table     string
		multi     bool  // expected CIM_MULTI / CIM_SINGLE arm
		wantCount int64 // rows expected in every counter delta
	}{
		{"cps_multi", true, rows},     // counted per flush batch (heapam.c:2702)
		{"cps_single", false, rows},   // counted per row (heapam.c:2251)
		{"cps_supp", false, rows / 2}, // suppressed rows are not counted
	} {
		tbl, ok := cat.LookupTable(parser.ObjectName{Name: tc.table})
		if !ok {
			t.Fatalf("%s not found", tc.table)
		}
		// Baselines: the process-global store and recycled OIDs mean earlier
		// package tests may have left counts for this OID behind — drain the
		// shared (id-0) session's pending bucket first, so the deltas below
		// count only THIS COPY.
		relStats.flush(sessionStatsID(ctx))
		dead0, ins0, mod0 := TriggerSnapshot(tbl.OID)
		shared0, _ := relStats.get(tbl.OID)

		cf, err := NewCopyFromExecutor(ctx, &optimizer.Copy{
			Direction:   optimizer.CopyFrom,
			Table:       tbl,
			ColumnIndex: []int{0},
			Endpoint:    optimizer.CopyEndpointStdin,
		})
		if err != nil {
			t.Fatal(err)
		}
		if cf.multiInsert != tc.multi {
			t.Fatalf("%s: multiInsert=%v, want %v", tc.table, cf.multiInsert, tc.multi)
		}
		for i := 1; i <= rows; i++ {
			if err := cf.PushLine([]byte(fmt.Sprint(i))); err != nil {
				t.Fatalf("%s line %d: %v", tc.table, i, err)
			}
		}
		if err := cf.Finish(); err != nil {
			t.Fatalf("%s Finish: %v", tc.table, err)
		}

		// Autovacuum-trigger counters (recordInsert's direct bumpTriggers):
		// ins = inserted since vacuum, mod = inserted+updated+deleted since
		// analyze, dead untouched. This is the arm the filed consequence
		// names: needsVacuum's ins arm and needsAnalyze's mod arm.
		dead1, ins1, mod1 := TriggerSnapshot(tbl.OID)
		if ins1-ins0 != tc.wantCount || mod1-mod0 != tc.wantCount {
			t.Errorf("%s: trigger counters dIns=%d dMod=%d, want %d/%d", tc.table, ins1-ins0, mod1-mod0, tc.wantCount, tc.wantCount)
		}
		if dead1 != dead0 {
			t.Errorf("%s: trigger dead moved %d -> %d, COPY must not bump n_dead_tup", tc.table, dead0, dead1)
		}

		// Cumulative tier: staging folded into pending by recordRelInsert
		// (autocommit ctx), flushed into the shared store the pg_stat_get_*
		// getters read.
		relStats.flush(sessionStatsID(ctx))
		c1, ok := relStats.get(tbl.OID)
		if !ok {
			t.Fatalf("%s: no flushed shared counters after COPY", tc.table)
		}
		if c1.tuplesInserted-shared0.tuplesInserted != tc.wantCount {
			t.Errorf("%s: dTuplesInserted=%d, want %d", tc.table, c1.tuplesInserted-shared0.tuplesInserted, tc.wantCount)
		}
		if c1.deltaLive-shared0.deltaLive != tc.wantCount {
			t.Errorf("%s: dDeltaLive=%d, want %d (pg_stat_get_live_tuples arm)", tc.table, c1.deltaLive-shared0.deltaLive, tc.wantCount)
		}
		if c1.changedTuples-shared0.changedTuples != tc.wantCount {
			t.Errorf("%s: dChangedTuples=%d, want %d (feeds n_mod_since_analyze)", tc.table, c1.changedTuples-shared0.changedTuples, tc.wantCount)
		}
		if c1.insSinceVacuum-shared0.insSinceVacuum != tc.wantCount {
			t.Errorf("%s: dInsSinceVacuum=%d, want %d (feeds n_ins_since_vacuum)", tc.table, c1.insSinceVacuum-shared0.insSinceVacuum, tc.wantCount)
		}

		// The rows are really there (the counters ride the data, not a
		// post-hoc estimate).
		countRows := runSQL(t, ctx, "SELECT count(*) FROM "+tc.table)
		if got := fmt.Sprint(countRows[0]); !strings.Contains(got, fmt.Sprint(tc.wantCount)) {
			t.Errorf("%s: SELECT count(*) = %s, want %d", tc.table, got, tc.wantCount)
		}
	}
}

// TestCopyStagedCountsCommitAbortTwin pins the tier math recordRelInsert
// feeds COPY into (shared verbatim with INSERT): attempted inserts count on
// commit AND abort (PG's AtEOXact_PgStat_Relations adds the counts either
// way — a rolled-back INSERT's n_tup_ins is visible in pg_stat), while the
// live/dead deltas and the mod_since_analyze change events follow the
// commit/abort arms. The fixture ctx is autocommit, so the block-level fold
// is driven through the manager directly (exactly the tiers
// recordRelInsert/CommitRelStats/AbortRelStats use). The server-level COPY
// (which runs its own auto-commit transaction, as PG reports COPY counts at
// statement end) is pinned by the testport sibling.
func TestCopyStagedCountsCommitAbortTwin(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()
	for _, q := range []string{
		"CREATE TABLE cps_rb (a int)",
		"CREATE TABLE cps_rb_commit (a int)",
	} {
		if err := runDDL(t, ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	id := sessionStatsID(ctx)
	relStats.flush(id) // drain other fixtures' residue from the shared id-0 bucket

	// Abort twin: an aborted block's staged inserts STILL count as attempted
	// tuples (applyXactToPending's commit-independent arm, PG's
	// AtEOXact_PgStat_Relations: a rolled-back INSERT's n_tup_ins is visible
	// in pg_stat), and the aborted rows land on the dead side — live delta
	// and change events (mod_since_analyze) see nothing.
	tbl, ok := cat.LookupTable(parser.ObjectName{Name: "cps_rb"})
	if !ok {
		t.Fatal("cps_rb not found")
	}
	before, _ := relStats.get(tbl.OID)
	relStats.recordInsert(id, tbl.OID, 42)
	relStats.abortXact(id)
	relStats.flush(id)
	after, _ := relStats.get(tbl.OID)
	if after.tuplesInserted-before.tuplesInserted != 42 {
		t.Errorf("cps_rb: dTuplesInserted=%d, want 42 (aborted inserts count as attempted, PG parity)", after.tuplesInserted-before.tuplesInserted)
	}
	if after.deltaLive-before.deltaLive != 0 {
		t.Errorf("cps_rb: dDeltaLive=%d, want 0 (an aborted insert adds no live tuple)", after.deltaLive-before.deltaLive)
	}
	if after.changedTuples-before.changedTuples != 0 {
		t.Errorf("cps_rb: dChangedTuples=%d, want 0 (aborted xact generates no change events)", after.changedTuples-before.changedTuples)
	}
	if after.deltaDead-before.deltaDead != 42 {
		t.Errorf("cps_rb: dDeltaDead=%d, want 42 (the aborted insert's tuples are dead)", after.deltaDead-before.deltaDead)
	}

	// Commit twin: the same staged volume folded at COMMIT reaches the
	// live/change arms as well.
	tbl2, ok := cat.LookupTable(parser.ObjectName{Name: "cps_rb_commit"})
	if !ok {
		t.Fatal("cps_rb_commit not found")
	}
	before2, _ := relStats.get(tbl2.OID)
	relStats.recordInsert(id, tbl2.OID, 42)
	relStats.commitXact(id)
	relStats.flush(id)
	after2, _ := relStats.get(tbl2.OID)
	if after2.tuplesInserted-before2.tuplesInserted != 42 {
		t.Errorf("cps_rb_commit: dTuplesInserted=%d, want 42", after2.tuplesInserted-before2.tuplesInserted)
	}
	if after2.deltaLive-before2.deltaLive != 42 {
		t.Errorf("cps_rb_commit: dDeltaLive=%d, want 42", after2.deltaLive-before2.deltaLive)
	}
	if after2.changedTuples-before2.changedTuples != 42 {
		t.Errorf("cps_rb_commit: dChangedTuples=%d, want 42 (feeds mod_since_analyze)", after2.changedTuples-before2.changedTuples)
	}
	if after2.deltaDead-before2.deltaDead != 0 {
		t.Errorf("cps_rb_commit: dDeltaDead=%d, want 0", after2.deltaDead-before2.deltaDead)
	}
}
