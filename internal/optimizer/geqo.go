package optimizer

import (
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/goopg/goopg/internal/utils/pgprng"
	"github.com/goopg/goopg/internal/utils/pgqsort"
)

// geqoOn gates GEQO usage. Default ON, matching PG's `geqo` GUC default.
// Set via the geqo GUC bridge (SET geqo = off disables GEQO).
var geqoOn atomic.Bool

// geqoThreshold is the minimum number of relations that triggers GEQO.
// Default 12 (PG's geqo_threshold default). Set via the geqo_threshold GUC bridge.
var geqoThreshold atomic.Int64

func init() {
	geqoOn.Store(true)
	geqoThreshold.Store(12)
}

// SetGeqoEnabled sets whether GEQO is enabled (GUC bridge target).
func SetGeqoEnabled(on bool) { geqoOn.Store(on) }

// SetGeqoThreshold sets the GEQO threshold (GUC bridge target).
func SetGeqoThreshold(n int64) {
	if n < 2 {
		n = 2
	}
	geqoThreshold.Store(n)
}

// GeqoEnabled reports whether GEQO is enabled (read at plan time).
func GeqoEnabled() bool { return geqoOn.Load() }

// GeqoThreshold returns the GEQO threshold (read at plan time).
func GeqoThreshold() int { return int(geqoThreshold.Load()) }

// Gene is a relation index in a chromosome (tour). 1-based (1..nrels),
// matching PG's convention.
type Gene int

// Chromosome is one individual in the GA population: a permutation of Gene
// values (the join order) and its fitness (cheapest path cost).
type Chromosome struct {
	string []Gene
	worth  float64
}

// Pool is the GA population, sorted by ascending worth (lower cost = fitter).
type Pool struct {
	data         []Chromosome
	size         int
	stringLength int
}

// allocPool allocates the GA pool. PG oracle: alloc_pool (geqo_pool.c:41).
func allocPool(poolSize, stringLength int) *Pool {
	p := &Pool{
		data:         make([]Chromosome, poolSize),
		size:         poolSize,
		stringLength: stringLength,
	}
	for i := range p.data {
		p.data[i].string = make([]Gene, stringLength)
	}
	return p
}

// freePool releases the pool's gene strings (not needed in Go with GC, but
// kept for clarity — the memory will be reclaimed when the pool drops out of
// scope).
func freePool(p *Pool) {
	for i := range p.data {
		p.data[i].string = nil
	}
	p.data = nil
}

// allocChromosome allocates a single chromosome. PG oracle: alloc_chromo
// (geqo_pool.c:161).
func allocChromosome(stringLength int) *Chromosome {
	return &Chromosome{
		string: make([]Gene, stringLength),
	}
}

// poolSize returns the pool size for a query with `nrels` base relations.
// PG oracle: gimme_pool_size (geqo_main.c:319).
func poolSize(nrels, effort int) int {
	if effort < 1 {
		effort = 5
	}
	size := math.Pow(2.0, float64(nrels+1))
	maxSize := 50 * effort
	if size > float64(maxSize) {
		return maxSize
	}
	minSize := 10 * effort
	if size < float64(minSize) {
		return minSize
	}
	return int(math.Ceil(size))
}

// numberGenerations returns the number of GA generations for a given pool size.
// PG oracle: gimme_number_generations (geqo_main.c:351).
func numberGenerations(poolSize, genSetting int) int {
	if genSetting > 0 {
		return genSetting
	}
	return poolSize
}

// initTour fills `tour` with a random permutation of 1..numGenes.
// PG oracle: init_tour (geqo_recombination.c:33) — inside-out Fisher-Yates,
// statement for statement. The loop starts at 1: PG makes no draw for
// tour[0], and the stream must stay in step with PG's (M0146-0214).
func initTour(tour []Gene, rng *geqoRNG) {
	n := len(tour)
	if n > 0 {
		tour[0] = 1
	}
	for i := 1; i < n; i++ {
		j := rng.randint(0, i)
		// i != j avoids reading an uninitialised element, as in PG.
		if i != j {
			tour[i] = tour[j]
		}
		tour[j] = Gene(i + 1)
	}
}

