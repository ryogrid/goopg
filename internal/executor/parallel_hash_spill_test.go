package executor

// E-09a — a SPILLING shared hash build (Variant A, private reload).
// Design: docs/design/executor-e09a-shared-spilling-build/DESIGN.md §7.
//
// This is a wrong-answer class. Every way the mechanism can fail returns a
// plausible, smaller result with no error anywhere: a participant handed the
// batch-0 maps alone probes one partition; a participant that re-routes a
// row into a shared file corrupts what its peers read; a batch skipped for
// the wrong reason loses its rows. So the gate is identity against the SERIAL
// join over the same rows, at a work_mem small enough that the build must
// batch, for every join type the shared build admits — plus the three
// invariants the design pins, each with a counter or a poison rather than an
// argument:
//
//   - a participant never writes or unlinks a shared inner file: the files
//     are made read-only on disk (a poison writer) and writeInner refuses;
//   - growth is frozen on the reload path;
//   - every participant opens each inner file it needs exactly once.
//
// Two harnesses. The operator-level one (spillParticipants) builds the shape
// by hand — the planner is not consulted, per the repo rule that an unwinnable
// path is an untested path — and runs N participants as goroutines over
// disjoint probe slices, which is exactly what a Gather's block partition
// hands them. The SQL-level one goes through MaybeAddGather so the real
// fan-out (worker contexts, leader participation, Close ordering) is under
// test too.

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

// spillShape is one shareable join type under test.
type spillShape struct {
	name      string
	typ       optimizer.JoinType
	nullAware bool
}

func spillShapes() []spillShape {
	return []spillShape{
		{"inner", optimizer.JoinTypeInner, false},
		{"left-probe-fill", optimizer.JoinTypeLeft, false},
		{"semi", optimizer.JoinTypeSemi, false},
		{"anti", optimizer.JoinTypeAnti, false},
		{"anti-not-in", optimizer.JoinTypeAnti, true},
	}
}

// spillPlan is a single-key hash join, probe on the left, build on the right.
func spillPlan(sh spillShape, lw, estLeft, estRight int) *optimizer.Join {
	col := func(idx int) *optimizer.ColumnRef {
		return &optimizer.ColumnRef{Index: idx, Type: catalog.Type{Name: "int4"}}
	}
	return &optimizer.Join{
		Type:      sh.typ,
		Algo:      optimizer.JoinAlgoHash,
		NullAware: sh.nullAware,
		LeftKey:   col(0),
		RightKey:  col(lw),
		Left:      valuesNode(estLeft),
		Right:     valuesNode(estRight),
	}
}

// spillRows builds n rows of [key, payload]; the key cycles over `distinct`
// values and every `nullEvery`-th row has a NULL key (0 = none).
func spillRows(n, distinct, nullEvery int, payload string) []Row {
	rows := make([]Row, n)
	for i := range rows {
		k := NewIntDatum(int64(i % distinct))
		if nullEvery > 0 && i%nullEvery == 0 {
			k = NullDatum
		}
		rows[i] = Row{k, NewStringDatum(fmt.Sprintf("%s%d-%s", payload, i, strings.Repeat("x", 40)))}
	}
	return rows
}

// poisonBuildOp is a build-side child that fails if anyone Opens it: a
// participant that adopts a shared build must never touch its build child.
type poisonBuildOp struct {
	schema optimizer.Schema
}

func (p *poisonBuildOp) Open(*Context) error {
	return fmt.Errorf("participant opened the BUILD side of a shared hash join")
}
func (p *poisonBuildOp) Next() (TupleSlot, error) { return nil, EOF }
func (p *poisonBuildOp) Close() error             { return nil }
func (p *poisonBuildOp) Schema() optimizer.Schema { return p.schema }

// innerOpenCounter records loadInnerBatch's opens per (participant, batch)
// through testHookInnerBatchOpened.
type innerOpenCounter struct {
	mu     sync.Mutex
	opens  map[*hashBatchState]map[int]int
	paths  map[string]bool
	onOpen func(b int)
}

