package executor

// join_worker_path_test.go — M0127-P1.2, the worker-path exercise for the
// P1.1 probe-slot seam (design docs/design/leftdeep-joins/05 §2, stage E1).
//
// Why a separate exercise at all: the build path has a precedent for
// diverging in a worker WITHOUT saying so. tryFuseHashCascade declines
// outright when env.inWorker is set (C10/F4), so a mechanism can be live in
// every serial test and dead in every parallel one, and nothing fails. P1.1's
// seam is the opposite risk — it is live in workers, and being live there is
// what makes it interesting: a chained probe source aliases the child's
// storage, and a worker's rows cross a goroutine boundary into the leader.
//
// So there are three separate claims here, and no one of them implies
// another:
//
//  1. the seam ENGAGES under BuildWorker (it is not silently declined, the
//     way fusion is);
//  2. a chained emit still produces rows that are safe to hand to another
//     goroutine after MaterializeForTransfer — the serial result loops format
//     each row as it arrives and would never notice otherwise;
//  3. a real Gather returns the same rows as serial execution in BOTH seam
//     arms, with and without leader participation.
//
// The exercise also found the reason `go test -race` was red for every
// parallel-join test: buildWithEnv wrote a package-global buildEnvInFlight,
// and gatherOp.runWorker calls BuildWorker from each worker's own goroutine
// while the leader builds its own child tree. That global is now a local
// (executor.go); this file is what keeps the worker build path exercised
// under the race detector.
//
// M0127-P6.1 stamp: runtime hash-join fusion is DELETED, so the precedent
// cited above (tryFuseHashCascade declining on env.inWorker) is history —
// `buildEnv` went with it and BuildWorker now builds exactly what Build
// builds. Claim (1) below therefore reads as "the seam engages under the
// worker entry point", with no remaining mechanism that declines there.

import (
	"sort"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/optimizer"
)

// workerSeamSQL is an INNER hash join over the P8 fixture: one join node, a
// probe side wide enough that chaining is not trivially a no-op, and a text
// column so the transfer check has an arena-backed datum to look at.
const workerSeamSQL = "SELECT f.fid, f.amt, d.dname FROM pq_fact f JOIN pq_dim d ON f.fk = d.dk"

// planWorkerQuery parses and plans sql against the fixture catalog.
func planWorkerQuery(t *testing.T, ctx *Context, sql string) optimizer.Node {
	t.Helper()
	stmts, err := parser.Parse(sql)
	if err != nil {
		t.Fatalf("parse %q: %v", sql, err)
	}
	node, err := optimizer.Plan(stmts[0], ctx.Catalog)
	if err != nil {
		t.Fatalf("plan %q: %v", sql, err)
	}
	return node
}

// drainWorkerTree runs a worker-built operator tree the way gatherOp.runWorker
// does — every emitted slot goes through MaterializeForTransfer and is
// RETAINED — and returns the rows plus the hash joins found in the tree.
//
// Retention is the point. A serial consumer formats each row before pulling
// the next, so a probe source that the next pull overwrites is invisible to
// it; a worker batches 256 rows before sending, so the same defect corrupts
// every row but the last.
func drainWorkerTree(t *testing.T, ctx *Context, node optimizer.Node) ([]Row, []*joinOp) {
	t.Helper()
	// M0129-S8.3: advance the command counter between statements.
	advanceStmtCounter(ctx)
	op, err := BuildWorker(node)
	if err != nil {
		t.Fatalf("BuildWorker: %v", err)
	}
	if err := op.Open(ctx); err != nil {
		t.Fatalf("open: %v", err)
	}
	var joins []*joinOp
	collectShareableJoins(op, &joins)

	var out []Row
	for {
		slot, err := op.Next()
		if err == EOF {
			break
		}
		if err != nil {
			_ = op.Close()
			t.Fatalf("next: %v", err)
		}
		if slot == nil {
			continue
		}
		row := MaterializeForTransfer(slot.Row())
		if err := AssertTransferable(row); err != nil {
			_ = op.Close()
			t.Fatalf("row %d is not safe to send to the leader: %v", len(out), err)
		}
		out = append(out, row)
	}
	if err := op.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	return out, joins
}