// geqoSearch is the main GEQO driver. It takes a pre-built search context
// (with level-1 rels and clauses already set up), runs the GA, and returns
// the best path. PG oracle: geqo (geqo_main.c:71).
//
// The caller must have already called buildInitialRels and set s.clauses.
// effort is Geqo_effort (1-10, default 5).
func geqoSearch(s *searchCtx, builder joinRelBuilder, effort int) (*Path, error) {
	nrels := s.nrels
	// take2 P3-10: the pool size and generation count come from the session's
	// GUCs. PG treats 0 in either as "derive me" — geqo_pool_size from effort,
	// geqo_generations from the pool size — so the override is simply a
	// non-zero value.
	ps := s.cp.geqoPoolSize
	// gimme_pool_size takes the setting only when it is >= 2: a legal pool
	// has at least two individuals, so 1 is ignored like 0.
	if ps < 2 {
		ps = poolSize(nrels, effort)
	}
	gens := numberGenerations(ps, s.cp.geqoGenerations)

	// PRNG: geqo_set_seed(root, Geqo_seed) (geqo_main.c) seeds PG's own
	// generator with pg_prng_fseed. goopg runs the same generator, so with
	// the same geqo_seed (default 0) every draw below is PG's draw
	// (M0146-0214).
	rng := newGeqoRNG(s.cp.geqoSeed)
	if geqoTraceOn {
		// M0146-0214 slice 3: the initial rels in gene order, for a
		// tour-by-tour comparison with PG's geqo_eval (GEQO_DEBUG-style).
		fmt.Fprintf(os.Stderr, "GEQOSTART nrels=%d\n", nrels)
		for i, r := range s.levelRels(1) {
			var rows, st, tot float64
			kind := -1
			if r != nil {
				rows = r.Rows
				if r.CheapestTotal != nil {
					kind, st, tot = int(r.CheapestTotal.Kind), r.CheapestTotal.Cost.Startup, r.CheapestTotal.Cost.Total
				}
			}
			fmt.Fprintf(os.Stderr, "GEQOREL gene=%d relids=%v rows=%.0f kind=%d startup=%.6f total=%.6f\n", i+1, traceRelids(r), rows, kind, st, tot)
		}
	}

	pool := allocPool(ps, nrels)
	defer freePool(pool)

	// Random initialisation of the pool. If no valid individual is found after
	// many tries, fail — the seam falls back to the syntactic shape.
	if !randomInitPool(pool, s, builder, rng) {
		return nil, fmt.Errorf("geqo: failed to make a valid plan")
	}

	// Sort the pool by fitness (cheapest first).
	sortPool(pool)

	// Allocate parent and child chromosomes.
	momma := allocChromosome(nrels)
	daddy := allocChromosome(nrels)
	defer func() {
		momma.string = nil
		daddy.string = nil
	}()

	// Edge table for ERX.
	edgeTable := allocEdgeTable(nrels)
	defer freeEdgeTable(edgeTable)

	for gen := 0; gen < gens; gen++ {
		// Selection: pick two parents using linear bias.
		bias := s.cp.geqoBias
		if bias <= 0 {
			bias = 2.0
		}
		geqoSelection(pool, bias, momma, daddy, rng)

		// ERX crossover: edge table from parents.
		gimmeEdgeTable(momma.string, daddy.string, nrels, edgeTable)

		// Build child tour (kid reuses momma's string buffer, matching PG).
		kid := momma
		_ = gimmeTour(kid.string, nrels, edgeTable, rng)

		// Evaluate fitness.
		kid.worth = geqoEval(s, kid.string, nrels)

		// Insert into pool, displacing worst.
		spreadChromosome(pool, kid)
	}

	// Best tour is pool[0] (sorted by ascending worth).
	bestTour := pool.data[0].string

	// Build the final plan from the best tour.
	joinrel := gimmeTree(s, bestTour, nrels)
	if joinrel == nil {
		return nil, fmt.Errorf("geqo: failed to build a valid join tree from the best tour")
	}
	setCheapest(joinrel)
	p := getCheapestFractionalPath(joinrel, s.tupleFraction)
	if s.rootCheapestTotal && joinrel.CheapestTotal != nil {
		// M0146-0150: as finalPath (searchCtx.rootPathOf). GEQO keeps its
		// unordered fractional pick otherwise.
		p = joinrel.CheapestTotal
	}
	if p == nil {
		return nil, fmt.Errorf("geqo: final joinrel has no cheapest path")
	}
	return p, nil
}