func (c *innerOpenCounter) install() {
	c.opens = make(map[*hashBatchState]map[int]int)
	c.paths = make(map[string]bool)
	testHookInnerBatchOpened = func(bs *hashBatchState, b int, path string) {
		c.mu.Lock()
		if c.opens[bs] == nil {
			c.opens[bs] = make(map[int]int)
		}
		c.opens[bs][b]++
		c.paths[path] = true
		fn := c.onOpen
		c.mu.Unlock()
		if fn != nil {
			fn(b)
		}
	}
}

func (c *innerOpenCounter) uninstall() { testHookInnerBatchOpened = nil }

// assertExactlyOnce checks that no (participant, batch) was opened twice, that
// at least minParticipants distinct participants reloaded something, and —
// when strict — that every participant opened every inner file the
// descriptor carries.
func (c *innerOpenCounter) assertExactlyOnce(t *testing.T, d *sharedBatchDesc, minParticipants int, strict bool) {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.opens) < minParticipants {
		t.Fatalf("%d participant(s) reloaded a batch, want at least %d", len(c.opens), minParticipants)
	}
	for bs, m := range c.opens {
		if !bs.innerShared {
			t.Fatalf("a participant reloaded through a non-shared batch state")
		}
		if bs.growEnabled {
			t.Fatalf("growth is enabled on a participant batch state")
		}
		for b, n := range m {
			if n != 1 {
				t.Fatalf("participant opened inner batch %d %d times", b, n)
			}
			if b <= 0 || b >= d.nbatch || d.inner[b] == nil {
				t.Fatalf("participant opened batch %d, which the descriptor does not carry", b)
			}
		}
		if strict {
			for b, f := range d.inner {
				if f == nil {
					continue
				}
				if m[b] != 1 {
					t.Fatalf("participant opened inner batch %d %d times, want exactly 1", b, m[b])
				}
			}
		}
	}
}

// spillParticipants prebuilds one shared build for plan, poisons its files
// against writers, and runs n participants concurrently over round-robin
// slices of probeRows. Returns the sorted union of their outputs.
func spillParticipants(t *testing.T, plan *optimizer.Join, probeRows, buildRows []Row, lw, rw int, workMem int64, n int, counter *innerOpenCounter) ([]string, *sharedHashBuild) {
	t.Helper()
	ctx := &Context{WorkMem: workMem, tempFiles: newTempFileRegistry()}
	tree := newJoinOp(plan,
		&rowsOp{rows: probeRows, schema: batchSchema("l", lw)},
		&rowsOp{rows: buildRows, schema: batchSchema("r", rw)})
	builds, err := prebuildSharedHashJoins(ctx, plan, func() (Operator, error) { return tree, nil })
	if err != nil {
		t.Fatalf("prebuild: %v", err)
	}
	sb := builds[plan]
	if sb == nil {
		t.Fatalf("no build published")
	}
	ctx.SharedHashBuilds = builds
	// Poison writer: every shared inner file is read-only on disk from here,
	// so a write that slipped past writeInner's guard would fail at the OS.
	if sb.batches != nil {
		for _, f := range sb.batches.inner {
			if f == nil {
				continue
			}
			if f.w != nil {
				t.Fatalf("published inner file still has a writer")
			}
			if err := os.Chmod(f.path, 0o444); err != nil {
				t.Fatalf("chmod: %v", err)
			}
		}
	}

	if counter != nil {
		counter.install()
		defer counter.uninstall()
	}
	var mu sync.Mutex
	var out []string
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		var slice []Row
		for j := i; j < len(probeRows); j += n {
			slice = append(slice, probeRows[j])
		}
		wg.Add(1)
		go func(i int, slice []Row) {
			defer wg.Done()
			wctx := &Context{WorkMem: workMem, tempFiles: ctx.tempFiles, SharedHashBuilds: builds}
			op := newJoinOp(plan,
				&rowsOp{rows: slice, schema: batchSchema("l", lw)},
				&poisonBuildOp{schema: batchSchema("r", rw)})
			if err := op.Open(wctx); err != nil {
				errs[i] = fmt.Errorf("open: %w", err)
				return
			}
			if sb.batches != nil && (op.batches == nil || !op.batches.innerShared) {
				errs[i] = fmt.Errorf("participant did not get a private shared-inner batch state")
				return
			}
			var mine []string
			for {
				slot, err := op.Next()
				if err == EOF {
					break
				}
				if err != nil {
					errs[i] = fmt.Errorf("next: %w", err)
					return
				}
				parts := make([]string, slot.Width())
				for k := range parts {
					parts[k] = fmt.Sprint(datumToString(slot.Get(k)))
				}
				mine = append(mine, strings.Join(parts, "|"))
			}
			if err := op.Close(); err != nil {
				errs[i] = fmt.Errorf("close: %w", err)
				return
			}
			mu.Lock()
			out = append(out, mine...)
			mu.Unlock()
		}(i, slice)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("participant %d: %v", i, err)
		}
	}
	// The files must survive every participant's Close (each only forgets
	// them) and go away with the publication.
	var paths []string
	if sb.batches != nil {
		for _, f := range sb.batches.inner {
			if f != nil {
				paths = append(paths, f.path)
			}
		}
	}
	for _, p := range paths {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("shared inner file %s did not survive the participants: %v", p, err)
		}
	}
	releaseSharedHashBuilds(ctx)
	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			t.Fatalf("shared inner file %s survived the release", p)
		}
	}
	if left := ctx.ReleaseSpillFiles(); left != 0 {
		t.Fatalf("%d spill file(s) reached the statement-end backstop", left)
	}
	sort.Strings(out)
	return out, sb
}

