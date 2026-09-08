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
//   - no participant opens an inner file twice.
//
// E-09b restated the third invariant rather than deleting it. Under Variant A
// it read "every participant opens each inner file it needs exactly once",
// because every participant reloaded every batch privately — which is the
// 5x memory multiplier E-09b exists to remove. It now reads: every open is a
// CLAIMED LOAD (opens == descriptor loadCount), no participant opens a batch
// twice, and no batch is loaded more times than there are participants (the
// straggler re-load of DESIGN-E09b.md §4 is bounded by Variant A's own count).
// The load-once accounting and the memory figures it produces have their own
// tests below: the protocol tests drive acquire/wait/release directly, and
// TestSharedSpillingBuildLoadsOncePerBatch holds every participant at the same
// batch so "one live table where there were four" is an assertion, not an
// observation.
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
	"sync/atomic"
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
	mu sync.Mutex
	// opens is per (participant, batch); perBatch aggregates it over
	// participants, which is the figure E-09b's load-once rule bounds.
	opens    map[*hashBatchState]map[int]int
	perBatch map[int]int
	total    int
	paths    map[string]bool
	onOpen   func(b int)
}

func (c *innerOpenCounter) install() {
	c.opens = make(map[*hashBatchState]map[int]int)
	c.perBatch = make(map[int]int)
	c.total = 0
	c.paths = make(map[string]bool)
	testHookInnerBatchOpened = func(bs *hashBatchState, b int, path string) {
		c.mu.Lock()
		if c.opens[bs] == nil {
			c.opens[bs] = make(map[int]int)
		}
		c.opens[bs][b]++
		c.perBatch[b]++
		c.total++
		c.paths[path] = true
		fn := c.onOpen
		c.mu.Unlock()
		if fn != nil {
			fn(b)
		}
	}
}

func (c *innerOpenCounter) uninstall() { testHookInnerBatchOpened = nil }

