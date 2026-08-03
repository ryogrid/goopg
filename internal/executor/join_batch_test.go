package executor

// M0127-P3.2 — hybrid hash join batch spill (design leftdeep-joins/06 §2.2-2.4).
//
// The property under test is an IDENTITY: a join that spills must return
// exactly what the same join returns when everything fits in memory. That is
// the only assertion strong enough for this mechanism, because every way it can
// break is a wrong ANSWER rather than an error — a row routed to a batch its
// matching partner does not land in is simply never emitted, and a row-count
// test on a self-consistent data set can miss it.
//
// So each test below runs the same inputs twice, once unbounded and once under
// a work_mem small enough to force batching, and compares the two multisets.
// The fixtures then assert that batching really engaged (an identity that holds
// because nothing spilled proves nothing).

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/planner"
)

func batchSchema(prefix string, n int) planner.Schema {
	s := make(planner.Schema, 0, n)
	for i := 0; i < n; i++ {
		s = append(s, planner.SchemaColumn{Name: fmt.Sprintf("%s%d", prefix, i)})
	}
	return s
}

// batchJoinPlan is a plain single-key INNER hash join: probe on the left,
// build on the right (the plan-shape contract's default orientation).
// estRows drives planner.EstimateRows, which is what chooses the geometry —
// keeping it separate from the row slices is deliberate, since an estimate
// that disagrees with reality is exactly what makes nbatch grow mid-build.
func batchJoinPlan(leftWidth, estLeft, estRight int) *planner.Join {
	col := func(idx int) *planner.ColumnRef {
		return &planner.ColumnRef{Index: idx, Type: catalog.Type{Name: "int4"}}
	}
	return &planner.Join{
		Type:     planner.JoinTypeInner,
		Algo:     planner.JoinAlgoHash,
		LeftKey:  col(0),
		RightKey: col(leftWidth),
		Left:     valuesNode(estLeft),
		Right:    valuesNode(estRight),
	}
}

// runBatchJoin opens a joinOp over the given rows under workMem and returns
// the emitted tuples rendered as strings, plus the batch state (nil when the
// join never batched).
func runBatchJoin(t *testing.T, plan *planner.Join, probeRows, buildRows []Row, lw, rw int, workMem int64) ([]string, *hashBatchState) {
	t.Helper()
	left := &rowsOp{rows: probeRows, schema: batchSchema("l", lw)}
	right := &rowsOp{rows: buildRows, schema: batchSchema("r", rw)}
	o := newJoinOp(plan, left, right)
	ctx := &Context{WorkMem: workMem}
	if err := o.Open(ctx); err != nil {
		t.Fatalf("open join: %v", err)
	}
	var out []string
	for {
		slot, err := o.Next()
		if err == EOF {
			break
		}
		if err != nil {
			t.Fatalf("join Next: %v", err)
		}
		parts := make([]string, slot.Width())
		for i := range parts {
			parts[i] = fmt.Sprint(datumToString(slot.Get(i)))
		}
		out = append(out, strings.Join(parts, "|"))
	}
	bs := o.batches
	sort.Strings(out)
	if err := o.Close(); err != nil {
		t.Fatalf("close join: %v", err)
	}
	return out, bs
}

// intKeyRows builds `n` rows of [key, payload] where the key repeats every
// `distinct` rows, so every probe row has a predictable number of partners.
func intKeyRows(n, distinct int, payload string) []Row {
	rows := make([]Row, n)
	for i := range rows {
		rows[i] = Row{
			NewIntDatum(int64(i % distinct)),
			NewStringDatum(fmt.Sprintf("%s%d-%s", payload, i, strings.Repeat("x", 40))),
		}
	}
	return rows
}