// TestSharedSpillingBuildParticipantsMatchSerial is the gate: for every
// shareable join type, N participants over a shared, batched build return
// exactly what one serial, unbatched join returns.
func TestSharedSpillingBuildParticipantsMatchSerial(t *testing.T) {
	const lw, rw = 2, 2
	buildRows := spillRows(6000, 900, 97, "b")  // every 97th build key NULL
	probeRows := spillRows(3000, 1200, 53, "p") // keys 900..1199 match nothing

	for _, sh := range spillShapes() {
		for _, arm := range []struct {
			name     string
			estRight int
			workMem  int64
			grew     bool
		}{
			// The estimate is honest: the geometry is multi-batch before the
			// first build row arrives.
			{"estimate-said-spill", len(buildRows), 128 << 10, false},
			// The estimate said it fits and it did not: growth fires during
			// the prebuild (DESIGN.md §7 "estimate-said-fit-but-didn't").
			{"estimate-said-fit", 50, 128 << 10, true},
		} {
			t.Run(sh.name+"/"+arm.name, func(t *testing.T) {
				plan := spillPlan(sh, lw, len(probeRows), arm.estRight)
				want, _ := runBatchJoin(t, plan, probeRows, buildRows, lw, rw, unboundedWorkMem)
				if sh.nullAware && len(want) != 0 {
					t.Fatalf("precondition: NOT IN over a NULL-containing build must be empty serially")
				}
				if !sh.nullAware && len(want) == 0 {
					t.Fatalf("precondition: the serial join returned nothing")
				}

				counter := &innerOpenCounter{}
				got, sb := spillParticipants(t, plan, probeRows, buildRows, lw, rw, arm.workMem, 4, counter)
				d := sb.batches
				if d == nil || d.nbatch <= 1 {
					t.Fatalf("precondition: the shared build did not batch")
				}
				if arm.grew && d.nbatch <= d.origNBatch {
					t.Fatalf("precondition: growth did not fire during the prebuild (nbatch=%d orig=%d)", d.nbatch, d.origNBatch)
				}
				if !arm.grew && d.origNBatch <= 1 {
					t.Fatalf("precondition: the estimate did not choose a multi-batch geometry")
				}
				assertSameRows(t, want, got)
				// Every participant's probe slice spans every key, so every
				// participant needs every inner file — and may open each
				// exactly once.
				if sh.nullAware {
					// NOT IN over a NULL-containing build short-circuits
					// before any batch is reloaded: nothing to count.
					return
				}
				counter.assertExactlyOnce(t, d, 4, true)
			})
		}
	}
}