// assertLoadInvariants is E-09a's per-participant invariant set plus E-09b's
// load-once accounting.
//
// E-09a, unchanged: every opener is a shared, growth-frozen participant state;
// no (participant, batch) pair is opened twice; nothing is opened that the
// descriptor does not carry.
//
// E-09b: every open is a CLAIMED LOAD, so the total number of opens equals the
// descriptor's loadCount — an open that was not a claimed load would be a
// private reload sneaking back in. And no batch is loaded more often than
// there are participants, which is the bound on the straggler re-load of
// DESIGN-E09b.md §4: the worst case is exactly Variant A's load count.
//
// It deliberately does NOT assert a lower bound tighter than "something was
// loaded": which participant wins a batch is a race by design.
// TestSharedSpillingBuildLoadsOncePerBatch is where the exact figures live.
func (c *innerOpenCounter) assertLoadInvariants(t *testing.T, d *sharedBatchDesc, participants int) {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.total == 0 {
		t.Fatalf("no batch was ever loaded, so the gate asserted nothing")
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
	}
	d.mu.Lock()
	loadCount, maxLive, maxBytes := d.loadCount, d.maxLiveLoads, d.maxLiveBytes
	live := d.liveLoads
	d.mu.Unlock()
	if loadCount != c.total {
		t.Fatalf("descriptor counted %d loads but %d inner files were opened — "+
			"an open that was not a claimed load is a private reload", loadCount, c.total)
	}
	for b, n := range c.perBatch {
		if n > participants {
			t.Fatalf("batch %d was loaded %d times with %d participants", b, n, participants)
		}
	}
	if maxLive > participants {
		t.Fatalf("%d batch tables were resident at once with %d participants", maxLive, participants)
	}
	if live != 0 {
		t.Fatalf("%d batch table(s) still resident after every participant finished", live)
	}
	// The measured artifact: Variant A's figures on the same shape would be
	// loadCount = participants * batches and maxLiveLoads = participants.
	t.Logf("E-09b: %d distinct batches, loadCount=%d (Variant A would be %d), "+
		"maxLiveLoads=%d (Variant A would be %d), maxLiveBytes=%d",
		len(c.perBatch), loadCount, len(c.perBatch)*participants, maxLive, participants, maxBytes)
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
				counter.assertLoadInvariants(t, d, 4)
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
			h, _, row, err := r.ReadRowKeyedInto(buf)
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
	err := bs.writeInner(2, 0, spillIntKey(1), Row{NewIntDatum(1)})
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
					counter.assertLoadInvariants(t, d, workers+1)
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
// cancelled while a participant is in batch 1 — no hang, files released, and
// (E-09b) no load reference left behind.
//
// The third arm is E-09b's integration half of the mandatory cancel-mid-batch
// test: the loader is parked INSIDE a batch load until a peer is provably
// blocked waiting on it, and only then is the statement cancelled. The
// protocol half, where the exact error and the exact refcount are asserted, is
// TestSharedBatchLoadCancelsWaiters.
func TestParallelHashJoinSpillingSharedBuildCancel(t *testing.T) {
	ctx, cleanup := pqSpillFixture(t)
	defer cleanup()
	for _, arm := range []struct {
		name        string
		limit       int
		blockLoader bool
	}{
		// LIMIT satisfied out of batch 0: Close cancels workers that are
		// still reloading later batches.
		{"limit-satisfied", 3, false},
		// LIMIT larger than the result, so the query reaches batch 1; the
		// statement is cancelled from inside the first batch-1 reload.
		{"statement-cancelled-mid-batch-1", 1000000, false},
		// Same, but cancelled only once another participant is parked on the
		// load in flight — the wait this item introduces.
		{"cancelled-with-a-waiter-parked", 1000000, true},
	} {
		t.Run(arm.name, func(t *testing.T) {
			sql := fmt.Sprintf("SELECT f.fid, d.dname FROM ps_fact f JOIN ps_dim d ON f.fk = d.dk LIMIT %d", arm.limit)
			cctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			ctx.Ctx = cctx
			counter := &innerOpenCounter{}
			var once sync.Once
			var fired int32
			var sawWaiter int32
			if arm.limit > 3 && !arm.blockLoader {
				counter.onOpen = func(b int) {
					if b >= 1 {
						once.Do(func() { fired = 1; cancel() })
					}
				}
			}
			if arm.blockLoader {
				testHookSharedBatchLoading = func(d *sharedBatchDesc, b int) {
					if b < 1 {
						return
					}
					once.Do(func() {
						atomic.StoreInt32(&fired, 1)
						// Park the loader until a peer is demonstrably
						// waiting on it, then cancel. The deadline keeps a
						// scheduling accident from hanging the gate; it
						// records that the strict arm did not fire rather
						// than pretending it did.
						deadline := time.Now().Add(20 * time.Second)
						for time.Now().Before(deadline) {
							d.mu.Lock()
							w := d.waiting
							d.mu.Unlock()
							if w >= 1 {
								atomic.StoreInt32(&sawWaiter, 1)
								break
							}
							time.Sleep(time.Millisecond)
						}
						cancel()
					})
				}
				defer func() { testHookSharedBatchLoading = nil }()
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
			if arm.limit > 3 && atomic.LoadInt32(&fired) == 0 {
				t.Fatalf("no participant reached batch 1, so nothing was cancelled mid-batch")
			}
			if arm.blockLoader && atomic.LoadInt32(&sawWaiter) == 0 {
				t.Logf("no peer parked on the load within the deadline; this arm " +
					"only asserted that the cancellation did not hang")
			}
			if sb.batches == nil || sb.batches.nbatch <= 1 {
				t.Fatalf("precondition: the shared build did not batch")
			}
			// E-09b: every reference is dropped on the cancellation path too,
			// so nothing is left holding a batch table.
			sb.batches.mu.Lock()
			liveLoads, liveBytes := sb.batches.liveLoads, sb.batches.liveBytes
			maxLive, loads := sb.batches.maxLiveLoads, sb.batches.loadCount
			sb.batches.mu.Unlock()
			if liveLoads != 0 || liveBytes != 0 {
				t.Fatalf("cancellation leaked %d batch table(s) / %d bytes", liveLoads, liveBytes)
			}
			t.Logf("E-09b: loadCount=%d maxLiveLoads=%d", loads, maxLive)
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

// ── E-09b: load-once-per-batch ──────────────────────────────────────────
//
// Design: DESIGN-E09b.md. Three things need pinning and they need different
// instruments:
//
//   - the PROTOCOL (acquire / wait / publish / release) is a concurrency
//     contract, so it is driven directly rather than through a join: a race
//     that resolves the wrong way in a join shows up as a wrong row count
//     days later, and a cancellation that leaks a reference shows up as
//     nothing at all;
//   - CANCELLATION is mandatory for this item and is tested at both levels —
//     waiters parked on a load in flight, and the real Gather under a LIMIT;
//   - the MEMORY claim is an object count, so it is asserted as one, with
//     every participant held at the same batch so "one live table where there
//     were four" is deterministic rather than a timing observation.

// sharedLoadTestDesc is a descriptor with no files: enough for the protocol,
// which never touches one.
func sharedLoadTestDesc(nbatch int) *sharedBatchDesc {
	return &sharedBatchDesc{
		nbatch: nbatch, origNBatch: nbatch, bucketBits: 10,
		spaceAllowed: 1 << 20,
		inner:        make([]*joinBatchFile, nbatch),
	}
}

// runFakeLoad mirrors runSharedLoad's publish discipline exactly — pessimistic
// error, payload, publish from a defer — over a payload the test supplies, so
// the protocol can be exercised without a file or an operator. runSharedLoad
// itself is under test through the operator harnesses further down (including
// its panic path).
func runFakeLoad(d *sharedBatchDesc, ld *sharedBatchLoad, gate <-chan struct{}, rows map[int64][]Row, fail error) {
	ld.err = errSharedBatchAbandoned
	defer d.publishLoad(ld)
	if gate != nil {
		<-gate
	}
	if fail != nil {
		ld.err = fail
		return
	}
	ld.intHash = rows
	ld.hashIsInt = true
	ld.spaceUsed = 4096
	ld.err = nil
}

// waitForWaiting blocks until n participants are parked on a load, which is
// what makes "the waiter really was blocked when we cancelled" an assertion
// rather than a hope.
func waitForWaiting(t *testing.T, d *sharedBatchDesc, n int) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		d.mu.Lock()
		got := d.waiting
		d.mu.Unlock()
		if got >= n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("only %d participant(s) parked on the load, want %d", got, n)
		}
		time.Sleep(time.Millisecond)
	}
}

// TestSharedBatchLoadProtocol: N participants race for one batch. Exactly one
// loads, every other one adopts the SAME maps, and the last one out frees
// them. This is the whole of E-09b's memory claim in one assertion —
// loadCount 1 and maxLiveLoads 1 where Variant A had N of each.
func TestSharedBatchLoadProtocol(t *testing.T) {
	const n = 5
	d := sharedLoadTestDesc(4)
	rows := map[int64][]Row{7: {Row{NewIntDatum(7)}}}
	gate := make(chan struct{})
	start := make(chan struct{})
	var loaders int32
	held := make([]*sharedBatchLoad, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			bs := &hashBatchState{ctx: &Context{}, desc: d}
			ld, mine := d.acquireLoad(1)
			held[i] = ld
			if mine {
				atomic.AddInt32(&loaders, 1)
				runFakeLoad(d, ld, gate, rows, nil)
				return
			}
			errs[i] = bs.waitSharedLoad(d, ld)
		}(i)
	}
	close(start)
	waitForWaiting(t, d, n-1)
	// While the load is in flight the slot is already accounted as resident:
	// a table being built costs the same memory as one that is built.
	d.mu.Lock()
	if d.loadCount != 1 || d.liveLoads != 1 || d.maxLiveLoads != 1 {
		t.Errorf("in flight: loadCount=%d liveLoads=%d maxLiveLoads=%d, want 1/1/1",
			d.loadCount, d.liveLoads, d.maxLiveLoads)
	}
	if d.loads[1] == nil || d.loads[1].refs != n {
		t.Errorf("slot holds %v refs, want %d", d.loads[1], n)
	}
	d.mu.Unlock()
	close(gate)
	wg.Wait()

	if loaders != 1 {
		t.Fatalf("%d participants ran the load, want exactly 1", loaders)
	}
	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Fatalf("participant %d: %v", i, errs[i])
		}
		if held[i] != held[0] {
			t.Fatalf("participant %d adopted a different load than participant 0", i)
		}
		if held[i].err != nil {
			t.Fatalf("participant %d saw err %v", i, held[i].err)
		}
		if len(held[i].intHash) != 1 || len(held[i].intHash[7]) != 1 {
			t.Fatalf("participant %d adopted the wrong table: %v", i, held[i].intHash)
		}
	}
	d.mu.Lock()
	if d.maxLiveLoads != 1 || d.loadCount != 1 {
		t.Errorf("loaded: loadCount=%d maxLiveLoads=%d, want 1/1 "+
			"(Variant A would be %d/%d)", d.loadCount, d.maxLiveLoads, n, n)
	}
	if d.maxLiveBytes != 4096 {
		t.Errorf("maxLiveBytes=%d, want 4096 — one table, not %d", d.maxLiveBytes, n)
	}
	d.mu.Unlock()

	// Everyone leaves: the maps go immediately, not at statement end.
	for i := 0; i < n; i++ {
		d.releaseLoad(1, held[i])
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.loads[1] != nil {
		t.Errorf("the slot survived the last holder")
	}
	if d.liveLoads != 0 || d.liveBytes != 0 {
		t.Errorf("liveLoads=%d liveBytes=%d after the last holder left, want 0/0", d.liveLoads, d.liveBytes)
	}
	if held[0].intHash != nil || held[0].hash != nil || held[0].nullBuild != nil {
		t.Errorf("the freed load still holds its payload")
	}
}

// TestSharedBatchLoadCancelsWaiters is the mandatory cancel-mid-batch test at
// the protocol level: waiters parked on a load in flight, cancelled while the
// loader is STILL parked. They must return 57014 rather than wait for a batch
// nobody will probe — and every reference must come back.
func TestSharedBatchLoadCancelsWaiters(t *testing.T) {
	const n = 4
	d := sharedLoadTestDesc(4)
	cctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	gate := make(chan struct{})

	ld, mine := d.acquireLoad(2)
	if !mine {
		t.Fatalf("the first acquire did not claim the slot")
	}
	loaderDone := make(chan struct{})
	go func() {
		defer close(loaderDone)
		runFakeLoad(d, ld, gate, map[int64][]Row{1: {Row{NewIntDatum(1)}}}, nil)
	}()

	waiters := make([]*sharedBatchLoad, n-1)
	errs := make([]error, n-1)
	var wg sync.WaitGroup
	for i := 0; i < n-1; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			bs := &hashBatchState{ctx: &Context{Ctx: cctx}, desc: d}
			w, m := d.acquireLoad(2)
			waiters[i] = w
			if m {
				t.Errorf("waiter %d claimed the slot", i)
				return
			}
			errs[i] = bs.waitSharedLoad(d, w)
			// The reference is dropped on the cancellation path too, exactly
			// as loadSharedInnerBatch does.
			d.releaseLoad(2, w)
		}(i)
	}
	waitForWaiting(t, d, n-1)
	cancel()

	// The waiters must be gone WHILE the loader is still parked on the gate.
	joined := make(chan struct{})
	go func() { wg.Wait(); close(joined) }()
	select {
	case <-joined:
	case <-time.After(30 * time.Second):
		t.Fatalf("waiters did not return after cancellation — the wait is not ctx-aware")
	}
	select {
	case <-loaderDone:
		t.Fatalf("the loader finished; the waiters were not cancelled mid-load")
	default:
	}
	for i, err := range errs {
		var ee *ExecError
		if err == nil || !errorsAs(err, &ee) || ee.Code != "57014" {
			t.Fatalf("waiter %d returned %v, want SQLSTATE 57014", i, err)
		}
	}
	d.mu.Lock()
	if d.waiting != 0 {
		t.Errorf("waiting=%d after every waiter returned", d.waiting)
	}
	// The loader still holds its own reference, so the slot must still stand.
	if d.loads[2] != ld || ld.refs != 1 {
		t.Errorf("slot=%v refs=%d after the waiters left, want the loader's single ref", d.loads[2], ld.refs)
	}
	d.mu.Unlock()

	// Rule 1: the loader is unaffected by the cancellation and finishes.
	close(gate)
	<-loaderDone
	if ld.err != nil {
		t.Fatalf("the loader failed: %v", ld.err)
	}
	d.releaseLoad(2, ld)
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.loads[2] != nil || d.liveLoads != 0 || d.liveBytes != 0 {
		t.Fatalf("cancellation leaked a reference: slot=%v liveLoads=%d liveBytes=%d",
			d.loads[2], d.liveLoads, d.liveBytes)
	}
}