// A work_mem small enough that the build cannot fit, over a build whose row
// estimate is honest: the geometry comes out multi-batch before the first row
// arrives, and the join runs entirely through the batch machinery.
func TestHashJoinSpillIsIdentityToMemoryJoin(t *testing.T) {
	const lw, rw = 2, 2
	buildRows := intKeyRows(4000, 700, "b")
	probeRows := intKeyRows(3000, 700, "p")
	plan := batchJoinPlan(lw, len(probeRows), len(buildRows))

	want, memBS := runBatchJoin(t, plan, probeRows, buildRows, lw, rw, 0)
	if memBS != nil && memBS.innerSpilled != 0 {
		t.Fatalf("precondition: the 512 MiB-default arm spilled %d rows", memBS.innerSpilled)
	}
	if len(want) == 0 {
		t.Fatalf("precondition: the in-memory join emitted nothing")
	}

	got, bs := runBatchJoin(t, plan, probeRows, buildRows, lw, rw, 512<<10)
	if bs == nil {
		t.Fatalf("precondition: the bounded arm did not batch — work_mem is not being honoured")
	}
	if bs.nbatch < 2 {
		t.Fatalf("precondition: nbatch=%d, wanted a multi-batch build", bs.nbatch)
	}
	if bs.innerSpilled == 0 || bs.outerSpilled == 0 {
		t.Fatalf("precondition: nothing reached a batch file (inner=%d outer=%d)",
			bs.innerSpilled, bs.outerSpilled)
	}
	assertSameRows(t, want, got)
}

// The estimate says the build fits in two batches; the real build is 8x that.
// nbatch has to grow in the middle of a build whose earlier rows are already
// on disk, which is only sound because doubling moves rows forward — this test
// is what pins that.
func TestHashJoinBatchGrowthMidBuildKeepsEveryMatch(t *testing.T) {
	const lw, rw = 2, 2
	buildRows := intKeyRows(8000, 1500, "b")
	probeRows := intKeyRows(4000, 1500, "p")
	// Estimate only a tenth of the build: the geometry starts small and the
	// real data forces increaseNumBatches.
	plan := batchJoinPlan(lw, len(probeRows), len(buildRows)/10)

	want, _ := runBatchJoin(t, plan, probeRows, buildRows, lw, rw, 0)
	got, bs := runBatchJoin(t, plan, probeRows, buildRows, lw, rw, 256<<10)
	if bs == nil {
		t.Fatalf("precondition: the bounded arm did not batch")
	}
	if bs.nbatch <= bs.origNBatch {
		t.Fatalf("precondition: nbatch never grew (nbatch=%d, original=%d) — "+
			"the mid-build growth path was not exercised", bs.nbatch, bs.origNBatch)
	}
	assertSameRows(t, want, got)
}

// Every build row shares one key. Doubling cannot subdivide a single hash
// value, so PG disables growth and lets the current batch exceed the budget
// (nodeHash.c:1182-1184). goopg must do the same rather than double until it
// runs out of batches — and must still return every row.
func TestHashJoinSkewFreezesGrowthAndStaysCorrect(t *testing.T) {
	const lw, rw = 2, 2
	buildRows := make([]Row, 6000)
	for i := range buildRows {
		buildRows[i] = Row{NewIntDatum(7), NewStringDatum(fmt.Sprintf("b%d-%s", i, strings.Repeat("x", 60)))}
	}
	probeRows := []Row{
		{NewIntDatum(7), NewStringDatum("p-hit")},
		{NewIntDatum(8), NewStringDatum("p-miss")},
	}
	plan := batchJoinPlan(lw, len(probeRows), len(buildRows))

	want, _ := runBatchJoin(t, plan, probeRows, buildRows, lw, rw, 0)
	got, bs := runBatchJoin(t, plan, probeRows, buildRows, lw, rw, 256<<10)
	if bs == nil {
		t.Fatalf("precondition: the bounded arm did not batch")
	}
	if bs.growEnabled {
		t.Errorf("growth stayed enabled on a single-hash-value build (nbatch=%d)", bs.nbatch)
	}
	if bs.nbatch > maxJoinBatches {
		t.Errorf("nbatch %d exceeded the cap %d", bs.nbatch, maxJoinBatches)
	}
	if len(want) != len(buildRows) {
		t.Fatalf("precondition: in-memory arm emitted %d rows, want %d", len(want), len(buildRows))
	}
	assertSameRows(t, want, got)
}