// randomInitPool fills the pool with random tours and evaluates each.
// Returns false when no valid individual could be found (PG errors in this
// case: "geqo failed to make a valid plan"). PG oracle: random_init_pool
// (geqo_pool.c:90).
func randomInitPool(pool *Pool, s *searchCtx, builder joinRelBuilder, rng *geqoRNG) bool {
	i := 0
	bad := 0
	for i < pool.size {
		initTour(pool.data[i].string, rng)
		pool.data[i].worth = geqoEval(s, pool.data[i].string, pool.stringLength)
		if pool.data[i].worth < math.MaxFloat64 {
			i++
		} else {
			bad++
			if i == 0 && bad >= 10000 {
				return false
			}
		}
	}
	return true
}

// sortPool sorts the pool by ascending worth. PG oracle: sort_pool
// (geqo_pool.c:134), which is qsort with compare(). pg_qsort is not stable,
// and tours with equal worth are common (different tours can build the same
// join tree). Selection then draws pool positions, so the order PG leaves
// the ties in is part of the result. It is reproduced with PG's qsort
// (M0146-0214).
func sortPool(pool *Pool) {
	pgqsort.Sort(pool.data[:pool.size], func(a, b *Chromosome) int {
		switch {
		case a.worth == b.worth:
			return 0
		case a.worth > b.worth:
			return 1
		default:
			return -1
		}
	})
}

// spreadChromosome inserts a chromosome into the pool, displacing the worst
// individual. PG oracle: spread_chromo (geqo_pool.c:186).
func spreadChromosome(pool *Pool, chromo *Chromosome) {
	if chromo.worth > pool.data[pool.size-1].worth {
		return // too bad, discard
	}
	// Binary search for insertion point.
	top := 0
	mid := pool.size / 2
	bot := pool.size - 1
	index := -1
	for index == -1 {
		if chromo.worth <= pool.data[top].worth {
			index = top
		} else if chromo.worth == pool.data[mid].worth {
			index = mid
		} else if chromo.worth == pool.data[bot].worth {
			index = bot
		} else if bot-top <= 1 {
			index = bot
		} else if chromo.worth < pool.data[mid].worth {
			bot = mid
			mid = top + (bot-top)/2
		} else {
			top = mid
			mid = top + (bot-top)/2
		}
	}
	// Copy chromo into the pool at the last slot, then bubble it into position.
	pool.data[pool.size-1].worth = chromo.worth
	copy(pool.data[pool.size-1].string, chromo.string)
	for i := pool.size - 1; i > index; i-- {
		pool.data[i], pool.data[i-1] = pool.data[i-1], pool.data[i]
	}
}

// geqoSelection picks two parents from the pool using linear bias selection.
// PG oracle: geqo_selection (geqo_selection.c:53).
func geqoSelection(pool *Pool, bias float64, momma, daddy *Chromosome, rng *geqoRNG) {
	first := linearRand(pool.size, bias, rng)
	second := linearRand(pool.size, bias, rng)
	for second == first && pool.size > 1 {
		second = linearRand(pool.size, bias, rng)
	}
	copy(momma.string, pool.data[first].string)
	momma.worth = pool.data[first].worth
	copy(daddy.string, pool.data[second].string)
	daddy.worth = pool.data[second].worth
}

