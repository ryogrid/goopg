package executor

import "math"

// M0138-0002: a faithful port of PostgreSQL's two-stage ANALYZE sampling
// mechanism — `BlockSampler`/`ReservoirState` (postgres/src/backend/utils/misc/sampling.c)
// driven by `acquire_sample_rows` (postgres/src/backend/commands/analyze.c:1199).
//
// Before this task, `analyzeRelationWith` scanned EVERY block of the
// relation and reservoir-sampled with the classic Algorithm R (one
// `rng.Int63n(seen+1)` draw per row past the cap) — see the M0138-0001
// census (docs/design/0100-0149/m0138-0001-analyze-divergence-census.md).
// PG instead (1) picks up to `targrows` BLOCKS at random via Knuth's
// Algorithm S (`BlockSampler_Init`/`_Next`), then (2) reservoir-samples
// ROWS within only those blocks via Vitter's Algorithm Z
// (`reservoir_init_selection_state`/`reservoir_get_next_S`). Per the owner's
// Q2 decision (AGENT.md "Plan-parity harness", 2026-09-14 — "goopg must
// reproduce PG's estimates, errors included"), the block-representation gap
// is itself a PG-incompatibility, and the fix is to run PG's exact
// algorithm — not merely something "similarly random".
//
// Both PG algorithms are driven by PG's own PRNG (`pg_prng_state`, a
// xoroshiro128** generator, postgres/src/common/pg_prng.c) rather than
// libc's `random()`. The floating-point recurrences in Algorithm Z
// (`reservoir_get_next_S`'s Algorithm X/Z body) are sensitive to exactly
// which uniform-random stream feeds them, so this file ports pg_prng.c's
// xoroshiro128** verbatim rather than substituting Go's math/rand — the
// goal is PG's *algorithm*, run start to finish with PG's own arithmetic,
// not merely "a" two-stage sampler that happens to look similar.
//
// Seeding: PG draws two independent uint32 seeds from the process-wide
// `pg_global_prng_state` — one for the block sampler, one for the row
// reservoir (analyze.c:1225,1232). goopg has no equivalent process-wide
// generator; `analyzeRelationWith`'s caller-supplied `rng` (seeded via
// `analyzeSeedFor`, mixing `GOOPG_ANALYZE_SEED` with the relation OID, or a
// wall-clock draw when unset) plays that role instead — it is consulted
// exactly twice, to seed the two ported sub-generators below. This keeps
// the existing determinism knob (a fixed `GOOPG_ANALYZE_SEED` reproduces
// the identical sampled TID set on a static relation) while the sampling
// arithmetic downstream of those two seeds is PG's own.

// pgPRNGState mirrors PostgreSQL's `pg_prng_state`
// (postgres/src/include/common/pg_prng.h): the two 64-bit words of the
// xoroshiro128** state vector.
type pgPRNGState struct {
	s0, s1 uint64
}

// splitmix64Next mirrors pg_prng.c's static splitmix64(), used only to
// derive an initial xoroshiro128** state from a 64-bit seed.
func splitmix64Next(state *uint64) uint64 {
	*state += 0x9E3779B97f4A7C15
	val := *state
	val = (val ^ (val >> 30)) * 0xBF58476D1CE4E5B9
	val = (val ^ (val >> 27)) * 0x94D049BB133111EB
	return val ^ (val >> 31)
}

// pgPRNGSeed mirrors pg_prng_seed()/pg_prng_seed_check(): fills the state
// vector via two splitmix64 draws, then substitutes Knuth's LCG constants
// if that chanced to produce all-zeroes (a fixed point of xoroshiro128**).
func pgPRNGSeed(seed uint64) pgPRNGState {
	var st pgPRNGState
	st.s0 = splitmix64Next(&seed)
	st.s1 = splitmix64Next(&seed)
	if st.s0 == 0 && st.s1 == 0 {
		st.s0 = 0x5851F42D4C957F2D
		st.s1 = 0x14057B7EF767814F
	}
	return st
}

func rotl64(x uint64, bits uint) uint64 {
	return (x << bits) | (x >> (64 - bits))
}

// next mirrors pg_prng.c's static xoroshiro128ss(): one 64-bit draw,
// advancing the state vector.
func (s *pgPRNGState) next() uint64 {
	s0 := s.s0
	sx := s.s1 ^ s0
	val := rotl64(s0*5, 7) * 9
	s.s0 = rotl64(s0, 24) ^ sx ^ (sx << 16)
	s.s1 = rotl64(sx, 37)
	return val
}

// double mirrors pg_prng_double(): a uniform draw in [0, 1) using the top
// 52 bits of the 64-bit word, matching PG's assumed double mantissa width.
func (s *pgPRNGState) double() float64 {
	v := s.next()
	return math.Ldexp(float64(v>>(64-52)), -52)
}