// The build side is the LEFT input (BuildLeft), so a reloaded row's key has to
// be re-evaluated against the merged key slot in the OTHER orientation. Getting
// that backwards reads the wrong column and silently drops every match.
func TestHashJoinSpillWithBuildLeftOrientation(t *testing.T) {
	const lw, rw = 2, 2
	buildRows := intKeyRows(4000, 600, "b")
	probeRows := intKeyRows(2000, 600, "p")
	plan := batchJoinPlan(lw, len(probeRows), len(buildRows))
	plan.BuildLeft = true
	// With BuildLeft the LEFT input is the build side and the right is probed.
	want, _ := runBatchJoin(t, plan, buildRows, probeRows, lw, rw, 0)
	got, bs := runBatchJoin(t, plan, buildRows, probeRows, lw, rw, 512<<10)
	if bs == nil {
		t.Fatalf("precondition: the bounded arm did not batch")
	}
	assertSameRows(t, want, got)
}

// A string-keyed build takes the map[string] lane, whose canonical key IS the
// bytes joinBatchHash hashes. Pinning it separately matters because the two
// lanes reach the router by different routes.
func TestHashJoinSpillOnStringKeys(t *testing.T) {
	const lw, rw = 2, 2
	mk := func(n int, tag string) []Row {
		rows := make([]Row, n)
		for i := range rows {
			rows[i] = Row{
				NewStringDatum(fmt.Sprintf("k%04d", i%800)),
				NewStringDatum(fmt.Sprintf("%s%d-%s", tag, i, strings.Repeat("y", 40))),
			}
		}
		return rows
	}
	buildRows, probeRows := mk(4000, "b"), mk(2500, "p")
	plan := batchJoinPlan(lw, len(probeRows), len(buildRows))
	plan.LeftKey.(*planner.ColumnRef).Type = catalog.Type{Name: "text"}
	plan.RightKey.(*planner.ColumnRef).Type = catalog.Type{Name: "text"}

	want, _ := runBatchJoin(t, plan, probeRows, buildRows, lw, rw, 0)
	got, bs := runBatchJoin(t, plan, probeRows, buildRows, lw, rw, 512<<10)
	if bs == nil {
		t.Fatalf("precondition: the bounded arm did not batch")
	}
	if o := len(got); o == 0 {
		t.Fatalf("string-key spill emitted nothing")
	}
	assertSameRows(t, want, got)
}