// linearRand returns a pool index with linear bias. PG oracle: linear_rand
// (geqo_selection.c:87). f(x) = bias - 2(bias-1)x, inverted via quadratic.
//
// The arithmetic is PG's, operation for operation:
// max * (bias - sqrtval) / 2.0 / (bias - 1.0). A different association
// rounds differently and can truncate to a different index (M0146-0214).
func linearRand(poolSize int, bias float64, rng *geqoRNG) int {
	max := float64(poolSize)
	for {
		sqrtval := (bias * bias) - 4.0*(bias-1.0)*rng.rand()
		if sqrtval > 0.0 {
			sqrtval = math.Sqrt(sqrtval)
		}
		index := max * (bias - sqrtval) / 2.0 / (bias - 1.0)
		if index < 0.0 || index >= max {
			continue
		}
		return int(index)
	}
}

// geqoEval evaluates the fitness of one tour. Returns the cheapest path cost,
// or math.MaxFloat64 if the tour is invalid. PG oracle: geqo_eval
// (geqo_eval.c:56). It runs in a FRESH context per evaluation (PG's temporary
// memory context + join_rel_list truncation): `makeJoinRel`'s find-or-create
// must not see joinrels built by a previous tour's gimmeTree, or the second
// evaluation would price the first's stale paths.
func geqoEval(s *searchCtx, tour []Gene, numGene int) float64 {
	probe := false
	if geqoTraceOn {
		probe = geqoTraceEval >= 0 && s.geqoEvalSeq == geqoTraceEval
		s.geqoEvalSeq++
	}
	worth := geqoEvalTour(s, tour, numGene, probe)
	if geqoTraceOn {
		var b strings.Builder
		for _, g := range tour[:numGene] {
			fmt.Fprintf(&b, "%d ", g)
		}
		w := worth
		if w == math.MaxFloat64 {
			w = math.Inf(1)
		}
		fmt.Fprintf(os.Stderr, "GEQOEVAL tour=%s worth=%.6f\n", b.String(), w)
	}
	return worth
}

// geqoTraceOn gates the GEQO tour trace (GOOPG_GEQO_TRACE=1): every evaluated
// tour with its worth, and the initial rels in gene order, in the same line
// format as the M0146-0214 PG oracle probe in geqo_eval.c, so the first
// differing worth names the costing gap.
var geqoTraceOn = os.Getenv("GOOPG_GEQO_TRACE") != ""

// geqoTraceEval selects one evaluation (0-based, in draw order) whose join
// rels mergeClump traces as GEQOJOIN lines (GOOPG_GEQO_TRACE_EVAL); -1 = none.
var geqoTraceEval = func() int {
	if v, err := strconv.Atoi(os.Getenv("GOOPG_GEQO_TRACE_EVAL")); err == nil {
		return v
	}
	return -1
}()

// geqoProbeJoin prints one join rel of a traced evaluation in the PG probe's
// GEQOJOIN format; gene k is relid bit k-1, printed as PG's RT index k.
func geqoProbeJoin(joinrel *RelOptInfo) {
	p := joinrel.CheapestTotal
	if p == nil {
		return
	}
	bms := func(r RelSet) string {
		var b strings.Builder
		b.WriteString("(b")
		for i := 0; i < 64; i++ {
			if r&(RelSet(1)<<uint(i)) != 0 {
				fmt.Fprintf(&b, " %d", i+1)
			}
		}
		b.WriteString(")")
		return b.String()
	}
	outer, inner := "-", "-"
	if len(p.Children) == 2 && p.Children[0] != nil && p.Children[1] != nil &&
		p.Children[0].Rel != nil && p.Children[1].Rel != nil {
		outer, inner = bms(p.Children[0].Rel.Relids), bms(p.Children[1].Rel.Relids)
	}
	fmt.Fprintf(os.Stderr, "GEQOJOIN relids=%s rows=%.0f kind=%d startup=%.6f total=%.6f outer=%s inner=%s\n",
		bms(joinrel.Relids), joinrel.Rows, p.Kind, p.Cost.Startup, p.Cost.Total, outer, inner)
}

