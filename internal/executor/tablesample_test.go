package executor

import (
	"math"
	"testing"
)

// TestTableSamplerMatchesOracle pins the two built-in samplers against the
// row sets PostgreSQL 18.3 actually produces, taken verbatim from
// postgres/src/test/regress/expected/tablesample.out.
//
// Why this test can be exact rather than statistical: SYSTEM and BERNOULLI are
// not PRNG streams. Both compute hash_any() over a small uint32 array and
// compare against a cutoff, so a given (block, offset, seed) either is or is
// not in the sample, on every machine, forever. That is the property upstream
// relies on to pin these expectations at all, and it is what makes a
// divergence here a real bug rather than noise.
//
// The fixture is `test_tablesample`: 10 rows of ~224 bytes created
// `WITH (fillfactor=10)`, which upstream packs 3 to a page, giving blocks
// [0,1,2] [3,4,5] [6,7,8] [9]. goopg does not honour fillfactor when choosing
// an insert page (M0134-0175a), so the live table has ONE block and the
// end-to-end row sets still differ — but the sampler itself is exact, and this
// test is what proves the two facts are independent.
func TestTableSamplerMatchesOracle(t *testing.T) {
	// Upstream's page layout for the fixture: block -> ids at offsets 1..n.
	blocks := [][]int{{0, 1, 2}, {3, 4, 5}, {6, 7, 8}, {9}}

	collect := func(s tableSampler) []int {
		var got []int
		blk := uint32(0)
		for {
			next, ok := s.nextSampleBlock(blk, uint32(len(blocks)))
			if !ok {
				return got
			}
			for off := 1; off <= len(blocks[next]); off++ {
				if s.sampleTuple(next, uint16(off)) {
					got = append(got, blocks[next][off-1])
				}
			}
			blk = next + 1
		}
	}

	cases := []struct {
		method  string
		percent float64
		seed    uint32
		want    []int // expected/tablesample.out
	}{
		// SELECT id FROM test_tablesample TABLESAMPLE SYSTEM (50) REPEATABLE (0);
		{"system", 50, 0, []int{3, 4, 5, 6, 7, 8}},
		// SELECT id FROM test_tablesample TABLESAMPLE SYSTEM (100.0/11) REPEATABLE (0);
		{"system", 100.0 / 11, 0, nil},
		// SELECT id FROM test_tablesample TABLESAMPLE BERNOULLI (50) REPEATABLE (0);
		{"bernoulli", 50, 0, []int{4, 5, 6, 7, 8}},
		// SELECT id FROM test_tablesample TABLESAMPLE BERNOULLI (5.5) REPEATABLE (0);
		{"bernoulli", 5.5, 0, []int{7}},
		// 100% must admit every row for either method, at any seed.
		{"system", 100, 0, []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}},
		{"bernoulli", 100, 0, []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}},
		{"system", 100, 12345, []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}},
		// 0% must admit nothing, likewise.
		{"system", 0, 0, nil},
		{"bernoulli", 0, 0, nil},
	}
	for _, c := range cases {
		s, err := buildTableSampler(c.method, c.percent, c.seed)
		if err != nil {
			t.Fatalf("%s(%v) seed %d: %v", c.method, c.percent, c.seed, err)
		}
		got := collect(s)
		if len(got) != len(c.want) {
			t.Errorf("%s(%v) seed %d: got %v, oracle %v", c.method, c.percent, c.seed, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%s(%v) seed %d: got %v, oracle %v", c.method, c.percent, c.seed, got, c.want)
				break
			}
		}
	}
}

// TestTableSampleSeedFromRepeatable pins the REPEATABLE -> seed conversion.
// REPEATABLE(0) hashing to 0 is the specific property upstream calls out as
// "machine-independent" and is what every REPEATABLE(0) expectation rests on.
func TestTableSampleSeedFromRepeatable(t *testing.T) {
	if got := tsmSeedFromRepeatable(0); got != 0 {
		t.Errorf("REPEATABLE(0) seed = %d, want 0", got)
	}
	// -0 must agree with +0 (hashfloat8's zero short-circuit).
	if got := tsmSeedFromRepeatable(math.Copysign(0, -1)); got != 0 {
		t.Errorf("REPEATABLE(-0) seed = %d, want 0", got)
	}
	// A non-zero seed must actually change the sample, or REPEATABLE would be
	// decorative.
	a, _ := buildTableSampler("bernoulli", 50, tsmSeedFromRepeatable(0))
	b, _ := buildTableSampler("bernoulli", 50, tsmSeedFromRepeatable(1))
	same := true
	for off := uint16(1); off <= 32; off++ {
		if a.sampleTuple(0, off) != b.sampleTuple(0, off) {
			same = false
			break
		}
	}
	if same {
		t.Error("REPEATABLE(0) and REPEATABLE(1) select identical tuples over 32 offsets")
	}
}

// TestTableSamplerRejects pins the four errors the oracle distinguishes, with
// their SQLSTATEs. The method-vs-percentage ORDER matters: upstream resolves
// the method in the parser and the percentage in the executor, so an unknown
// method with a bad percentage reports the method.
func TestTableSamplerRejects(t *testing.T) {
	cases := []struct {
		method  string
		percent float64
		code    string
		msg     string
	}{
		{"foobar", 1, "42704", "tablesample method foobar does not exist"},
		{"foobar", -1, "42704", "tablesample method foobar does not exist"},
		{"bernoulli", -1, "2202H", "sample percentage must be between 0 and 100"},
		{"bernoulli", 200, "2202H", "sample percentage must be between 0 and 100"},
		{"system", -1, "2202H", "sample percentage must be between 0 and 100"},
		{"system", 200, "2202H", "sample percentage must be between 0 and 100"},
		{"system", math.NaN(), "2202H", "sample percentage must be between 0 and 100"},
	}
	for _, c := range cases {
		_, err := buildTableSampler(c.method, c.percent, 0)
		ee, ok := err.(*ExecError)
		if !ok {
			t.Errorf("%s(%v): got %v, want *ExecError", c.method, c.percent, err)
			continue
		}
		if ee.Code != c.code || ee.Message != c.msg {
			t.Errorf("%s(%v): got %s/%q, want %s/%q", c.method, c.percent, ee.Code, ee.Message, c.code, c.msg)
		}
	}
	// The boundaries themselves are legal.
	for _, p := range []float64{0, 100} {
		if _, err := buildTableSampler("system", p, 0); err != nil {
			t.Errorf("system(%v) rejected: %v", p, err)
		}
	}
}