// Temp files are the one resource a spilling join can leak past its own
// lifetime. P3.3 makes the guarantee unconditional with a per-query registry;
// what this pins is the operator's own half — a normally-drained, Closed join
// leaves nothing behind.
func TestHashJoinSpillFilesAreRemoved(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	const lw, rw = 2, 2
	buildRows := intKeyRows(4000, 700, "b")
	probeRows := intKeyRows(2000, 700, "p")
	plan := batchJoinPlan(lw, len(probeRows), len(buildRows))
	_, bs := runBatchJoin(t, plan, probeRows, buildRows, lw, rw, 512<<10)
	if bs == nil {
		t.Fatalf("precondition: the join did not batch, so no file was created")
	}
	left, err := filepath.Glob(filepath.Join(dir, "goopg-spill-*"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(left) != 0 {
		t.Fatalf("%d spill file(s) survived the join: %v", len(left), left)
	}
}

// An abandoned join — Closed before its probe stream drains — must not leave
// its batch files behind either. This is the cancellation / LIMIT shape.
func TestHashJoinSpillFilesRemovedOnEarlyClose(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	const lw, rw = 2, 2
	plan := batchJoinPlan(lw, 2000, 4000)
	left := &rowsOp{rows: intKeyRows(2000, 700, "p"), schema: batchSchema("l", lw)}
	right := &rowsOp{rows: intKeyRows(4000, 700, "b"), schema: batchSchema("r", rw)}
	o := newJoinOp(plan, left, right)
	if err := o.Open(&Context{WorkMem: 512 << 10}); err != nil {
		t.Fatalf("open: %v", err)
	}
	if o.batches == nil {
		t.Fatalf("precondition: the join did not batch")
	}
	if _, err := o.Next(); err != nil {
		t.Fatalf("first Next: %v", err)
	}
	if err := o.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	leftover, err := filepath.Glob(filepath.Join(dir, "goopg-spill-*"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(leftover) != 0 {
		t.Fatalf("%d spill file(s) survived an early Close: %v", len(leftover), leftover)
	}
	_ = os.Getenv("TMPDIR")
}

// Join shapes the batching still does not cover must not batch at all — a
// half-implemented fill rule drops rows, which no row count on the INNER tests
// would ever notice.
func TestBatchingDeclinesShapesItDoesNotYetSupport(t *testing.T) {
	const lw, rw = 2, 2
	buildRows := intKeyRows(4000, 700, "b")
	probeRows := intKeyRows(2000, 700, "p")
	for _, tc := range []struct {
		name  string
		mutet func(*planner.Join)
	}{
		// The build-left outer join used to be declined here: it fills from
		// the BUILD side, which needs the post-replay unmatched sweep.
		// M0127-P4.2 landed that sweep, so the shape now batches like any
		// other — TestHashOuterFillSweepsEveryBatch is its per-batch identity
		// test and TestBuildOnlyBatchIsNotSkippedWhenBuildFills its skip rule.
		{"composite key", func(p *planner.Join) {
			col := func(i int) *planner.ColumnRef {
				return &planner.ColumnRef{Index: i, Type: catalog.Type{Name: "int4"}}
			}
			p.HashKeys = []planner.JoinKeyPair{
				{Left: col(0), Right: col(lw)},
				{Left: col(1), Right: col(lw + 1)},
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan := batchJoinPlan(lw, len(probeRows), len(buildRows))
			tc.mutet(plan)
			_, bs := runBatchJoin(t, plan, probeRows, buildRows, lw, rw, 512<<10)
			if bs != nil {
				t.Fatalf("%s installed batch state (nbatch=%d) — its per-batch semantics are a later slice",
					tc.name, bs.nbatch)
			}
		})
	}
}

// M0127-P3.4 — the fill arm of PG's batch-skip rule 1 (06 §2.3).
//
// The fixture is built so exactly ONE batch can ever hold an inner row: the
// build side carries a single distinct key, while the geometry is sized from an
// estimate 5,000× larger, so nbatch comes out big from the ESTIMATE and no
// growth ever fires. Every other batch is outer-only.
//
// Under INNER and SEMI those batches produce nothing and are dropped unread.
// Under LEFT and ANTI every probe row in them EMITS — null-padded or as-is —
// so skipping them is a silent loss of ~all the join's output. The assertion is
// the identity against the unbounded arm, which is what makes the difference
// between the two groups visible at all: a row count on the INNER case would
// look perfect either way.
func TestFillingJoinsKeepOuterOnlyBatches(t *testing.T) {
	const lw, rw = 2, 2
	buildRows := intKeyRows(40, 1, "b")      // every build row keys on 0
	probeRows := intKeyRows(1200, 1200, "p") // 1200 distinct probe keys

	for _, tc := range []struct {
		name string
		typ  planner.JoinType
	}{
		{"inner", planner.JoinTypeInner},
		{"semi", planner.JoinTypeSemi},
		{"left", planner.JoinTypeLeft},
		{"anti", planner.JoinTypeAnti},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan := batchJoinPlan(lw, len(probeRows), 200000)
			plan.Type = tc.typ

			want, memBS := runBatchJoin(t, plan, probeRows, buildRows, lw, rw, 0)
			if memBS != nil && memBS.nbatch != 1 {
				t.Fatalf("precondition: the default-work_mem arm batched (nbatch=%d)", memBS.nbatch)
			}
			if len(want) == 0 {
				t.Fatalf("precondition: the in-memory %s join emitted nothing", tc.name)
			}

			got, bs := runBatchJoin(t, plan, probeRows, buildRows, lw, rw, 512<<10)
			if bs == nil {
				t.Fatalf("%s join did not batch — work_mem is not being honoured", tc.name)
			}
			if bs.nbatch < 8 {
				t.Fatalf("precondition: nbatch=%d, too few batches to leave any outer-only", bs.nbatch)
			}
			if bs.outerSpilled == 0 {
				t.Fatalf("precondition: no probe row reached a batch file")
			}
			assertSameRows(t, want, got)
		})
	}
}

// The three skip rules stated as a table. The identity tests above prove the
// mechanism end to end but cannot isolate WHICH rule fired; this pins each arm
// directly, including the two (rules 2 and 3) that only matter after a doubling
// and are therefore invisible in any fixture whose estimate was right.
func TestBatchSkipRulesRespectFillAndReassignment(t *testing.T) {
	skippable := func(typ planner.JoinType, buildLeft, hasInner, hasOuter bool, nbatch, orig, outstart int) bool {
		o := &joinOp{plan: &planner.Join{Type: typ, Algo: planner.JoinAlgoHash, BuildLeft: buildLeft}}
		bs := &hashBatchState{
			nbatch: nbatch, origNBatch: orig, nbatchOutstart: outstart,
			inner: make([]*joinBatchFile, nbatch), outer: make([]*joinBatchFile, nbatch),
		}
		if hasInner {
			bs.inner[1] = &joinBatchFile{}
		}
		if hasOuter {
			bs.outer[1] = &joinBatchFile{}
		}
		return bs.batchSkippable(o, 1)
	}
	cases := []struct {
		name                          string
		typ                           planner.JoinType
		buildLeft, hasInner, hasOuter bool
		nbatch, orig, outstart        int
		want                          bool
	}{
		{"both sides present", planner.JoinTypeInner, false, true, true, 4, 4, 4, false},
		{"empty batch", planner.JoinTypeInner, false, false, false, 4, 4, 4, true},
		{"inner: outer-only is dead", planner.JoinTypeInner, false, false, true, 4, 4, 4, true},
		{"semi: outer-only is dead", planner.JoinTypeSemi, false, false, true, 4, 4, 4, true},
		{"left: outer-only fills", planner.JoinTypeLeft, false, false, true, 4, 4, 4, false},
		{"anti: outer-only fills", planner.JoinTypeAnti, false, false, true, 4, 4, 4, false},
		{"left: inner-only needs no sweep yet", planner.JoinTypeLeft, false, true, false, 4, 4, 4, true},
		{"rule 2: inner file predates a doubling", planner.JoinTypeInner, false, true, false, 8, 4, 8, false},
		{"rule 3: outer file predates a doubling", planner.JoinTypeInner, false, false, true, 8, 8, 4, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := skippable(c.typ, c.buildLeft, c.hasInner, c.hasOuter, c.nbatch, c.orig, c.outstart)
			if got != c.want {
				t.Fatalf("batchSkippable = %v, want %v", got, c.want)
			}
		})
	}
}

// M0127-P3.4 — a shared (parallel) build that would spill declines the SHARE,
// not the SPILL.
//
// captureSharedBuild freezes the in-memory table alone; the batch files and the
// per-batch replay stay on the leader's operator, which no worker ever runs. So
// publishing a spilled build would hand every worker one partition of the table
// and lose the rest — silently. P3.2 sidestepped it by disabling the spill,
// which left the shared build the one hash build in the executor with no
// work_mem bound at all.
//
// Three arms, because the decision is made twice: from the estimate before the
// build (so the common case wastes no pass) and from the measurement after it
// (because goopg's estimates are absent often enough that only growth bounds
// anything).
func TestSharedHashBuildDeclinesWhenItWouldSpill(t *testing.T) {
	const lw, rw = 2, 2
	buildRows := intKeyRows(4000, 700, "b")
	probeRows := intKeyRows(2000, 700, "p")

	prebuild := func(t *testing.T, estRight int, workMem int64) map[*planner.Join]*sharedHashBuild {
		t.Helper()
		plan := batchJoinPlan(lw, len(probeRows), estRight)
		tree := newJoinOp(plan,
			&rowsOp{rows: probeRows, schema: batchSchema("l", lw)},
			&rowsOp{rows: buildRows, schema: batchSchema("r", rw)})
		t.Cleanup(func() { _ = tree.Close() })
		out, err := prebuildSharedHashJoins(&Context{WorkMem: workMem}, plan,
			func() (Operator, error) { return tree, nil })
		if err != nil {
			t.Fatalf("prebuild: %v", err)
		}
		return out
	}

	t.Run("a build that fits is shared", func(t *testing.T) {
		if n := len(prebuild(t, len(buildRows), 0)); n != 1 {
			t.Fatalf("published %d builds, want 1 — the sharing regressed", n)
		}
	})
	t.Run("an estimate that says spill declines before building", func(t *testing.T) {
		if n := len(prebuild(t, 200000, 512<<10)); n != 0 {
			t.Fatalf("published %d builds for a build sized at 200k rows under 512 KiB", n)
		}
	})
	t.Run("an estimate that lied declines after building", func(t *testing.T) {
		// The estimate says a hundred rows; four thousand arrive. Growth is
		// what discovers it, so only the post-build check can catch this one.
		if n := len(prebuild(t, 100, 256<<10)); n != 0 {
			t.Fatalf("published %d builds after the build outgrew work_mem", n)
		}
	})
}

// The batch hash must be a function of the key's VALUE, not of the datum kind
// that happens to carry it. The int64 fast lane and the string lane can hold
// the same value (and a build can fall from one to the other mid-run via
// demoteIntHash), so two equal keys hashing differently would send matching
// rows to different batches — a lost match with no error anywhere.
func TestJoinBatchHashFollowsTheCanonicalKey(t *testing.T) {
	cases := []struct {
		name string
		d    Datum
	}{
		{"int", NewIntDatum(1230)},
		{"numeric scale 0", NewNumericInt64Datum(1230, 0)},
		{"numeric trailing zeros", NewNumericInt64Datum(123000, 2)},
	}
	want := joinBatchHash(cases[0].d)
	for _, c := range cases {
		if got := joinBatchHash(c.d); got != want {
			t.Errorf("%s: joinBatchHash = %#x, want %#x — equal keys route to different batches",
				c.name, got, want)
		}
		if got := hashKeyString(datumKey(c.d)); got != want {
			t.Errorf("%s: hashing the canonical key gives %#x, joinBatchHash gives %#x",
				c.name, got, want)
		}
	}
	if joinBatchHash(NewStringDatum("1230")) == want {
		t.Errorf("a text '1230' hashed the same as the integer 1230 — the key spaces are not distinct")
	}
}

// The hashed spill frame is the format the whole scheme rides on. A row must
// come back byte-identical alongside its hash, and the hashed and unhashed
// framings must stay distinguishable (they share writeFrame).
func TestSpillHashedFrameRoundTrips(t *testing.T) {
	w, err := newSpillWriterInDir(t.TempDir())
	if err != nil {
		t.Fatalf("newSpillWriter: %v", err)
	}
	rows := []Row{
		{NewIntDatum(42), NewStringDatum("forty-two")},
		{Datum{Kind: KindNull}, NewStringDatum("")},
		{NewIntDatum(-1), NewStringDatum(strings.Repeat("z", 300))},
	}
	hashes := []uint32{0, 0xdeadbeef, 1}
	for i, r := range rows {
		if err := w.WriteRowHashed(hashes[i], r); err != nil {
			t.Fatalf("WriteRowHashed: %v", err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	r, err := newSpillReader(w.Path())
	if err != nil {
		t.Fatalf("newSpillReader: %v", err)
	}
	defer r.Close()
	var buf Row
	for i := range rows {
		h, got, err := r.ReadRowHashedInto(buf)
		if err != nil {
			t.Fatalf("row %d: %v", i, err)
		}
		buf = got
		if h != hashes[i] {
			t.Errorf("row %d: hash %#x, want %#x", i, h, hashes[i])
		}
		if len(got) != len(rows[i]) {
			t.Fatalf("row %d: width %d, want %d", i, len(got), len(rows[i]))
		}
		for j := range got {
			if datumToString(got[j]) != datumToString(rows[i][j]) {
				t.Errorf("row %d col %d: %v, want %v", i, j, datumToString(got[j]), datumToString(rows[i][j]))
			}
		}
	}
}

func assertSameRows(t *testing.T, want, got []string) {
	t.Helper()
	if len(want) != len(got) {
		t.Fatalf("spilled join emitted %d rows, in-memory join emitted %d", len(got), len(want))
	}
	for i := range want {
		if want[i] != got[i] {
			t.Fatalf("row %d differs:\n spilled: %s\n  memory: %s", i, got[i], want[i])
		}
	}
}