func geqoEvalTour(s *searchCtx, tour []Gene, numGene int, probe bool) float64 {
	eval := s.freshEvalCtx()
	eval.geqoProbe = probe
	joinrel := gimmeTree(eval, tour, numGene)
	if joinrel == nil {
		return math.MaxFloat64
	}
	setCheapest(joinrel)
	if joinrel.CheapestTotal != nil {
		return joinrel.CheapestTotal.Cost.Total
	}
	return math.MaxFloat64
}

// freshEvalCtx clones the search context's base state — the level-1 rels, the
// clause list, the cost params, the join-info list — into a NEW context with
// an empty relMap and empty higher levels, so one GEQO tour evaluation cannot
// see another's joinrels.
func (s *searchCtx) freshEvalCtx() *searchCtx {
	n := &searchCtx{
		joinrels:       make([][]*RelOptInfo, s.nrels+1),
		relMap:         make(map[RelSet]*RelOptInfo),
		nrels:          s.nrels,
		cp:             s.cp,
		tupleFraction:  s.tupleFraction,
		relInfos:       s.relInfos,
		clauses:        s.clauses,
		builder:        s.builder,
		joinInfoList:   s.joinInfoList,
		neededCols:     s.neededCols,
		neededColsKnown: s.neededColsKnown,
		// Take2 P4-01 Slice 3: the above-tree set and its eligibility ride
		// along, so joinrels a GEQO tour creates stamp exactly as the DP
		// ones do.
		outputCols:      s.outputCols,
		outputColsKnown: s.outputColsKnown,
		outputEligible:  s.outputEligible,
		// M0146-0134: every other query-level fact the DP search reads
		// rides along too — PG's GEQO tours share the one PlannerInfo.
		// Without queryPathkeys a tour truncated every ordering as useless
		// (TPC-DS Q64's cs_item_sk order, PG's Incremental Sort input);
		// without parallelModeOK it built no partial join paths.
		queryPathkeys:      s.queryPathkeys,
		groupPathkeys:      s.groupPathkeys,
		distinctPathkeys:   s.distinctPathkeys,
		parallelModeOK:     s.parallelModeOK,
		cat:                s.cat,
		itemSpans:          s.itemSpans,
		problemItems:       s.problemItems,
		semiDerivedRHS:     s.semiDerivedRHS,
		orClauseSelDivisor: s.orClauseSelDivisor,
	}
	// Re-register the base rels (level 1), sharing the same *RelOptInfo
	// pointers. gimmeTree reads them by index; makeJoinRel may ADD paths to a
	// base rel only through a join above it, never by mutating the base rel
	// itself.
	for _, rel := range s.levelRels(1) {
		n.joinrels[1] = append(n.joinrels[1], rel)
		n.relMap[rel.Relids] = rel
	}
	return n
}

// clump is a group of already-joined relations within gimmeTree.
// PG oracle: Clump (geqo_eval.c:36).
type clump struct {
	joinrel *RelOptInfo
	size    int
}

