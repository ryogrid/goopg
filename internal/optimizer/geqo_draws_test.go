package optimizer

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// TestGEQODrawsMatchPG pins goopg's GEQO random walk to PG 18.3's
// (M0146-0214). The golden file comes from testdata/geqo_draws_harness.c,
// which compiles PG's own geqo_random.c, geqo_recombination.c,
// geqo_selection.c, geqo_copy.c and geqo_erx.c against the real pg_prng. For
// each seed and problem size it records the random_init_pool tours, then
// twelve rounds of geqo_selection (parents by pool position) and the ERX
// child gimme_tour builds from them. Same seed, same draws, same order: the
// sequences must be identical. That is what lets geqo_seed = 0 reproduce
// PG's tour. Fitness is not involved here; pool positions stand in for
// worth, exactly as in the harness.
func TestGEQODrawsMatchPG(t *testing.T) {
	raw, err := os.ReadFile("testdata/geqo_draws_golden.txt")
	if err != nil {
		t.Fatal(err)
	}
	tourStr := func(tag string, g []Gene) string {
		parts := make([]string, len(g))
		for i, v := range g {
			parts[i] = fmt.Sprint(int(v))
		}
		return tag + strings.Join(parts, ",")
	}
	var got []string
	run := func(seed float64, numGene, poolSize int) {
		rng := newGeqoRNG(seed)
		pool := allocPool(poolSize, numGene)
		got = append(got, fmt.Sprintf("case seed=%g genes=%d pool=%d", seed, numGene, poolSize))
		for i := 0; i < poolSize; i++ {
			initTour(pool.data[i].string, rng)
			pool.data[i].worth = float64(i)
			got = append(got, tourStr("init ", pool.data[i].string))
		}
		momma, daddy := allocChromosome(numGene), allocChromosome(numGene)
		et := allocEdgeTable(numGene)
		for g := 0; g < 12; g++ {
			geqoSelection(pool, 2.0, momma, daddy, rng)
			got = append(got, fmt.Sprintf("sel %d,%d", int(momma.worth), int(daddy.worth)))
			gimmeEdgeTable(momma.string, daddy.string, numGene, et)
			gimmeTour(momma.string, numGene, et, rng)
			got = append(got, tourStr("kid ", momma.string))
		}
	}
	run(0.0, 4, 40)
	run(0.0, 12, 250)
	run(0.0, 18, 250)
	run(0.5, 13, 250)

	want := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(got) != len(want) {
		t.Fatalf("%d lines, golden has %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("line %d: got %q, want %q (first divergence)", i+1, got[i], want[i])
		}
	}
}