// TestSharedSpillingBuildFilesAreSettled pins the publication contract the
// participants rely on: after freezeForSharing no inner file holds a row of
// another batch, even when the build doubled nbatch more than once.
func TestSharedSpillingBuildFilesAreSettled(t *testing.T) {
	const lw, rw = 2, 2
	buildRows := spillRows(12000, 3000, 0, "b")
	probeRows := spillRows(100, 3000, 0, "p")
	plan := spillPlan(spillShapes()[0], lw, len(probeRows), 20)
	ctx := &Context{WorkMem: 96 << 10, tempFiles: newTempFileRegistry()}
	defer ctx.ReleaseSpillFiles()
	tree := newJoinOp(plan,
		&rowsOp{rows: probeRows, schema: batchSchema("l", lw)},
		&rowsOp{rows: buildRows, schema: batchSchema("r", rw)})
	builds, err := prebuildSharedHashJoins(ctx, plan, func() (Operator, error) { return tree, nil })
	if err != nil {
		t.Fatalf("prebuild: %v", err)
	}
	ctx.SharedHashBuilds = builds
	defer releaseSharedHashBuilds(ctx)
	d := builds[plan].batches
	if d == nil || d.nbatch < 4*d.origNBatch {
		t.Fatalf("precondition: want at least two doublings, got nbatch=%d orig=%d", d.nbatch, d.origNBatch)
	}
	probe := &hashBatchState{nbatch: d.nbatch, bucketBits: d.bucketBits}
	var total int64
	for k, f := range d.inner {
		if f == nil {
			continue
		}
		if f.w != nil {
			t.Fatalf("inner[%d] still has a writer", k)
		}
		if f.createdNBatch != d.nbatch {
			t.Fatalf("inner[%d] was created under nbatch=%d and not rewritten", k, f.createdNBatch)
		}
		r, err := newSpillReader(f.path)
		if err != nil {
			t.Fatalf("open inner[%d]: %v", k, err)
		}
		var n int64
		var buf Row
		for {
			h, row, err := r.ReadRowHashedInto(buf)
			if err != nil {
				break
			}
			buf = row
			n++
			if b := probe.batchOf(h); b != k {
				t.Fatalf("inner[%d] holds a row of batch %d after settling", k, b)
			}
		}
		r.closeKeepFile()
		if n != f.rows {
			t.Fatalf("inner[%d]: %d rows on disk, descriptor says %d", k, n, f.rows)
		}
		total += n
	}
	// Batch 0 is the in-memory map; everything else is on disk.
	inMem := 0
	for _, rows := range builds[plan].intHash {
		inMem += len(rows)
	}
	for _, rows := range builds[plan].hash {
		inMem += len(rows)
	}
	if int64(inMem)+total != int64(len(buildRows)) {
		t.Fatalf("build rows lost: %d in memory + %d on disk != %d", inMem, total, len(buildRows))
	}
}

// TestSharedInnerFilesRefuseParticipantWrites is the poison-writer test's
// direct arm: the participant state's inner-side writer refuses outright,
// and the guard fires before any file is touched.
func TestSharedInnerFilesRefuseParticipantWrites(t *testing.T) {
	d := &sharedBatchDesc{nbatch: 4, origNBatch: 4, bucketBits: 10, spaceAllowed: 1 << 20,
		inner: make([]*joinBatchFile, 4)}
	d.inner[2] = &joinBatchFile{path: "/nonexistent/poison", rows: 1, createdNBatch: 4}
	ctx := &Context{tempFiles: newTempFileRegistry()}
	bs := newParticipantBatchState(ctx, nil, d)
	if bs.growEnabled {
		t.Fatalf("participant state has growth enabled")
	}
	err := bs.writeInner(2, 0, Row{NewIntDatum(1)})
	var ee *ExecError
	if err == nil || !errorsAs(err, &ee) || ee.Code != "XX000" {
		t.Fatalf("writeInner on a shared state returned %v, want XX000", err)
	}
	// The outer side is the participant's own and must still work.
	if err := bs.writeOuter(2, 0, Row{NewIntDatum(1)}); err != nil {
		t.Fatalf("writeOuter on a shared state: %v", err)
	}
	if _, err := os.Stat("/nonexistent/poison"); err == nil {
		t.Fatalf("the poison path exists")
	}
	// close must not try to unlink the shared inner path, and must drop the
	// private outer file.
	outerPath := bs.outer[2].path
	bs.close()
	if _, err := os.Stat(outerPath); err == nil {
		t.Fatalf("participant close left its outer file %s", outerPath)
	}
	if d.inner[2] == nil {
		t.Fatalf("participant close cleared the descriptor's slot")
	}
	ctx.ReleaseSpillFiles()
}

func errorsAs(err error, target **ExecError) bool {
	e, ok := err.(*ExecError)
	if ok {
		*target = e
	}
	return ok
}