// TestJoinSeamEngagesUnderBuildWorker is claim (1): the P1.1 seam must be LIVE
// in a worker-built tree.
//
// It is asserted structurally rather than by result, because a declined seam
// returns exactly the same rows — that is the whole design of the copy
// fallback, and the reason the divergence would otherwise be silent.
func TestJoinSeamEngagesUnderBuildWorker(t *testing.T) {
	ctx, cleanup := pqJoinFixture(t)
	defer cleanup()

	node := planWorkerQuery(t, ctx, workerSeamSQL)
	op, err := BuildWorker(node)
	if err != nil {
		t.Fatalf("BuildWorker: %v", err)
	}
	if err := op.Open(ctx); err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = op.Close() }()

	var joins []*joinOp
	collectShareableJoins(op, &joins)
	if len(joins) != 1 {
		t.Fatalf("worker tree has %d shareable hash joins, want 1 — the "+
			"planner no longer produces a hash join for %q, so this test is "+
			"asserting nothing", len(joins), workerSeamSQL)
	}
	j := joins[0]

	// One pull is enough: bindProbe runs per probe row, so the state after
	// the first emitted row already tells us which arm the seam took.
	if _, err := op.Next(); err != nil {
		t.Fatalf("first row: %v", err)
	}
	if !j.lazyChainProbe {
		t.Fatal("seam is OFF in a worker-built join: ensureLazyVirtual read " +
			"joinSlotChainEnabled as false. Fusion declines in workers by " +
			"design (C10/F4); the P1.1 seam must not")
	}
	if j.lazyProbeSrc == nil {
		t.Fatal("no probe slot bound after the first emitted row")
	}
	if j.lazyProbeSrc == TupleSlot(j.lazyProbeSlot) {
		t.Fatalf("worker join took the COPY fallback: probe source is the "+
			"join's own lazyProbeSlot, not the child's slot (probe width %d)",
			j.lazyProbeWidth)
	}
	if got := j.lazyVirtualOut.sources[j.lazyProbeSrcIdx]; got != j.lazyProbeSrc {
		t.Errorf("sources[%d] is not the bound probe slot", j.lazyProbeSrcIdx)
	}
}

// TestJoinSeamWorkerTransferIndependence is claim (2): rows produced through
// the chained emit and retained across later pulls must still be correct and
// still be arena-free.
//
// Both seam arms run, and both are compared against the SAME serial baseline,
// so this is also the identity check for the seam at the transfer boundary.
func TestJoinSeamWorkerTransferIndependence(t *testing.T) {
	ctx, cleanup := pqJoinFixture(t)
	defer cleanup()

	serialRows, err := runQueryWithErr(ctx, workerSeamSQL)
	if err != nil {
		t.Fatalf("serial: %v", err)
	}
	want := renderRows(serialRows)
	sort.Strings(want)
	if len(want) == 0 {
		t.Fatal("fixture produced no joined rows; the comparison is vacuous")
	}

	for _, chained := range []bool{true, false} {
		name := "chained"
		if !chained {
			name = "killswitch"
		}
		t.Run(name, func(t *testing.T) {
			SetJoinSlotChainEnabled(chained)
			defer SetJoinSlotChainEnabled(true)

			// The flag is read once per joinOp in ensureLazyVirtual, so the
			// plan must be built AFTER it is set.
			node := planWorkerQuery(t, ctx, workerSeamSQL)
			got, joins := drainWorkerTree(t, ctx, node)
			if len(joins) == 1 && joins[0].lazyChainProbe != chained {
				t.Fatalf("seam arm not honoured: lazyChainProbe=%v, want %v",
					joins[0].lazyChainProbe, chained)
			}
			rendered := renderRows(got)
			sort.Strings(rendered)
			if len(rendered) != len(want) {
				t.Fatalf("got %d rows, want %d", len(rendered), len(want))
			}
			for i := range want {
				if rendered[i] != want[i] {
					t.Fatalf("row %d differs after retention: got %q, want %q\n"+
						"a mismatch here means the retained row still read "+
						"through to the probe child's storage",
						i, rendered[i], want[i])
				}
			}
		})
	}
}