// gimmeTree constructs a join tree from a tour. PG oracle: gimme_tree
// (geqo_eval.c:162).
func gimmeTree(s *searchCtx, tour []Gene, numGene int) *RelOptInfo {
	var clumps []*clump
	for relCount := 0; relCount < numGene; relCount++ {
		curRelIndex := int(tour[relCount]) - 1 // convert 1-based to 0-based
		if curRelIndex < 0 || curRelIndex >= len(s.levelRels(1)) {
			return nil
		}
		curRel := s.levelRels(1)[curRelIndex]
		if curRel == nil {
			return nil
		}
		curClump := &clump{joinrel: curRel, size: 1}
		clumps = mergeClump(s, clumps, curClump, numGene, false)
	}
	if len(clumps) > 1 {
		var fclumps []*clump
		for _, c := range clumps {
			fclumps = mergeClump(s, fclumps, c, numGene, true)
		}
		clumps = fclumps
	}
	if len(clumps) != 1 {
		return nil
	}
	return clumps[0].joinrel
}

// mergeClump merges a clump into a list of existing clumps.
// PG oracle: merge_clump (geqo_eval.c:237).
func mergeClump(s *searchCtx, clumps []*clump, newClump *clump, numGene int, force bool) []*clump {
	for i, oldClump := range clumps {
		if force || desirableJoin(s, oldClump.joinrel, newClump.joinrel) {
			// Build the joinrel for this pair.
			joinrel, err := s.makeJoinRel(oldClump.joinrel, newClump.joinrel)
			if err == nil && joinrel != nil {
				// PG calls set_cheapest immediately after make_join_rel
				// inside merge_clump (geqo_eval.c:280). Without it, the
				// joinrel has no CheapestTotal and the next merge using
				// this clump as input fails.
				//
				// C-19d: and immediately BEFORE it, upstream calls
				// `generate_useful_gather_paths(root, joinrel, false)` — the
				// GEQO arm's counterpart of the DP arm's per-level call
				// (joinsearchlevel.go). Same ordering reason: a Gather path
				// offered after set_cheapest could never be CheapestTotal.
				s.generateUsefulGatherPaths(joinrel, false)
				setCheapest(joinrel)
				if s.geqoProbe {
					geqoProbeJoin(joinrel)
				}
				// Absorb new clump into old.
				oldClump.joinrel = joinrel
				oldClump.size += newClump.size
				// Remove oldClump from the list.
				clumps = append(clumps[:i], clumps[i+1:]...)
				// Recursively try to merge the enlarged old clump with others.
				return mergeClump(s, clumps, oldClump, numGene, force)
			}
		}
	}
	// No merging possible; add newClump as an independent clump.
	// Size-1 clumps always go at the end (PG fast path).
	if len(clumps) == 0 || newClump.size == 1 {
		return append(clumps, newClump)
	}
	// Insert in size order (larger clumps first).
	pos := 0
	for pos < len(clumps) && clumps[pos].size >= newClump.size {
		pos++
	}
	clumps = append(clumps, nil)
	copy(clumps[pos+1:], clumps[pos:])
	clumps[pos] = newClump
	return clumps
}

// desirableJoin reports whether two relations should be joined (heuristic for
// gimmeTree). PG oracle: desirable_join (geqo_eval.c:324).
func desirableJoin(s *searchCtx, outerRel, innerRel *RelOptInfo) bool {
	if s.clauses != nil && s.clauses.hasRelevantJoinClause(outerRel, innerRel) {
		return true
	}
	if s.joinOrderRestricted(outerRel, innerRel) {
		return true
	}
	return false
}

// --- Edge Recombination Crossover (ERX) ---

// edgeTable is the adjacency list for ERX. One entry per city (1-based).
// edges holds the neighbour list; a negative value marks a "shared" edge
// (present in both parents). unusedEdges is the running count of still-live
// edges; -1 marks a city already incorporated into the tour.
type edgeTable struct {
	edges       [][]int
	totalEdges  []int
	unusedEdges []int
}

// allocEdgeTable allocates the edge table. PG oracle: alloc_edge_table
// (geqo_erx.c:55).
func allocEdgeTable(numGene int) *edgeTable {
	et := &edgeTable{
		edges:       make([][]int, numGene+1), // 1-based
		totalEdges:  make([]int, numGene+1),
		unusedEdges: make([]int, numGene+1),
	}
	for i := range et.edges {
		et.edges[i] = make([]int, 0, 4)
		et.unusedEdges[i] = -1 // 0 is "empty"; -1 is "not yet seen"
	}
	return et
}