// ── SQL-level: the real Gather ──────────────────────────────────────────

// pqSpillFixture is pqJoinFixture's larger sibling: a dimension big enough
// that a 64 KiB work_mem cannot hold its build, with NULL keys on both sides.
func pqSpillFixture(t *testing.T) (*Context, func()) {
	t.Helper()
	ctx, _, cleanup := newDDLFixture(t)
	fail := func(err error, what string) {
		cleanup()
		t.Fatalf("%s: %v", what, err)
	}
	if err := runDDL(t, ctx, "CREATE TABLE ps_dim (dk int, dname text)"); err != nil {
		fail(err, "create dim")
	}
	if err := runDDL(t, ctx, "CREATE TABLE ps_fact (fid int, fk int, amt int)"); err != nil {
		fail(err, "create fact")
	}
	var sb strings.Builder
	for i := 0; i < 3000; i++ {
		if i > 0 {
			sb.WriteString(",")
		}
		k := fmt.Sprintf("%d", i)
		if i%211 == 7 {
			k = "NULL"
		}
		fmt.Fprintf(&sb, "(%s, 'd-%d-%s')", k, i, strings.Repeat("y", 48))
	}
	if err := runDDL(t, ctx, "INSERT INTO ps_dim VALUES "+sb.String()); err != nil {
		fail(err, "insert dim")
	}
	sb.Reset()
	for i := 0; i < 2000; i++ {
		if i > 0 {
			sb.WriteString(",")
		}
		fk := fmt.Sprintf("%d", (i*7)%4000) // keys >= 3000 have no dimension row
		if i%53 == 0 {
			fk = "NULL"
		}
		fmt.Fprintf(&sb, "(%d, %s, %d)", i, fk, i*2)
	}
	if err := runDDL(t, ctx, "INSERT INTO ps_fact VALUES "+sb.String()); err != nil {
		fail(err, "insert fact")
	}
	return ctx, cleanup
}

func pqSpillCorpus() []string {
	return []string{
		"SELECT f.fid, d.dname FROM ps_fact f JOIN ps_dim d ON f.fk = d.dk",
		"SELECT f.fid, d.dname FROM ps_fact f LEFT JOIN ps_dim d ON f.fk = d.dk",
		"SELECT f.fid FROM ps_fact f WHERE EXISTS (SELECT 1 FROM ps_dim d WHERE d.dk = f.fk)",
		"SELECT f.fid FROM ps_fact f WHERE NOT EXISTS (SELECT 1 FROM ps_dim d WHERE d.dk = f.fk)",
		"SELECT f.fid FROM ps_fact f WHERE f.fk NOT IN (SELECT d.dk FROM ps_dim d)",
		"SELECT f.fid FROM ps_fact f WHERE f.fk NOT IN (SELECT d.dk FROM ps_dim d WHERE d.dk IS NOT NULL)",
	}
}

// gatherSpillRun plans sql under a Gather with `workers` workers plus leader
// participation, runs it at workMem, and returns the rows, the shared build
// the Gather published (snapshotted after Open) and whether a Gather existed.
func gatherSpillRun(t *testing.T, ctx *Context, sql string, workers int, workMem int64, limit int) ([]string, *sharedHashBuild, bool) {
	t.Helper()
	advanceStmtCounter(ctx)
	stmts, err := parser.Parse(sql)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	node, err := optimizer.Plan(stmts[0], ctx.Catalog)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	gathered := optimizer.MaybeAddGather(node, optimizer.ParallelSettings{
		MaxWorkersPerGather: workers,
		MinTableScanBlocks:  1,
		DebugParallelQuery:  "on",
		BlocksForTable:      func(*catalog.Table) (int64, bool) { return 4096, true },
	})
	hasGather := planTreeHasParallelNode(gathered)
	ctx.MaxParallelWorkers = 8
	ctx.ParallelLeaderParticipation = true
	ctx.WorkMem = workMem
	op, err := Build(gathered)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if err := op.Open(ctx); err != nil {
		t.Fatalf("open: %v", err)
	}
	var sb *sharedHashBuild
	for _, b := range ctx.SharedHashBuilds {
		sb = b
	}
	var out []string
	for limit <= 0 || len(out) < limit {
		slot, err := op.Next()
		if err == EOF {
			break
		}
		if err != nil {
			t.Fatalf("next: %v", err)
		}
		out = append(out, renderRows([]Row{slot.Row()})...)
	}
	if err := op.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if ctx.SharedHashBuilds != nil {
		t.Fatalf("Close left SharedHashBuilds published")
	}
	return out, sb, hasGather
}

