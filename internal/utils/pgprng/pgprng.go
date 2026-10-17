// Package pgprng is PostgreSQL's pseudo-random number generator,
// postgres/src/common/pg_prng.c: xoroshiro128** seeded through splitmix64.
//
// goopg runs PG's own generator wherever a PG algorithm consumes random
// draws and its result must match PG's: the ANALYZE block and row samplers
// (executor) and the GEQO join search (optimizer). The draws themselves are
// part of the algorithm. GEQO with geqo_seed = 0 reproduces PG's tour only
// when it sees PG's exact stream, so this is a verbatim port, not a
// substitute generator (M0146-0214).
package pgprng

import (
	"math"
	"math/bits"
)

// State mirrors pg_prng_state (postgres/src/include/common/pg_prng.h): the
// two 64-bit words of the xoroshiro128** state vector.
type State struct {
	S0, S1 uint64
}

// splitmix64 mirrors pg_prng.c's static splitmix64(), used only to derive an
// initial xoroshiro128** state from a 64-bit seed.
func splitmix64(state *uint64) uint64 {
	*state += 0x9E3779B97f4A7C15
	val := *state
	val = (val ^ (val >> 30)) * 0xBF58476D1CE4E5B9
	val = (val ^ (val >> 27)) * 0x94D049BB133111EB
	return val ^ (val >> 31)
}

// Seed mirrors pg_prng_seed() followed by pg_prng_seed_check(): two
// splitmix64 draws fill the state, and an all-zero state (a fixed point of
// xoroshiro128**) is replaced by Knuth's LCG constants.
func Seed(seed uint64) State {
	var st State
	st.S0 = splitmix64(&seed)
	st.S1 = splitmix64(&seed)
	if st.S0 == 0 && st.S1 == 0 {
		st.S0 = 0x5851F42D4C957F2D
		st.S1 = 0x14057B7EF767814F
	}
	return st
}

// FSeed mirrors pg_prng_fseed(): a double seed in [-1, 1] is scaled by
// 2^52 - 1 (about the double's mantissa width; the sign contributes too),
// truncated to int64 as C's conversion does, and then reinterpreted as uint64.
// geqo_set_seed and setseed() seed through it.
func FSeed(fseed float64) State {
	seed := int64(float64((uint64(1)<<52)-1) * fseed)
	return Seed(uint64(seed))
}

func rotl(x uint64, k uint) uint64 {
	return (x << k) | (x >> (64 - k))
}

// Uint64 mirrors pg_prng_uint64() (the static xoroshiro128ss): one 64-bit
// draw, advancing the state.
func (s *State) Uint64() uint64 {
	s0 := s.S0
	sx := s.S1 ^ s0
	val := rotl(s0*5, 7) * 9
	s.S0 = rotl(s0, 24) ^ sx ^ (sx << 16)
	s.S1 = rotl(sx, 37)
	return val
}

// Uint64Range mirrors pg_prng_uint64_range(): a uniform draw in
// [rmin, rmax] by bitmask rejection. The draw is shifted right by
// 63 - pg_leftmost_one_pos64(range), which is the range's leading-zero count,
// and redrawn while it exceeds the range. When rmax <= rmin, it returns rmin
// WITHOUT consuming a draw, as PG does. Callers that reach a one-value range
// (GEQO's init_tour at i = 0) depend on that for the stream to stay in step.
func (s *State) Uint64Range(rmin, rmax uint64) uint64 {
	var val uint64
	if rmax > rmin {
		rng := rmax - rmin
		rshift := uint(bits.LeadingZeros64(rng))
		for {
			val = s.Uint64() >> rshift
			if val <= rng {
				break
			}
		}
	}
	return rmin + val
}

// Double mirrors pg_prng_double(): a uniform draw in [0, 1) from the top 52
// bits of one 64-bit draw, ldexp(v >> 12, -52).
func (s *State) Double() float64 {
	v := s.Uint64()
	return math.Ldexp(float64(v>>(64-52)), -52)
}