func freeEdgeTable(et *edgeTable) {
	et.edges = nil
}

// gimmeEdgeTable builds the edge table from two parent tours.
// PG oracle: gimme_edge_table (geqo_erx.c:94). Shared edges are negated.
func gimmeEdgeTable(momma, daddy []Gene, numGene int, et *edgeTable) {
	// Reset per-city bookkeeping.
	for i := 1; i <= numGene; i++ {
		et.edges[i] = et.edges[i][:0]
		et.totalEdges[i] = 0
		et.unusedEdges[i] = 0
	}

	for i := 0; i < numGene; i++ {
		// Edge from momma[i] to momma[(i+1)%numGene] (circular tour).
		gimmeEdge(et, int(momma[i]), int(momma[(i+1)%numGene]))
		gimmeEdge(et, int(momma[(i+1)%numGene]), int(momma[i]))
		// Edge from daddy[i] to daddy[(i+1)%numGene].
		gimmeEdge(et, int(daddy[i]), int(daddy[(i+1)%numGene]))
		gimmeEdge(et, int(daddy[(i+1)%numGene]), int(daddy[i]))
	}
}

func gimmeEdge(et *edgeTable, city1, city2 int) {
	for i, edge := range et.edges[city1] {
		// PG compares abs(edge): an edge already marked shared stays one
		// entry, however often the parents repeat it (gimme_edge).
		if edge == city2 || edge == -city2 {
			et.edges[city1][i] = -city2
			return
		}
	}
	// New edge.
	et.edges[city1] = append(et.edges[city1], city2)
	et.totalEdges[city1]++
	et.unusedEdges[city1]++
}

// gimmeTour builds the child tour from the edge table.
// PG oracle: gimme_tour (geqo_erx.c:195). Returns edge failure count.
func gimmeTour(kid []Gene, numGene int, et *edgeTable, rng *geqoRNG) int {
	edgeFailures := 0
	// Pick random starting city.
	kid[0] = Gene(rng.randint(1, numGene))

	for i := 1; i < numGene; i++ {
		prev := int(kid[i-1])
		// As each point is entered into the tour, remove it from the edge table.
		removeGene(et, prev)

		// Find destination for the newly entered point.
		if et.unusedEdges[prev] > 0 {
			kid[i] = Gene(gimmeGene(et, prev, rng))
		} else {
			// Cope with fault.
			edgeFailures++
			kid[i] = Gene(edgeFailure(et, kid, i-1, numGene, rng))
		}

		// Mark this node as incorporated.
		et.unusedEdges[prev] = -1
	}
	return edgeFailures
}

// removeGene removes the input gene from the edge table.
// PG oracle: remove_gene (geqo_erx.c:239).
func removeGene(et *edgeTable, gene int) {
	for i := 0; i < et.unusedEdges[gene]; i++ {
		possessEdge := et.edges[gene][i]
		if possessEdge < 0 {
			possessEdge = -possessEdge
		}
		genesRemaining := et.unusedEdges[possessEdge]
		// Find the input gene in this neighbour's edge list and delete it.
		for j := 0; j < genesRemaining; j++ {
			e := et.edges[possessEdge][j]
			if e < 0 {
				e = -e
			}
			if e == gene {
				et.unusedEdges[possessEdge]--
				et.edges[possessEdge][j] = et.edges[possessEdge][genesRemaining-1]
				break
			}
		}
	}
}