// TestParallelHashJoinSpillingSharedBuildIdentity: through the real Gather,
// with the build forced to batch, every shape matches the serial, unbatched
// result — and the build was shared, batched, and reloaded by more than one
// participant.
func TestParallelHashJoinSpillingSharedBuildIdentity(t *testing.T) {
	ctx, cleanup := pqSpillFixture(t)
	defer cleanup()
	const smallWorkMem = 64 << 10

	shared := 0
	for _, sql := range pqSpillCorpus() {
		t.Run(sql, func(t *testing.T) {
			ctx.WorkMem = unboundedWorkMem
			serialRows, err := runQueryWithErr(ctx, sql)
			if err != nil {
				t.Fatalf("serial: %v", err)
			}
			want := renderRows(serialRows)
			sort.Strings(want)

			for _, workers := range []int{2, 4} {
				counter := &innerOpenCounter{}
				counter.install()
				got, sb, hasGather := gatherSpillRun(t, ctx, sql, workers, smallWorkMem, 0)
				counter.uninstall()
				if !hasGather {
					t.Skipf("planner declined to parallelise this shape")
				}
				if sb == nil {
					t.Fatalf("workers=%d: the Gather published no shared build", workers)
				}
				d := sb.batches
				if d == nil || d.nbatch <= 1 {
					t.Fatalf("workers=%d: the shared build did not batch at work_mem=%d", workers, smallWorkMem)
				}
				shared++
				sort.Strings(got)
				if len(got) != len(want) {
					t.Fatalf("workers=%d: got %d rows, want %d — a participant probed one partition",
						workers, len(got), len(want))
				}
				for i := range want {
					if got[i] != want[i] {
						t.Fatalf("workers=%d: row %d differs: got %q, want %q", workers, i, got[i], want[i])
					}
				}
				// NOT IN over a NULL-containing build short-circuits before
				// any batch is reloaded, so the counter has nothing there.
				if len(want) > 0 {
					counter.assertExactlyOnce(t, d, 2, false)
				}
				// The descriptor's files must be gone after Close.
				for _, f := range d.inner {
					if f != nil {
						if _, err := os.Stat(f.path); err == nil {
							t.Fatalf("shared inner file %s survived Gather Close", f.path)
						}
					}
				}
			}
		})
	}
	if shared == 0 {
		t.Fatal("no query in the corpus produced a shared spilling build; the gate asserted nothing")
	}
}

// TestParallelHashJoinSpillingSharedBuildRunsOnce: the build side runs ONCE
// even though it spills. Counted, because a per-participant build would be
// correct and no identity test would notice.
func TestParallelHashJoinSpillingSharedBuildRunsOnce(t *testing.T) {
	ctx, cleanup := pqSpillFixture(t)
	defer cleanup()
	sql := "SELECT f.fid, d.dname FROM ps_fact f JOIN ps_dim d ON f.fk = d.dk"
	ctx.WorkMem = unboundedWorkMem
	serialRows, err := runQueryWithErr(ctx, sql)
	if err != nil {
		t.Fatalf("serial: %v", err)
	}
	// The serial run above registered its own plan node; only the Gather's
	// is under inspection.
	ctx.HashJoinStats = nil
	_, sb, hasGather := gatherSpillRun(t, ctx, sql, 4, 64<<10, 0)
	if !hasGather {
		t.Skip("planner declined to parallelise this shape")
	}
	if sb.batches == nil || sb.batches.nbatch <= 1 {
		t.Fatalf("the shared build did not batch")
	}
	// One build: the leader's stats carry one Build Time and the batch
	// geometry; a worker that rebuilt would have reported its own NBatch
	// through MergeWorkerContext and the map would show more than one key.
	if n := len(ctx.HashJoinStats); n != 1 {
		t.Fatalf("HashJoinStats has %d plan nodes, want 1", n)
	}
	for _, st := range ctx.HashJoinStats {
		if st.NBatch != sb.batches.nbatch {
			t.Fatalf("reported NBatch=%d, descriptor nbatch=%d", st.NBatch, sb.batches.nbatch)
		}
		if st.BuildTimeNs == 0 {
			t.Fatalf("no Build Time recorded by the leader's prebuild")
		}
	}
	_ = serialRows
}

