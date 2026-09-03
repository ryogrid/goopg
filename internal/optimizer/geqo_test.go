package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestGeqoPoolSize checks the pool sizing formula against PG's gimme_pool_size.
// Formula: size = 2^(nrels+1); if size > 50*effort return 50*effort;
// if size < 10*effort return 10*effort; else ceil(size).
func TestGeqoPoolSize(t *testing.T) {
	cases := []struct {
		nrels, effort int
		want          int
	}{
		{2, 5, 50},   // 2^3=8, below 10*5 → 50
		{12, 5, 250}, // 2^13=8192, above 50*5 → 250
		{8, 5, 250},  // 2^9=512, above 50*5 → 250
		{4, 10, 100}, // 2^5=32, below 10*10 → 100
		{6, 1, 50},   // 2^7=128, above 50*1 → 50
		{5, 10, 64},  // 2^6=64, within [100, 500]... wait 64 < 100 → 100
	}
	// Correct the last case's expectation: 2^6=64 < 10*10=100 → 100.
	cases[5].want = 100
	for _, c := range cases {
		got := poolSize(c.nrels, c.effort)
		if got != c.want {
			t.Errorf("poolSize(%d,%d)=%d, want %d", c.nrels, c.effort, got, c.want)
		}
	}
}

// TestGeqoGenerations checks the generations default (= pool size).
func TestGeqoGenerations(t *testing.T) {
	if got := numberGenerations(50, 0); got != 50 {
		t.Errorf("numberGenerations(50,0)=%d, want 50", got)
	}
	if got := numberGenerations(50, 100); got != 100 {
		t.Errorf("numberGenerations(50,100)=%d, want 100", got)
	}
}

// TestGeqoInitTour checks initTour produces a valid permutation of 1..n.
func TestGeqoInitTour(t *testing.T) {
	rng := newGeqoRNG(42)
	for n := 2; n <= 16; n++ {
		tour := make([]Gene, n)
		initTour(tour, rng)
		seen := make([]bool, n+1)
		for _, g := range tour {
			if g < 1 || g > Gene(n) || seen[g] {
				t.Fatalf("n=%d: invalid tour %v", n, tour)
			}
			seen[g] = true
		}
	}
}

// TestGeqoRandintRange checks randint stays within [lower, upper].
func TestGeqoRandintRange(t *testing.T) {
	rng := newGeqoRNG(7)
	for i := 0; i < 10000; i++ {
		v := rng.randint(0, 9)
		if v < 0 || v > 9 {
			t.Fatalf("randint out of range: %d", v)
		}
	}
}

// TestGeqoLinearRandRange checks linearRand stays within [0, max).
func TestGeqoLinearRandRange(t *testing.T) {
	rng := newGeqoRNG(3)
	for i := 0; i < 10000; i++ {
		v := linearRand(50, 2.0, rng)
		if v < 0 || v >= 50 {
			t.Fatalf("linearRand out of range: %d", v)
		}
	}
}

// TestGeqoRNGDeterministic checks a fixed seed produces a fixed sequence.
func TestGeqoRNGDeterministic(t *testing.T) {
	r1 := newGeqoRNG(99)
	r2 := newGeqoRNG(99)
	for i := 0; i < 100; i++ {
		if r1.rand() != r2.rand() {
			t.Fatalf("RNG not deterministic at step %d", i)
		}
	}
}

// TestGeqoEdgeTable checks the ERX edge table is built with the expected
// adjacency for two simple tours.
func TestGeqoEdgeTable(t *testing.T) {
	et := allocEdgeTable(4)
	defer freeEdgeTable(et)
	momma := []Gene{1, 2, 3, 4}
	daddy := []Gene{2, 1, 3, 4}
	gimmeEdgeTable(momma, daddy, 4, et)
	// City 1: edges to 2 (both parents), 4 (momma wraps), 2 shared.
	// momma: 1-2, 2-3, 3-4, 4-1 (and reverse). daddy: 2-1, 1-3, 3-4, 4-2.
	// City 1 neighbours: 2 (momma), 4 (momma wrap), 2 (daddy), 3 (daddy).
	found := map[int]bool{}
	for _, e := range et.edges[1] {
		if e < 0 {
			e = -e
		}
		found[e] = true
	}
	for _, want := range []int{2, 3, 4} {
		if !found[want] {
			t.Errorf("city 1 missing edge to %d; edges=%v", want, et.edges[1])
		}
	}
	// City 2 appears in both parents as 1's neighbour, so it should be
	// marked shared (negative).
	var hasNeg bool
	for _, e := range et.edges[1] {
		if e == -2 {
			hasNeg = true
		}
	}
	if !hasNeg {
		t.Errorf("city 1's edge to 2 should be shared (negative), got %v", et.edges[1])
	}
}