// runSeamGathered plans sql, wraps it in a Gather, and drains it with the
// given worker count and leader-participation setting.
//
// It is deliberately not parallel_hash_join_test.go's runJoinGathered:
// leaderParticipation must be controllable here, because with the leader
// participating some rows never cross a goroutine boundary at all — turning
// it off is what forces EVERY row through the worker transfer path.
func runSeamGathered(t *testing.T, ctx *Context, sql string, workers int, leaderParticipation bool) ([]string, bool) {
	t.Helper()
	// M0129-S8.3: advance the command counter between statements.
	advanceStmtCounter(ctx)
	node := planWorkerQuery(t, ctx, sql)
	gathered := optimizer.MaybeAddGather(node, optimizer.ParallelSettings{
		MaxWorkersPerGather: workers,
		MinTableScanBlocks:  1,
		DebugParallelQuery:  "on", // force past the size gate; the fixture is small
		BlocksForTable:      func(*catalog.Table) (int64, bool) { return 4096, true },
	})
	hasGather := planTreeHasParallelNode(gathered)

	ctx.MaxParallelWorkers = 8
	ctx.ParallelLeaderParticipation = leaderParticipation
	op, err := Build(gathered)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if err := op.Open(ctx); err != nil {
		t.Fatalf("open: %v", err)
	}
	var out []string
	for {
		slot, err := op.Next()
		if err == EOF {
			break
		}
		if err != nil {
			_ = op.Close()
			t.Fatalf("next: %v", err)
		}
		out = append(out, renderRows([]Row{slot.Row()})...)
	}
	if err := op.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	return out, hasGather
}

// TestJoinSeamGatheredIdentity is claim (3), and the test the RACE bar is
// about: real Gather, real worker goroutines, both seam arms, with and
// without leader participation.
//
// The leader-participation=false arm is the one that matters most — there,
// every row is produced by a worker's chained emit, batched, and read by the
// leader after the worker has moved on.
func TestJoinSeamGatheredIdentity(t *testing.T) {
	ctx, cleanup := pqJoinFixture(t)
	defer cleanup()

	// Baseline is serial execution with the seam in its default (chained)
	// state — every arm below is compared against this one set of rows, not
	// against a per-arm baseline that could drift with it.
	baseline := make(map[string][]string, len(pqJoinCorpus()))
	for _, sql := range pqJoinCorpus() {
		rows, err := runQueryWithErr(ctx, sql)
		if err != nil {
			t.Fatalf("serial %q: %v", sql, err)
		}
		want := renderRows(rows)
		sort.Strings(want)
		baseline[sql] = want
	}

	gathered := 0
	for _, chained := range []bool{true, false} {
		arm := "chained"
		if !chained {
			arm = "killswitch"
		}
		t.Run(arm, func(t *testing.T) {
			SetJoinSlotChainEnabled(chained)
			defer SetJoinSlotChainEnabled(true)

			for _, sql := range pqJoinCorpus() {
				want := baseline[sql]
				for _, leader := range []bool{true, false} {
					for _, workers := range []int{2, 4} {
						got, hasGather := runSeamGathered(t, ctx, sql, workers, leader)
						if hasGather {
							gathered++
						}
						sort.Strings(got)
						if len(got) != len(want) {
							t.Fatalf("%q leader=%v workers=%d: got %d rows, want %d",
								sql, leader, workers, len(got), len(want))
						}
						for i := range want {
							if got[i] != want[i] {
								t.Fatalf("%q leader=%v workers=%d: row %d differs: got %q, want %q",
									sql, leader, workers, i, got[i], want[i])
							}
						}
					}
				}
			}
		})
	}
	if gathered == 0 {
		t.Fatal("no query produced a Gather; every comparison above was " +
			"serial against serial and the worker path was never entered")
	}
}