// TestParallelHashJoinSpillingSharedBuildCancel: LIMIT above the Gather,
// cancelled while a participant is in batch 1 — no hang, files released.
func TestParallelHashJoinSpillingSharedBuildCancel(t *testing.T) {
	ctx, cleanup := pqSpillFixture(t)
	defer cleanup()
	for _, arm := range []struct {
		name  string
		limit int
	}{
		// LIMIT satisfied out of batch 0: Close cancels workers that are
		// still reloading later batches.
		{"limit-satisfied", 3},
		// LIMIT larger than the result, so the query reaches batch 1; the
		// statement is cancelled from inside the first batch-1 reload.
		{"statement-cancelled-mid-batch-1", 1000000},
	} {
		t.Run(arm.name, func(t *testing.T) {
			sql := fmt.Sprintf("SELECT f.fid, d.dname FROM ps_fact f JOIN ps_dim d ON f.fk = d.dk LIMIT %d", arm.limit)
			cctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			ctx.Ctx = cctx
			counter := &innerOpenCounter{}
			var once sync.Once
			var fired int32
			if arm.limit > 3 {
				counter.onOpen = func(b int) {
					if b >= 1 {
						once.Do(func() { fired = 1; cancel() })
					}
				}
			}
			counter.install()
			defer counter.uninstall()

			done := make(chan *sharedHashBuild, 1)
			go func() {
				advanceStmtCounter(ctx)
				stmts, err := parser.Parse(sql)
				if err != nil {
					t.Errorf("parse: %v", err)
					done <- nil
					return
				}
				node, err := optimizer.Plan(stmts[0], ctx.Catalog)
				if err != nil {
					t.Errorf("plan: %v", err)
					done <- nil
					return
				}
				gathered := optimizer.MaybeAddGather(node, optimizer.ParallelSettings{
					MaxWorkersPerGather: 4,
					MinTableScanBlocks:  1,
					DebugParallelQuery:  "on",
					BlocksForTable:      func(*catalog.Table) (int64, bool) { return 4096, true },
				})
				if !planTreeHasParallelNode(gathered) {
					t.Logf("planner declined to parallelise; arm asserts nothing")
					done <- nil
					return
				}
				ctx.MaxParallelWorkers = 8
				ctx.ParallelLeaderParticipation = true
				ctx.WorkMem = 64 << 10
				op, err := Build(gathered)
				if err != nil {
					t.Errorf("build: %v", err)
					done <- nil
					return
				}
				if err := op.Open(ctx); err != nil {
					t.Errorf("open: %v", err)
					done <- nil
					return
				}
				var sb *sharedHashBuild
				for _, b := range ctx.SharedHashBuilds {
					sb = b
				}
				for {
					_, err := op.Next()
					if err != nil {
						break // EOF (LIMIT) or 57014 — both end the query
					}
				}
				if err := op.Close(); err != nil {
					t.Errorf("close: %v", err)
				}
				done <- sb
			}()
			var sb *sharedHashBuild
			select {
			case sb = <-done:
			case <-time.After(60 * time.Second):
				t.Fatalf("the Gather hung after %s", arm.name)
			}
			ctx.Ctx = nil
			if sb == nil {
				return
			}
			if arm.limit > 3 && fired == 0 {
				t.Fatalf("no participant reached batch 1, so nothing was cancelled mid-batch")
			}
			if sb.batches == nil || sb.batches.nbatch <= 1 {
				t.Fatalf("precondition: the shared build did not batch")
			}
			if ctx.SharedHashBuilds != nil {
				t.Fatalf("Close left the publication in place")
			}
			for _, f := range sb.batches.inner {
				if f != nil {
					if _, err := os.Stat(f.path); err == nil {
						t.Fatalf("shared inner file %s survived Close", f.path)
					}
				}
			}
			if left := ctx.ReleaseSpillFiles(); left != 0 {
				t.Fatalf("%d spill file(s) leaked to the statement-end backstop", left)
			}
		})
	}
}