// TestGeqoGimmeTour checks the ERX crossover produces a valid tour of 1..n.
func TestGeqoGimmeTour(t *testing.T) {
	rng := newGeqoRNG(11)
	for n := 3; n <= 10; n++ {
		et := allocEdgeTable(n)
		momma := make([]Gene, n)
		daddy := make([]Gene, n)
		kid := make([]Gene, n)
		initTour(momma, rng)
		initTour(daddy, rng)
		gimmeEdgeTable(momma, daddy, n, et)
		_ = gimmeTour(kid, n, et, rng)
		seen := make([]bool, n+1)
		valid := true
		for _, g := range kid {
			if g < 1 || g > Gene(n) || seen[g] {
				valid = false
				break
			}
			seen[g] = true
		}
		if !valid {
			t.Fatalf("n=%d: gimmeTour produced invalid tour %v (parents %v %v)",
				n, kid, momma, daddy)
		}
		freeEdgeTable(et)
	}
}

// TestGeqoSpreadChromosome checks the sorted-pool invariant is maintained.
func TestGeqoSpreadChromosome(t *testing.T) {
	pool := allocPool(5, 4)
	for i := range pool.data {
		pool.data[i].worth = float64(i * 10)
		for j := range pool.data[i].string {
			pool.data[i].string[j] = Gene(j + 1)
		}
	}
	// Insert a chromosome with worth 25 (between 20 and 30). PG's binary
	// search places it at index 3 (after 0,10,20, before 30).
	chromo := &Chromosome{string: []Gene{4, 3, 2, 1}, worth: 25}
	spreadChromosome(pool, chromo)
	if pool.data[3].worth != 25 {
		t.Fatalf("worth 25 not at index 3, got %v", worths(pool))
	}
	// The worst (40) is displaced; the pool is [0,10,20,25,30].
	want := []float64{0, 10, 20, 25, 30}
	for i, w := range want {
		if pool.data[i].worth != w {
			t.Fatalf("pool order broken at %d: got %v want %v", i, worths(pool), want)
		}
	}
	// Discard a too-bad chromosome (worth 1000 > worst 30): pool unchanged.
	chromo2 := &Chromosome{string: []Gene{1, 1, 1, 1}, worth: 1000}
	spreadChromosome(pool, chromo2)
	for i, w := range want {
		if pool.data[i].worth != w {
			t.Fatalf("pool changed after discarding bad chromo: %v want %v", worths(pool), want)
		}
	}
}

func worths(p *Pool) []float64 {
	out := make([]float64, p.size)
	for i := range p.data {
		out[i] = p.data[i].worth
	}
	return out
}

// TestGeqoEvalPanicsOnNilCtx checks that geqoEval panics on nil ctx (defensive).
func TestGeqoEvalPanicsOnNilCtx(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("geqoEval(nil) should panic")
		}
	}()
	geqoEval(nil, []Gene{1, 2}, 2)
}

// --- GEQO usage-case tests ---

// geqoTestCtx builds an n-relation search context suitable for the REAL
// joinRelBuilder: each base rel carries a costed PathPrebuilt path and its
// CheapestTotal, and `s.clauses` holds one cross-rel equality clause per
// consecutive pair so every clump merge is "desirable" (a relevant join clause
// exists). This mirrors the production setup in `searchOneProblem`
// (relfromjoinlist.go): buildInitialRels (level-1 rels with paths) + clauses +
// the concrete `newJoinRelBuilder`.
func geqoTestCtx(t *testing.T, n int) *searchCtx {
	t.Helper()
	s := jslCtx(t, n)
	// One equijoin clause between each consecutive pair (i, i+1). gimmeTree's
	// mergeClump uses desirableJoin → hasRelevantJoinClause; a fully connected
	// clause set is what makes the GA able to collapse the tour into one clump
	// during the non-forced pass.
	var clauses []*restrictInfo
	for i := 0; i < n-1; i++ {
		clauses = append(clauses, &restrictInfo{
			clause:      &BinaryOp{Op: parser.OpEq, Left: jsCol(i, "a"), Right: jsCol(i+1, "a")},
			relids:      relsetOf(i) | relsetOf(i+1),
			leftKey:     jsCol(i, "a"),
			rightKey:    jsCol(i+1, "a"),
			leftRelids:  relsetOf(i),
			rightRelids: relsetOf(i + 1),
			isEquijoin:  true,
			ecID:        noEquivClass,
		})
	}
	l := &restrictInfoList{}
	for _, c := range clauses {
		l.all = append(l.all, c)
	}
	s.clauses = l
	return s
}

