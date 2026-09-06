package optimizer

import (
	"fmt"
	"math"
	"sync/atomic"
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
// PG oracle: init_tour (geqo_recombination.c:33) — inside-out Fisher-Yates.
func initTour(tour []Gene, rng *geqoRNG) {
	n := len(tour)
	for i := 0; i < n; i++ {
		j := rng.randint(0, i)
		tour[i] = tour[j]
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
	if ps <= 0 {
		ps = poolSize(nrels, effort)
	}
	gens := numberGenerations(ps, s.cp.geqoGenerations)

	// PRNG: geqo_seed controls reproducibility (geqo_main.c). take2 P3-10 —
	// the planner DOES have the session in scope now, so this reads the GUC
	// rather than a fixed 0. PG's default of 0 keeps today's behaviour, which
	// is what makes this change plan-neutral at the defaults.
	rng := newGeqoRNG(geqoSeedState(s.cp.geqoSeed))

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
// (geqo_pool.c:134).
func sortPool(pool *Pool) {
	for i := 1; i < pool.size; i++ {
		for j := i; j > 0 && pool.data[j].worth < pool.data[j-1].worth; j-- {
			pool.data[j], pool.data[j-1] = pool.data[j-1], pool.data[j]
		}
	}
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
func linearRand(max int, bias float64, rng *geqoRNG) int {
	for {
		sqrtVal := bias*bias - 4*(bias-1)*rng.rand()
		if sqrtVal < 0 {
			continue
		}
		index := int((bias - math.Sqrt(sqrtVal)) / (2 * (bias - 1)) * float64(max))
		if index < 0 || index >= max {
			continue
		}
		return index
	}
}

// geqoEval evaluates the fitness of one tour. Returns the cheapest path cost,
// or math.MaxFloat64 if the tour is invalid. PG oracle: geqo_eval
// (geqo_eval.c:56). It runs in a FRESH context per evaluation (PG's temporary
// memory context + join_rel_list truncation): `makeJoinRel`'s find-or-create
// must not see joinrels built by a previous tour's gimmeTree, or the second
// evaluation would price the first's stale paths.
func geqoEval(s *searchCtx, tour []Gene, numGene int) float64 {
	eval := s.freshEvalCtx()
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
				s.generateUsefulGatherPaths(joinrel)
				setCheapest(joinrel)
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
		if edge == city2 {
			// Present as positive — mark as shared (negate).
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

// geqoRNG is a seeded PRNG for GEQO.
type geqoRNG struct {
	state uint64
}

// geqoSeedState maps geqo_seed, a double in [0,1], onto the integer state
// goopg's PRNG takes. PG seeds its own generator from the double directly
// (geqo_set_seed -> pg_prng_fseed); the mapping matters only in that a given
// seed must reproduce a given plan, not that it match PG's bit-for-bit.
//
// 0 — PG's default — returns 0 so newGeqoRNG keeps its existing fixed state,
// which is what makes take2 P3-10 plan-neutral at the defaults.
func geqoSeedState(seed float64) int64 {
	if seed <= 0 {
		return 0
	}
	if seed >= 1 {
		seed = 1
	}
	return int64(seed * float64(math.MaxInt64/2))
}

func newGeqoRNG(seed int64) *geqoRNG {
	rng := &geqoRNG{}
	if seed != 0 {
		rng.state = uint64(seed)
	} else {
		rng.state = 1 // default seed
	}
	return rng
}

// rand returns a float64 in [0, 1).
func (r *geqoRNG) rand() float64 {
	r.state = r.state*6364136223846793005 + 1442695040888963407
	return float64(r.state>>33) / float64(1<<31)
}

// randint returns an int in [lower, upper] inclusive.
func (r *geqoRNG) randint(lower, upper int) int {
	if lower >= upper {
		return lower
	}
	return lower + int(r.rand()*float64(upper-lower+1))
}