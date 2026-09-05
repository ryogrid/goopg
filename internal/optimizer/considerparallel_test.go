package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// C-19a / C-19b pins (take3 08 §8). Three properties, in the order the task
// statement lists them:
//
//	(a) consider_parallel is set / cleared for the shapes PG sets / clears it;
//	(b) a partial seq scan path exists on a large rel and its cost is priced
//	    by cost_seqscan's parallel arm (CPU / get_parallel_divisor);
//	(c) NO partial path is ever the final path — the serial control arm is
//	    unchanged (nothing consumes PartialPathlist until C-19d).

// withParallelOn runs f with the process kill switch on and the previous
// state restored, so the pins do not depend on GOOPG_PARALLEL in the
// environment.
func withParallelOn(t *testing.T, f func()) {
	t.Helper()
	prev := ParallelEnabled()
	SetParallelEnabled(true)
	defer SetParallelEnabled(prev)
	f()
}

func cpTable(name string) *catalog.Table {
	schema := cpjSchema(name, rfjWidth)
	cols := make([]catalog.Column, rfjWidth)
	for c := range cols {
		cols[c] = catalog.Column{Name: schema[c].Name, Type: schema[c].Type, Ordinal: c}
	}
	return &catalog.Table{Name: name, Columns: cols}
}

func cpScan(tbl *catalog.Table) *SeqScan {
	return &SeqScan{Table: tbl, Alias: tbl.Name, schema: cpjSchema(tbl.Name, rfjWidth)}
}

func cpFilter(child Node, pred Expr) Node {
	return &Filter{Child: child, Predicate: pred, LeafLocal: true}
}

func cpCol(i int) Expr { return &ColumnRef{Name: "c", Index: i} }