// gimmeGene picks the next gene using edge table priorities: shared edges
// first, then candidates with fewest remaining unused edges (random tie-break).
// PG oracle: gimme_gene (geqo_erx.c:281).
func gimmeGene(et *edgeTable, city int, rng *geqoRNG) int {
	// Consider candidate destination points in this city's edge list.
	// Priority 1: shared edges (negative values).
	for i := 0; i < et.unusedEdges[city]; i++ {
		friend := et.edges[city][i]
		if friend < 0 {
			return -friend
		}
	}

	// Priority 2: candidates with fewest remaining unused edges.
	minimumEdges := 5 // no point has edges to more than 4 other points
	minimumCount := 0
	for i := 0; i < et.unusedEdges[city]; i++ {
		friend := et.edges[city][i]
		if et.unusedEdges[friend] < minimumEdges {
			minimumEdges = et.unusedEdges[friend]
			minimumCount = 1
		} else if et.unusedEdges[friend] == minimumEdges {
			minimumCount++
		}
	}

	// Random decision among the candidates with the minimum edge count.
	randDecision := rng.randint(0, minimumCount-1)
	for i := 0; i < et.unusedEdges[city]; i++ {
		friend := et.edges[city][i]
		if et.unusedEdges[friend] == minimumEdges {
			minimumCount--
			if minimumCount == randDecision {
				return friend
			}
		}
	}
	// ... should never be reached
	return 0
}

// edgeFailure handles an edge failure: randomly pick among genes with
// remaining edges, preferring those with total_edges == 4; on the last point
// pick the first gene not yet used. PG oracle: edge_failure (geqo_erx.c:371).
func edgeFailure(et *edgeTable, gene []Gene, index, numGene int, rng *geqoRNG) int {
	failGene := int(gene[index])
	remainingEdges := 0
	fourCount := 0

	// How many edges remain? How many genes with four total (initial) edges
	// remain?
	for i := 1; i <= numGene; i++ {
		if et.unusedEdges[i] != -1 && i != failGene {
			remainingEdges++
			if et.totalEdges[i] == 4 {
				fourCount++
			}
		}
	}

	// Random decision of the gene with remaining edges and total_edges == 4.
	if fourCount != 0 {
		randDecision := rng.randint(0, fourCount-1)
		for i := 1; i <= numGene; i++ {
			if i != failGene && et.unusedEdges[i] != -1 && et.totalEdges[i] == 4 {
				fourCount--
				if randDecision == fourCount {
					return i
				}
			}
		}
	} else if remainingEdges != 0 {
		// Random decision of the gene with remaining edges.
		randDecision := rng.randint(0, remainingEdges-1)
		for i := 1; i <= numGene; i++ {
			if i != failGene && et.unusedEdges[i] != -1 {
				remainingEdges--
				if randDecision == remainingEdges {
					return i
				}
			}
		}
	} else {
		// Occurs only at the last point in the tour; simply look for the
		// point which is not yet used.
		for i := 1; i <= numGene; i++ {
			if et.unusedEdges[i] >= 0 {
				return i
			}
		}
	}
	// ... should never be reached
	return 0
}

// --- PRNG ---

// geqoRNG is GEQO's private generator, GeqoPrivateData.random_state:
// PostgreSQL's pg_prng (M0146-0214).
type geqoRNG struct {
	state pgprng.State
}

// newGeqoRNG mirrors geqo_set_seed: pg_prng_fseed(&random_state, seed).
func newGeqoRNG(seed float64) *geqoRNG {
	return &geqoRNG{state: pgprng.FSeed(seed)}
}

// rand mirrors geqo_rand: pg_prng_double, a float64 in [0, 1).
func (r *geqoRNG) rand() float64 {
	return r.state.Double()
}

// randint returns an int in [lower, upper] inclusive. It mirrors
// geqo_randint(root, upper, lower), whose arguments PG passes upper first:
// pg_prng_uint64_range(lower, upper). A one-value range consumes no draw.
// PG's callers never pass a negative bound.
func (r *geqoRNG) randint(lower, upper int) int {
	return int(r.state.Uint64Range(uint64(lower), uint64(upper)))
}