// TestSharedBatchLoadLateArrivalReloads pins the free rule of DESIGN-E09b.md
// §4: freeing on refs==0 CLEARS the slot, so a straggler that reaches the
// batch after everyone else has passed it re-loads from the still-linked file
// rather than adopting a table that has been dropped.
func TestSharedBatchLoadLateArrivalReloads(t *testing.T) {
	d := sharedLoadTestDesc(4)
	first, mine := d.acquireLoad(3)
	if !mine {
		t.Fatalf("the first acquire did not claim the slot")
	}
	runFakeLoad(d, first, nil, map[int64][]Row{1: {Row{NewIntDatum(1)}}}, nil)
	d.releaseLoad(3, first)

	second, mine := d.acquireLoad(3)
	if !mine {
		t.Fatalf("the late arrival adopted a freed load instead of re-loading it")
	}
	if second == first {
		t.Fatalf("the late arrival got the freed slot back")
	}
	runFakeLoad(d, second, nil, map[int64][]Row{2: {Row{NewIntDatum(2)}}}, nil)
	d.releaseLoad(3, second)
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.loadCount != 2 {
		t.Fatalf("loadCount=%d, want 2 — the re-load was not counted", d.loadCount)
	}
	// The bound the design claims: never worse than Variant A, which would
	// have loaded once per participant.
	if d.maxLiveLoads != 1 {
		t.Fatalf("maxLiveLoads=%d, want 1 — the two loads overlapped", d.maxLiveLoads)
	}
}