// (a) set_rel_consider_parallel's rtekind switch and the baserestrictinfo walk.
func TestConsiderParallel_SetAndClearedByShape(t *testing.T) {
	cat := catalog.NewInMemory()
	if _, err := cat.Routines().Create(&catalog.Routine{
		Name: "unsafe_f", ArgTypes: []catalog.Type{{Name: "int4"}},
		ReturnType: catalog.Type{Name: "bool"}, Language: "sql", Body: "SELECT true",
	}, false); err != nil {
		t.Fatal(err)
	}
	if _, err := cat.Routines().Create(&catalog.Routine{
		Name: "safe_f", ArgTypes: []catalog.Type{{Name: "int4"}},
		ReturnType: catalog.Type{Name: "bool"}, Language: "sql", Body: "SELECT true",
		Parallel: "s",
	}, false); err != nil {
		t.Fatal(err)
	}
	plain := cpTable("t")
	temp := cpTable("tmp")
	temp.Temp = true
	sys := cpTable("pg_class")
	sys.Virtual = true

	eq := &BinaryOp{Op: parser.OpEq, Left: cpCol(0), Right: &IntegerConst{Value: 1}}
	call := func(name string) Expr { return &FuncCall{Name: name, Args: []Expr{cpCol(0)}} }

	cases := []struct {
		name string
		leaf Node
		tbl  *catalog.Table
		want bool
	}{
		{"plain relation", cpScan(plain), plain, true},
		{"plain relation with a safe operator qual", cpFilter(cpScan(plain), eq), plain, true},
		{"temp table (RELPERSISTENCE_TEMP, allpaths.c:622)", cpScan(temp), temp, false},
		{"system catalog (virtual, leader-only callbacks)", cpScan(sys), sys, false},
		{"qual calls a user function with proparallel unset ('u')", cpFilter(cpScan(plain), call("unsafe_f")), plain, false},
		{"qual calls a user function marked PARALLEL SAFE", cpFilter(cpScan(plain), call("safe_f")), plain, true},
		{"qual calls nextval (builtin 'u')", cpFilter(cpScan(plain), call("nextval")), plain, false},
		{"qual calls a builtin not in the restricted table", cpFilter(cpScan(plain), call("abs")), plain, true},
		{"qual carries a SubPlan (clauses.c:857)", cpFilter(cpScan(plain), &ExistsExpr{Plan: cpScan(plain)}), plain, false},
		{"qual carries a PARAM_EXEC reference (clauses.c:894)", cpFilter(cpScan(plain), &BinaryOp{Op: parser.OpEq, Left: cpCol(0), Right: &OuterColumnRef{}}), plain, false},
		{"CTE scan (RTE_CTE, allpaths.c:706)", &CTEScan{Name: "w", Child: cpScan(plain)}, nil, false},
		{"subquery leaf without LIMIT (RTE_SUBQUERY)", &Project{Child: cpScan(plain)}, nil, true},
		{"subquery leaf with LIMIT (limit_needed, allpaths.c:670)", &Project{Child: &Limit{Child: cpScan(plain), Limit: &IntegerConst{Value: 1}}}, nil, false},
		{"subquery leaf over a temp table", &Project{Child: cpScan(temp)}, nil, false},
		{"index scan leaf on a plain relation", &IndexScan{Table: plain}, plain, true},
		{"index scan leaf on a temp table", &IndexScan{Table: temp}, temp, false},

		// --- review findings on the first cut, each of which was fail-OPEN ---

		// Finding 3: random/random_normal/setseed are proparallel 'r'
		// (pg_proc.dat:3488-3507). A worker drawing its own stream is the
		// canonical "different rows per run" wrong answer.
		{"qual calls random() (builtin 'r')", cpFilter(cpScan(plain), &FuncCall{Name: "random"}), plain, false},
		{"qual calls pg_catalog.nextval (schema-qualified lookup)", cpFilter(cpScan(plain), call("pg_catalog.nextval")), plain, false},

		// Finding 4: an index leaf keeps its predicate in Key/LowKey/HighKey,
		// not in a Filter wrapper, so the qual walk never saw it.
		{"index scan whose KEY calls nextval", &IndexScan{Table: plain, Key: call("nextval")}, plain, false},
		{"index scan whose HighKey calls nextval", &IndexScan{Table: plain, HighKey: call("nextval")}, plain, false},
		{"index-only scan whose Key calls nextval", &IndexOnlyScan{Table: plain, Key: call("nextval")}, plain, false},
		{"bitmap heap scan whose BitmapQual calls nextval", &BitmapHeapScan{Table: plain, BitmapQual: []Expr{call("nextval")}}, plain, false},

		// Finding 1: ScalarFuncScan fell to the default arm, whose subtree
		// walk saw no children and read that as "nothing unsafe".
		{"FROM unsafe_f() (ScalarFuncScan, proparallel unset)", &ScalarFuncScan{Func: call("unsafe_f")}, nil, false},
		{"FROM safe_f() (ScalarFuncScan, PARALLEL SAFE)", &ScalarFuncScan{Func: call("safe_f")}, nil, true},
		{"catalog SRF leaf (leader-only callbacks)", &PgPartitionTree{}, nil, false},

		// Finding 2: a subquery leaf's OWN expressions were never checked —
		// only node kinds. `FROM (SELECT nextval('s'), x FROM big) q` passed.
		{"subquery leaf whose Project target calls nextval", &Project{Child: cpScan(plain), Targets: []Expr{call("nextval")}}, nil, false},
		{"subquery leaf whose inner Filter calls random()", &Project{Child: cpFilter(cpScan(plain), &FuncCall{Name: "random"})}, nil, false},
		{"subquery leaf whose Aggregate arg calls nextval", &Project{Child: &Aggregate{Child: cpScan(plain), Aggs: []AggregateCall{{Arg: call("nextval")}}}}, nil, false},

		// Finding 1 (second half): the subtree walk descended through
		// parallelChildren, which answers "no children" for anything it does
		// not model, and the SAFETY reading of that is "nothing unsafe below".
		// An unmodelled node must now refuse the whole subquery.
		{"subquery leaf containing a WindowAgg (unmodelled by parallelChildren)", &Project{Child: &WindowAgg{Child: cpScan(temp)}}, nil, false},
		{"subquery leaf containing a SetOp over a temp table", &Project{Child: &SetOp{Left: cpScan(plain), Right: cpScan(temp)}}, nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := relConsiderParallel(tc.leaf, tc.tbl, cat); got != tc.want {
				t.Fatalf("relConsiderParallel = %v, want %v", got, tc.want)
			}
		})
	}
}