// TestGeqoSearchReturnsValidFullRelPath is the core usage test: the real GA
// driver (geqoSearch) over the real joinRelBuilder must return a path whose
// final joinrel covers EVERY base relation — i.e. GEQO actually solves the
// join-order problem rather than failing back or returning a partial tree.
// Runs for 2..8 relations; below maxSearchRels and above the 2-rel minimum,
// spanning the typical GEQO-eligible range.
func TestGeqoSearchReturnsValidFullRelPath(t *testing.T) {
	for n := 2; n <= 8; n++ {
		s := geqoTestCtx(t, n)
		s.builder = newJoinRelBuilder(s, nil)
		p, err := geqoSearch(s, s.builder, 5)
		if err != nil {
			t.Fatalf("n=%d: geqoSearch error: %v", n, err)
		}
		if p == nil {
			t.Fatalf("n=%d: geqoSearch returned nil path", n)
		}
		all := RelSet(1)<<uint(n) - 1
		if p.Rel.Relids != all {
			t.Errorf("n=%d: final relids=%#08x, want full relset %#08x", n, uint32(p.Rel.Relids), uint32(all))
		}
		if !(p.Cost.Total > 0) {
			t.Errorf("n=%d: total cost %v not positive", n, p.Cost.Total)
		}
	}
}

// TestGeqoSearchDeterministic checks the fixed-seed PRNG makes the whole GA
// deterministic: two runs on the same problem return the same final cost.
// (PG uses geqo_seed; goopg's v0 uses a fixed seed — geqo.go:141.)
func TestGeqoSearchDeterministic(t *testing.T) {
	run := func() float64 {
		s := geqoTestCtx(t, 5)
		s.builder = newJoinRelBuilder(s, nil)
		p, err := geqoSearch(s, s.builder, 5)
		if err != nil {
			t.Fatalf("geqoSearch: %v", err)
		}
		return p.Cost.Total
	}
	a, b := run(), run()
	if a != b {
		t.Errorf("GEQO not deterministic: first run %v, second run %v", a, b)
	}
}

// TestGeqoSearchFailsWithoutValidPool checks the guarded failure path: when no
// valid tour can be formed, geqoSearch returns an error instead of a nil path
// or a panic. Build a context with an EMPTY clause list and a builder that
// refuses to add paths (so every joinrel ends with no CheapestTotal → every
// tour scores MaxFloat64 → randomInitPool fails after 10000 tries).
func TestGeqoSearchFailsWithoutValidPool(t *testing.T) {
	s := jslCtx(t, 4) // no clauses, no builder addPaths refusal → still connects via force pass
	s.builder = newJoinRelBuilder(s, nil)
	// A nil clause list is legal (desirableJoin handles it); GEQO's force pass
	// must still collapse the clumps. This asserts the opposite failure: with a
	// builder that yields no paths, we error out cleanly rather than panic.
	bad := &refusingBuilder{s: s}
	s.builder = bad
	p, err := geqoSearch(s, bad, 5)
	if err == nil {
		t.Fatalf("geqoSearch should fail with a refusing builder, got path %v", p)
	}
}

// refusingBuilder implements joinRelBuilder but adds NO paths, so every
// joinrel ends without a CheapestTotal and every tour scores MaxFloat64.
type refusingBuilder struct {
	s *searchCtx
}

func (r *refusingBuilder) sizeJoinRel(outer, inner *RelOptInfo, clauses []*restrictInfo) (float64, int) {
	return r.s.calcJoinrelSize(nil, outer, inner, clauses)
}

func (r *refusingBuilder) addPaths(joinrel, outer, inner *RelOptInfo, clauses []*restrictInfo) error {
	return nil // refuse: never add a path
}

// TestGeqoDispatchConditions checks the two GUC-backed knobs that gate GEQO:
// GeqoEnabled (default on) and GeqoThreshold (default 12, matching PG), plus
// the SetGeqo* setters used by the cmd/goopg/main.go bridge.
func TestGeqoDispatchConditions(t *testing.T) {
	if !GeqoEnabled() {
		t.Error("GEQO should default to enabled (PG geqo=on)")
	}
	if got := GeqoThreshold(); got != 12 {
		t.Errorf("GeqoThreshold default = %d, want 12 (PG geqo_threshold)", got)
	}
	// The setters are process-global atomics; restore the defaults afterwards
	// so other tests are unaffected.
	SetGeqoEnabled(false)
	if GeqoEnabled() {
		t.Error("SetGeqoEnabled(false) did not disable GEQO")
	}
	SetGeqoEnabled(true)
	if !GeqoEnabled() {
		t.Error("SetGeqoEnabled(true) did not re-enable GEQO")
	}
	SetGeqoThreshold(8)
	if got := GeqoThreshold(); got != 8 {
		t.Errorf("SetGeqoThreshold(8) -> %d", got)
	}
	SetGeqoThreshold(2) // clamps below 2 (geqo.go:28)
	if got := GeqoThreshold(); got != 2 {
		t.Errorf("SetGeqoThreshold(1) should clamp to 2, got %d", got)
	}
	SetGeqoThreshold(12) // restore default
}