// TestSharedBatchLoadPublishesErrors: a load that fails hands every waiter the
// error. Nobody adopts an empty table, which is the silently-partial-join
// failure mode plain sync.Once would produce here.
func TestSharedBatchLoadPublishesErrors(t *testing.T) {
	d := sharedLoadTestDesc(4)
	boom := &ExecError{Code: "XX000", Message: "load failed"}
	ld, _ := d.acquireLoad(1)
	gate := make(chan struct{})
	go runFakeLoad(d, ld, gate, nil, boom)

	got := make(chan error, 1)
	go func() {
		bs := &hashBatchState{ctx: &Context{}, desc: d}
		w, mine := d.acquireLoad(1)
		if mine {
			got <- fmt.Errorf("the waiter claimed the slot")
			return
		}
		if err := bs.waitSharedLoad(d, w); err != nil {
			got <- err
			return
		}
		got <- w.err
	}()
	waitForWaiting(t, d, 1)
	close(gate)
	select {
	case err := <-got:
		if err != boom {
			t.Fatalf("the waiter saw %v, want the loader's error", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatalf("the waiter hung on a failed load")
	}
	// A failed load is never charged as resident memory.
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.liveBytes != 0 || d.maxLiveBytes != 0 {
		t.Fatalf("a failed load was charged %d bytes (peak %d)", d.liveBytes, d.maxLiveBytes)
	}
}

// sharedSpillDesc prebuilds one shared spilling build and returns the context,
// the plan and the descriptor. The caller owns the release.
func sharedSpillDesc(t *testing.T, workMem int64) (*Context, *optimizer.Join, *sharedHashBuild) {
	t.Helper()
	const lw, rw = 2, 2
	buildRows := spillRows(6000, 900, 0, "b")
	probeRows := spillRows(100, 900, 0, "p")
	plan := spillPlan(spillShapes()[0], lw, len(probeRows), len(buildRows))
	ctx := &Context{WorkMem: workMem, tempFiles: newTempFileRegistry()}
	tree := newJoinOp(plan,
		&rowsOp{rows: probeRows, schema: batchSchema("l", lw)},
		&rowsOp{rows: buildRows, schema: batchSchema("r", rw)})
	builds, err := prebuildSharedHashJoins(ctx, plan, func() (Operator, error) { return tree, nil })
	if err != nil {
		t.Fatalf("prebuild: %v", err)
	}
	ctx.SharedHashBuilds = builds
	sb := builds[plan]
	if sb == nil || sb.batches == nil || sb.batches.nbatch <= 1 {
		t.Fatalf("precondition: the shared build did not batch")
	}
	return ctx, plan, sb
}

// TestSharedBatchLoaderPanicPublishesError drives the REAL runSharedLoad over
// a real frozen file with a loader that panics inside it. The waiter must be
// handed an error, not a channel nobody will close — rule 2 of the
// cancellation protocol, and the reason this is a channel and not a
// sync.Once.
func TestSharedBatchLoaderPanicPublishesError(t *testing.T) {
	const lw, rw = 2, 2
	ctx, plan, sb := sharedSpillDesc(t, 128<<10)
	defer func() {
		releaseSharedHashBuilds(ctx)
		ctx.ReleaseSpillFiles()
	}()
	d := sb.batches
	batch := -1
	for b, f := range d.inner {
		if f != nil {
			batch = b
			break
		}
	}
	if batch < 0 {
		t.Fatalf("precondition: the descriptor carries no inner file")
	}

	gate := make(chan struct{})
	testHookSharedBatchLoading = func(_ *sharedBatchDesc, _ int) {
		<-gate
		panic("loader exploded mid-batch")
	}
	defer func() { testHookSharedBatchLoading = nil }()

	bs := newParticipantBatchState(ctx, plan, d)
	bs.curBatch = batch
	o := newJoinOp(plan,
		&rowsOp{rows: nil, schema: batchSchema("l", lw)},
		&poisonBuildOp{schema: batchSchema("r", rw)})
	ld, mine := d.acquireLoad(batch)
	if !mine {
		t.Fatalf("the first acquire did not claim the slot")
	}
	panicked := make(chan any, 1)
	go func() {
		defer func() { panicked <- recover() }()
		bs.runSharedLoad(o, ld, batch, d.inner[batch])
	}()

	waiterErr := make(chan error, 1)
	go func() {
		wbs := &hashBatchState{ctx: &Context{}, desc: d}
		w, m := d.acquireLoad(batch)
		if m {
			waiterErr <- fmt.Errorf("the waiter claimed the slot")
			return
		}
		if err := wbs.waitSharedLoad(d, w); err != nil {
			waiterErr <- err
			return
		}
		waiterErr <- w.err
		d.releaseLoad(batch, w)
	}()
	waitForWaiting(t, d, 1)
	close(gate)

	if p := <-panicked; p == nil {
		t.Fatalf("the loader did not panic, so nothing was proven")
	}
	select {
	case err := <-waiterErr:
		if err != errSharedBatchAbandoned {
			t.Fatalf("the waiter saw %v, want errSharedBatchAbandoned", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatalf("the waiter hung on a panicking loader — `done` was not closed")
	}
	d.releaseLoad(batch, ld)
}

// batchBarrier holds every participant at the same batch. It is what turns
// E-09b's memory claim into a deterministic assertion: released together, the
// participants cannot be staggered across batches, so exactly one table is
// resident at any moment and each batch is loaded exactly once.
type batchBarrier struct {
	t  *testing.T
	n  int
	mu sync.Mutex
	at map[int]int
	ch map[int]chan struct{}
}

func newBatchBarrier(t *testing.T, n int) *batchBarrier {
	return &batchBarrier{t: t, n: n, at: map[int]int{}, ch: map[int]chan struct{}{}}
}

func (b *batchBarrier) arrive(batch int) {
	b.mu.Lock()
	c := b.ch[batch]
	if c == nil {
		c = make(chan struct{})
		b.ch[batch] = c
	}
	b.at[batch]++
	full := b.at[batch] == b.n
	b.mu.Unlock()
	if full {
		close(c)
		return
	}
	select {
	case <-c:
	case <-time.After(60 * time.Second):
		b.mu.Lock()
		got := b.at[batch]
		b.mu.Unlock()
		b.t.Errorf("barrier for batch %d timed out with %d of %d arrivals", batch, got, b.n)
	}
}

// TestSharedSpillingBuildLoadsOncePerBatch is the memory gate. Every
// participant is held at the same batch, so the figures are exact: ONE live
// batch table where Variant A had four, and one load per batch where Variant
// A had four. The rows are still identity-checked against the serial join,
// because a shared table adopted wrongly is a silently partial join.
func TestSharedSpillingBuildLoadsOncePerBatch(t *testing.T) {
	const lw, rw, n = 2, 2, 4
	buildRows := spillRows(6000, 900, 0, "b")
	// Probe keys in RUNS of n, so each round-robin slice spillParticipants
	// hands out covers EVERY key. That makes "all four participants reach
	// every batch that has an inner file" a property of the data rather than
	// of the scheduler — which is what lets the barrier below always
	// complete, and what makes the exact figures meaningful.
	probeRows := make([]Row, 3600)
	for i := range probeRows {
		probeRows[i] = Row{
			NewIntDatum(int64((i / n) % 900)),
			NewStringDatum(fmt.Sprintf("p%d-%s", i, strings.Repeat("x", 40))),
		}
	}
	plan := spillPlan(spillShapes()[0], lw, len(probeRows), len(buildRows))
	want, _ := runBatchJoin(t, plan, probeRows, buildRows, lw, rw, unboundedWorkMem)
	if len(want) == 0 {
		t.Fatalf("precondition: the serial join returned nothing")
	}

	bar := newBatchBarrier(t, n)
	testHookSharedBatchAcquire = bar.arrive
	defer func() { testHookSharedBatchAcquire = nil }()

	counter := &innerOpenCounter{}
	got, sb := spillParticipants(t, plan, probeRows, buildRows, lw, rw, 128<<10, n, counter)
	d := sb.batches
	if d == nil || d.nbatch <= 1 {
		t.Fatalf("precondition: the shared build did not batch")
	}
	assertSameRows(t, want, got)

	files := 0
	for _, f := range d.inner {
		if f != nil {
			files++
		}
	}
	if files < 2 {
		t.Fatalf("precondition: only %d inner file(s); the gate asserts nothing", files)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.loadCount != files {
		t.Fatalf("loadCount=%d with %d inner files and %d participants — "+
			"want exactly %d (Variant A would be %d)", d.loadCount, files, n, files, files*n)
	}
	if d.maxLiveLoads != 1 {
		t.Fatalf("maxLiveLoads=%d, want 1 — the 5x multiplier is still there "+
			"(Variant A would be %d)", d.maxLiveLoads, n)
	}
	if d.liveLoads != 0 || d.liveBytes != 0 {
		t.Fatalf("liveLoads=%d liveBytes=%d after every participant finished", d.liveLoads, d.liveBytes)
	}
	if d.maxLiveBytes <= 0 {
		t.Fatalf("maxLiveBytes=%d — nothing was measured", d.maxLiveBytes)
	}
	t.Logf("E-09b memory: %d batches, loadCount=%d (Variant A: %d), "+
		"maxLiveLoads=%d (Variant A: %d), maxLiveBytes=%d (Variant A: ~%d)",
		files, d.loadCount, files*n, d.maxLiveLoads, n, d.maxLiveBytes, d.maxLiveBytes*int64(n))
}