// samplerRandomFract mirrors sampling.c's sampler_random_fract(): a
// uniform draw in (0, 1) — pg_prng_double can return exactly 0.0, which
// Algorithm Z's log() calls cannot tolerate, so PG rejects and redraws.
func samplerRandomFract(s *pgPRNGState) float64 {
	for {
		v := s.double()
		if v != 0.0 {
			return v
		}
	}
}

// blockSampler mirrors sampling.c's BlockSamplerData / BlockSampler_Init /
// BlockSampler_HasMore / BlockSampler_Next — Knuth's Algorithm S (TAOCP
// 3.4.2), selecting up to `n` block numbers uniformly at random out of `N`
// without replacement, in increasing physical order.
type blockSampler struct {
	N    uint32 // total blocks in the relation (BlockSampler.N)
	n    int    // blocks wanted (BlockSampler.n == targrows/sampleCap)
	t    uint32 // blocks scanned so far (BlockSampler.t)
	m    int    // blocks selected so far (BlockSampler.m)
	rand pgPRNGState
}

func newBlockSampler(nblocks uint32, samplesize int, seed uint64) *blockSampler {
	return &blockSampler{N: nblocks, n: samplesize, rand: pgPRNGSeed(seed)}
}

func (bs *blockSampler) hasMore() bool {
	return bs.t < bs.N && bs.m < bs.n
}

// next mirrors BlockSampler_Next (sampling.c:63-116) exactly, including the
// "reduce V instead of redrawing" optimisation in the comment there.
func (bs *blockSampler) next() uint32 {
	K := int64(bs.N) - int64(bs.t) // remaining blocks
	k := bs.n - bs.m               // blocks still to sample

	if int64(k) >= K {
		// need all the rest
		bs.m++
		r := bs.t
		bs.t++
		return r
	}

	V := samplerRandomFract(&bs.rand)
	p := 1.0 - float64(k)/float64(K)
	for V < p {
		// skip
		bs.t++
		K--
		p *= 1.0 - float64(k)/float64(K)
	}

	// select
	bs.m++
	r := bs.t
	bs.t++
	return r
}

// reservoirState mirrors sampling.c's ReservoirStateData /
// reservoir_init_selection_state / reservoir_get_next_S — Vitter's
// Algorithm Z ("Random sampling with a reservoir", ACM TOMS 11:1, 1985),
// which computes a skip-count S rather than testing every row.
type reservoirState struct {
	rand pgPRNGState
	W    float64
}

func newReservoirState(n int, seed uint64) *reservoirState {
	rs := &reservoirState{rand: pgPRNGSeed(seed)}
	rs.W = math.Exp(-math.Log(samplerRandomFract(&rs.rand)) / float64(n))
	return rs
}

// getNextS mirrors reservoir_get_next_S (sampling.c:146-226) line for
// line — the magic constant 22.0 is "T" from Vitter's paper, unchanged from
// upstream. t is the number of rows already processed (PG's `samplerows`
// BEFORE this call, per analyze.c:1291's comment); n is targrows/sampleCap.
func (rs *reservoirState) getNextS(t float64, n int) float64 {
	var S float64
	if t <= 22.0*float64(n) {
		// Algorithm X, used until t is large enough for Z's approximations.
		V := samplerRandomFract(&rs.rand)
		S = 0
		t += 1
		quot := (t - float64(n)) / t
		for quot > V {
			S += 1
			t += 1
			quot *= (t - float64(n)) / t
		}
		return S
	}

	// Algorithm Z.
	W := rs.W
	term := t - float64(n) + 1
	for {
		U := samplerRandomFract(&rs.rand)
		X := t * (W - 1.0)
		S = math.Floor(X)
		tmp := (t + 1) / term
		lhs := math.Exp(math.Log(((U*tmp*tmp)*(term+S))/(t+X)) / float64(n))
		rhs := (((t + X) / (term + S)) * term) / t
		if lhs <= rhs {
			W = rhs / lhs
			break
		}
		y := (((U * (t + 1)) / term) * (t + S + 1)) / (t + X)
		var denom, numerLim float64
		if float64(n) < S {
			denom = t
			numerLim = term + S
		} else {
			denom = t - float64(n) + S
			numerLim = t + 1
		}
		for numer := t + S; numer >= numerLim; numer -= 1 {
			y *= numer / denom
			denom -= 1
		}
		W = math.Exp(-math.Log(samplerRandomFract(&rs.rand)) / float64(n)) // Generate W in advance
		if math.Exp(math.Log(y)/float64(n)) <= (t+X)/t {
			break
		}
	}
	rs.W = W
	return S
}