// (a) continued: the protocol step publishes the flag onto every base rel
// and re-stamps the prebuilt path, and `parallelModeOK` gates all of it —
// `max_parallel_workers_per_gather = 0` or the kill switch clears every rel.
func TestSetBaseRelConsiderParallel_StampsRelsAndPrebuiltPaths(t *testing.T) {
	withParallelOn(t, func() {
		names := []string{"a", "b", "c"}
		prob := rfjProblem(names, []int64{1000, 1000, 1000}, []Expr{rfjEq(names, 0, 1), rfjEq(names, 1, 2)})
		prob.relInfos[1].table.Temp = true
		prob.scans[1] = cpScan(prob.relInfos[1].table)
		s, err := buildInitialRels(prob.bindings, prob.scans, prob.relInfos, prob.cp, 0, nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, rel := range s.joinrels[1] {
			if rel.ConsiderParallel || rel.Pathlist[0].ParallelSafe {
				t.Fatal("a rel that has not been through the step must not consider parallel")
			}
		}
		s.setBaseRelConsiderParallel(nil)
		if !s.parallelModeOK {
			t.Fatal("parallelModeOK false under the default max_parallel_workers_per_gather")
		}
		want := []bool{true, false, true}
		for i, rel := range s.joinrels[1] {
			if rel.ConsiderParallel != want[i] {
				t.Errorf("rel %d ConsiderParallel = %v, want %v", i, rel.ConsiderParallel, want[i])
			}
			for _, p := range rel.Pathlist {
				if p.ParallelSafe != want[i] {
					t.Errorf("rel %d path ParallelSafe = %v, want the rel's flag %v", i, p.ParallelSafe, want[i])
				}
			}
		}

		// max_parallel_workers_per_gather = 0: parallelModeOK false, every rel
		// cleared (planner.c:339).
		off := prob.cp
		off.maxParallelWorkersPerGather = 0
		s2, err := buildInitialRels(prob.bindings, prob.scans, prob.relInfos, off, 0, nil)
		if err != nil {
			t.Fatal(err)
		}
		s2.setBaseRelConsiderParallel(nil)
		if s2.parallelModeOK {
			t.Fatal("parallelModeOK must be false at max_parallel_workers_per_gather = 0")
		}
		for i, rel := range s2.joinrels[1] {
			if rel.ConsiderParallel {
				t.Errorf("rel %d considers parallel with the GUC at 0", i)
			}
		}
	})
}

// (a) continued: build_join_rel's propagation (relnode.c:842) — both inputs
// AND the join clauses.
func TestJoinrelConsiderParallel_BothInputsAndClauses(t *testing.T) {
	withParallelOn(t, func() {
		names := []string{"a", "b"}
		safeClause := rfjEq(names, 0, 1)
		unsafeClause := &BinaryOp{Op: parser.OpEq,
			Left:  &FuncCall{Name: "nextval", Args: []Expr{&ColumnRef{Name: "a0", Index: 0, SourceTableIdx: 0}}},
			Right: &ColumnRef{Name: "b0", Index: rfjWidth, SourceTableIdx: 1}}
		run := func(t *testing.T, tempB bool, conj []Expr, want bool) {
			t.Helper()
			prob := rfjProblem(names, []int64{1000, 1000}, conj)
			if tempB {
				prob.relInfos[1].table.Temp = true
			}
			s, err := buildInitialRels(prob.bindings, prob.scans, prob.relInfos, prob.cp, 0, nil)
			if err != nil {
				t.Fatal(err)
			}
			s.clauses = buildRestrictInfos(prob.conjuncts, 0, prob.cumOffsets)
			s.setBaseRelConsiderParallel(nil)
			s.builder = newJoinRelBuilder(s, nil)
			joinrel, err := s.makeJoinRel(s.joinrels[1][0], s.joinrels[1][1])
			if err != nil || joinrel == nil {
				t.Fatalf("makeJoinRel: %v %v", joinrel, err)
			}
			if joinrel.ConsiderParallel != want {
				t.Fatalf("joinrel.ConsiderParallel = %v, want %v", joinrel.ConsiderParallel, want)
			}
			// Every path of the join rel carries the same flag — the
			// uniformity the serial-arm argument rests on (file header).
			for _, p := range joinrel.Pathlist {
				if p.ParallelSafe != want {
					t.Fatalf("join path %v ParallelSafe = %v, want %v", p.Kind, p.ParallelSafe, want)
				}
			}
		}
		t.Run("both safe, safe clause", func(t *testing.T) { run(t, false, []Expr{safeClause}, true) })
		t.Run("one input temp", func(t *testing.T) { run(t, true, []Expr{safeClause}, false) })
		t.Run("restricted join clause", func(t *testing.T) { run(t, false, []Expr{unsafeClause}, false) })
	})
}

// (b) compute_parallel_worker: the path-model twin answers exactly what the
// post-pass answers for the same relation, at the ladder's edges.
func TestComputeParallelWorkerForRel_MatchesThePostPassLadder(t *testing.T) {
	cp := defaultCostParams() // min 1024 blocks, max 4 per gather
	for _, blocks := range []int64{0, 1, 1023, 1024, 3071, 3072, 9216, 27648, 100000} {
		tbl := &catalog.Table{Name: "t"}
		post := computeParallelWorkers(&SeqScan{Table: tbl}, ParallelSettings{
			MaxWorkersPerGather: cp.maxParallelWorkersPerGather,
			MinTableScanBlocks:  cp.minParallelTableScanBlocks,
			BlocksForTable:      func(*catalog.Table) (int64, bool) { return blocks, true },
		})
		if got := computeParallelWorkerForRel(cp, blocks, 0); got != post {
			t.Errorf("blocks=%d: path model says %d workers, post-pass says %d", blocks, got, post)
		}
	}
	if got := computeParallelWorkerForRel(cp, 10, 3); got != 3 {
		t.Errorf("parallel_workers reloption must win outright: got %d", got)
	}
	if got := computeParallelWorkerForRel(cp, 100000, 9); got != 4 {
		t.Errorf("max_parallel_workers_per_gather caps the reloption: got %d", got)
	}
}

// cpSearch runs the production protocol of searchOneProblem step by step on a
// fixture, returning the context so the pins can read the rels.
func cpSearch(t *testing.T, prob *joinlistProblem) *searchCtx {
	t.Helper()
	s, err := buildInitialRels(prob.bindings, prob.scans, prob.relInfos, prob.cp, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	s.clauses = buildRestrictInfos(prob.conjuncts, 0, prob.cumOffsets)
	s.setBaseRelConsiderParallel(prob.cat)
	s.addBaseRelPartialPaths()
	s.addBaseRelIndexPaths(prob.cat)
	if _, err := s.joinSearch(s.clauses, newJoinRelBuilder(s, prob.cat)); err != nil {
		t.Fatal(err)
	}
	return s
}

// cpBigProblem: `a` and `c` are large ANALYZEd relations (pages well above
// min_parallel_table_scan_size), `b` is small. rows/pages are stamped as
// pg_class.reltuples/relpages so baseRelPages reads the real figure.
func cpBigProblem(names []string) *joinlistProblem {
	rows := []int64{2_000_000, 100, 500_000}
	pages := []int{20_000, 1, 5_000}
	prob := rfjProblem(names, rows, []Expr{rfjEq(names, 0, 1), rfjEq(names, 1, 2)})
	for i := range names {
		prob.relInfos[i].table.Stats = &catalog.TableStats{RowCount: rows[i], Pages: pages[i], Analyzed: true}
	}
	return prob
}

// (b) create_plain_partial_paths: a partial seq scan exists on the large rels
// only, sized by compute_parallel_worker and priced by cost_seqscan's
// parallel arm.
func TestCreatePlainPartialPaths_LargeRelGetsAPartialSeqScanDividedByTheDivisor(t *testing.T) {
	withParallelOn(t, func() {
		names := []string{"a", "b", "c"}
		prob := cpBigProblem(names)
		s := cpSearch(t, prob)
		cp := prob.cp
		for i, rel := range s.joinrels[1] {
			tbl := prob.relInfos[i].table
			pages := int64(tbl.Stats.Pages)
			wantWorkers := computeParallelWorkerForRel(cp, pages, 0)
			if wantWorkers == 0 {
				if len(rel.PartialPathlist) != 0 {
					t.Errorf("%s: %d pages is below min_parallel_table_scan_size but has %d partial paths", tbl.Name, pages, len(rel.PartialPathlist))
				}
				continue
			}
			if len(rel.PartialPathlist) != 1 {
				t.Fatalf("%s: want exactly one partial path, got %d", tbl.Name, len(rel.PartialPathlist))
			}
			pp := rel.PartialPathlist[0]
			if pp.Kind != PathSeqScan || pp.ParallelWorkers != wantWorkers || !pp.ParallelSafe {
				t.Fatalf("%s: partial path = kind %v workers %d safe %v, want seq scan / %d / true", tbl.Name, pp.Kind, pp.ParallelWorkers, pp.ParallelSafe, wantWorkers)
			}
			d := getParallelDivisor(wantWorkers, cp.parallelLeaderParticipation)
			// The same inputs buildInitialRels priced the serial scan on
			// (see addBaseRelPartialPaths): pages from rows × width.
			disk := cp.seqPageCost * float64(estScanPages(rel.Rows, rel.Width))
			cpu := cp.cpuTupleCost * rel.Rows
			wantTotal := disk + cpu/d
			if diff := pp.Cost.Total - wantTotal; diff > 1e-6 || diff < -1e-6 {
				t.Errorf("%s: partial total %.4f, want disk %.2f + cpu %.2f / divisor %.2f = %.4f", tbl.Name, pp.Cost.Total, disk, cpu, d, wantTotal)
			}
			if pp.Cost.Startup != 0 {
				t.Errorf("%s: a seq scan has zero startup, got %v", tbl.Name, pp.Cost.Startup)
			}
			if want := clampRowEst(rel.Rows / d); pp.Rows != want {
				t.Errorf("%s: partial rows %v, want clamp_row_est(%v / %v) = %v", tbl.Name, pp.Rows, rel.Rows, d, want)
			}
			// The serial scan is NOT divided: the partial path's saving is
			// exactly the CPU share the workers take.
			serial := rel.Pathlist[0]
			if serial.Kind != PathPrebuilt || serial.ParallelWorkers != 0 {
				t.Fatalf("%s: Pathlist[0] should be the serial prebuilt scan, got %v/%d", tbl.Name, serial.Kind, serial.ParallelWorkers)
			}
			if !(pp.Cost.Total < serial.Cost.Total) {
				t.Errorf("%s: partial %v must be cheaper than serial %v", tbl.Name, pp.Cost.Total, serial.Cost.Total)
			}
		}
		// A partial seq scan is never generated on a rel that does not
		// consider parallel, whatever its size.
		prob2 := cpBigProblem(names)
		prob2.relInfos[0].table.Temp = true
		s2 := cpSearch(t, prob2)
		if n := len(s2.joinrels[1][0].PartialPathlist); n != 0 {
			t.Errorf("temp relation has %d partial paths, want 0", n)
		}
		// And never once the GUC is 0.
		prob3 := cpBigProblem(names)
		prob3.cp.maxParallelWorkersPerGather = 0
		s3 := cpSearch(t, prob3)
		for i, rel := range s3.joinrels[1] {
			if len(rel.PartialPathlist) != 0 {
				t.Errorf("rel %d has partial paths at max_parallel_workers_per_gather = 0", i)
			}
		}
	})
}

// (c) NO partial path is chosen as the final path, and the serial control arm
// is unchanged: the chosen tree is the same with the parallel GUC at its
// default and at 0, and no path in the chosen tree carries workers.
func TestPartialPathIsNeverTheFinalPath(t *testing.T) {
	withParallelOn(t, func() {
		names := []string{"a", "b", "c"}
		prob := cpBigProblem(names)
		s := cpSearch(t, prob)
		partial := map[*Path]bool{}
		nPartial := 0
		for _, rel := range s.joinrels[1] {
			for _, p := range rel.PartialPathlist {
				partial[p] = true
				nPartial++
			}
		}
		if nPartial == 0 {
			t.Fatal("fixture produced no partial paths; the pin would be vacuous")
		}
		final, err := s.finalPath()
		if err != nil {
			t.Fatal(err)
		}
		var walk func(*Path)
		walk = func(p *Path) {
			if p == nil {
				return
			}
			if partial[p] {
				t.Fatalf("a partial path (%v, %d workers) reached the chosen tree", p.Kind, p.ParallelWorkers)
			}
			if p.ParallelWorkers != 0 {
				t.Fatalf("chosen tree carries a path with %d workers", p.ParallelWorkers)
			}
			for _, c := range p.Children {
				walk(c)
			}
		}
		walk(final)
		// Join rels have no partial paths yet (C-19d/e produce them).
		for lev := 2; lev < len(s.joinrels); lev++ {
			for _, rel := range s.joinrels[lev] {
				if len(rel.PartialPathlist) != 0 {
					t.Fatalf("join rel %#x has partial paths before C-19d", uint32(rel.Relids))
				}
			}
		}

		// The whole production seam, both arms: identical trees.
		on, err := planJoinlistSearch(deconstructRangeVars(len(names)), cpBigProblem(names))
		if err != nil {
			t.Fatal(err)
		}
		offProb := cpBigProblem(names)
		offProb.cp.maxParallelWorkersPerGather = 0
		off, err := planJoinlistSearch(deconstructRangeVars(len(names)), offProb)
		if err != nil {
			t.Fatal(err)
		}
		if a, b := planShapeString(on), planShapeString(off); a != b {
			t.Fatalf("serial control arm moved:\nparallel on:\n%s\nparallel off:\n%s", a, b)
		}
	})
}
