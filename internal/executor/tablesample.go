// tablesample.go — the SYSTEM and BERNOULLI table-sampling methods.
//
// Port of postgres/src/backend/access/tablesample/{system.c,bernoulli.c} plus
// the seed derivation in postgres/src/backend/executor/nodeSamplescan.c:270.
// M0134-0175.
//
// Both built-in methods are DETERMINISTIC HASH functions, not PRNG streams:
// upstream computes hash_any() over a small array of uint32s and compares the
// result against a cutoff derived from the requested percentage. That is the
// property that makes `REPEATABLE (n)` reproduce byte-identical row sets
// across machines — and the reason this port can match the oracle exactly
// rather than approximately. goopg already carries the same Jenkins hash as
// pgHashBytesExtended (hash_partition.go), whose low 32 bits are hash_any's
// result, so no new hash primitive is needed.
package executor

import (
	"encoding/binary"
	"fmt"
	"math"
	"math/rand/v2"

	"github.com/goopg/goopg/internal/optimizer"
)

// tableSampler is the goopg equivalent of upstream's TsmRoutine callback pair
// (NextSampleBlock / NextSampleTuple). The two built-in methods each implement
// exactly one half of it and leave the other a pass-through, which is why
// upstream lets NextSampleBlock be NULL for BERNOULLI: a nil block callback
// means "scan every block".
type tableSampler interface {
	// nextSampleBlock returns the first block at or after `from` that is in
	// the sample, or ok=false once the relation is exhausted. BERNOULLI
	// returns `from` unchanged (it samples within every block).
	nextSampleBlock(from, nblocks uint32) (blk uint32, ok bool)
	// sampleTuple reports whether the tuple at (blockno, offset) — offset
	// being a 1-based OffsetNumber, as upstream's OffsetNumber is — belongs
	// to the sample. SYSTEM returns true for every tuple in a block it
	// already accepted.
	sampleTuple(blockno uint32, offset uint16) bool
	// method is the name EXPLAIN prints on the "Sampling:" line.
	method() string
}

// tsmCutoff is the shared cutoff computation of system_beginsamplescan
// (system.c) and bernoulli_beginsamplescan (bernoulli.c) — they are
// character-for-character identical upstream, including the error.
//
// The cutoff is the sample probability times (PG_UINT32_MAX + 1), stored as a
// uint64 so that 100% is representable; upstream notes this "gives strictly
// correct behavior at the limits of zero or one probability", i.e. 0% admits
// nothing and 100% admits everything, with no off-by-one at either end.
func tsmCutoff(percent float64) (uint64, error) {
	if percent < 0 || percent > 100 || math.IsNaN(percent) {
		return 0, &ExecError{Code: "2202H", Message: "sample percentage must be between 0 and 100"}
	}
	return uint64(math.RoundToEven(float64(math.MaxUint32+1) * percent / 100)), nil
}

// tsmHashAny is hash_any(k, len) — hash_bytes in common/hashfn.c, whose result
// is the low 32 bits of hash_bytes_extended with seed 0.
func tsmHashAny(words []uint32) uint32 {
	buf := make([]byte, 4*len(words))
	for i, w := range words {
		binary.LittleEndian.PutUint32(buf[4*i:], w)
	}
	return uint32(pgHashBytesExtended(buf, 0))
}

// systemSampler is SYSTEM — block-level sampling. Every tuple of an accepted
// block is returned, which is why SYSTEM at a given percentage is far
// "clumpier" than BERNOULLI at the same percentage.
type systemSampler struct {
	cutoff uint64
	seed   uint32
}

func (s *systemSampler) method() string { return "system" }

// nextSampleBlock is system_nextsampleblock (system.c). Upstream keeps the
// cursor in sampler->nextblock; goopg passes it in as `from` because the scan
// loop already owns a block cursor.
func (s *systemSampler) nextSampleBlock(from, nblocks uint32) (uint32, bool) {
	for blk := from; blk < nblocks; blk++ {
		if uint64(tsmHashAny([]uint32{blk, s.seed})) < s.cutoff {
			return blk, true
		}
	}
	return 0, false
}

// sampleTuple is system_nextsampletuple, which walks every offset on the page
// unconditionally — the block decision was already made.
func (s *systemSampler) sampleTuple(uint32, uint16) bool { return true }

// bernoulliSampler is BERNOULLI — tuple-level sampling. Each tuple is admitted
// independently with the requested probability, so it visits every block.
type bernoulliSampler struct {
	cutoff uint64
	seed   uint32
}

func (b *bernoulliSampler) method() string { return "bernoulli" }

// nextSampleBlock: upstream sets NextSampleBlock = NULL for BERNOULLI, which
// the scan reads as "no block filtering".
func (b *bernoulliSampler) nextSampleBlock(from, nblocks uint32) (uint32, bool) {
	if from >= nblocks {
		return 0, false
	}
	return from, true
}

// sampleTuple is bernoulli_nextsampletuple (bernoulli.c). Upstream hashes an
// array of THREE uint32s — block, offset, seed, in that order — so the byte
// image fed to hash_any is 12 bytes, not 8; getting the word order or the
// length wrong yields a plausible-looking but different sample.
func (b *bernoulliSampler) sampleTuple(blockno uint32, offset uint16) bool {
	return uint64(tsmHashAny([]uint32{blockno, uint32(offset), b.seed})) < b.cutoff
}

// Note on the comparison: upstream writes `hash < sampler->cutoff` with hash a
// uint32 widened to uint64. The widening is load-bearing — at 100% the cutoff
// is exactly 2^32, which no uint32 can hold, and truncating it to MaxUint32
// would silently drop every tuple whose hash is MaxUint32. Both call sites
// below therefore widen the hash rather than narrowing the cutoff.

// buildTableSampler resolves a method name to its sampler, mirroring
// parse_tablesample_method (parse_clause.c:929): an unknown name is 42704
// "tablesample method X does not exist", NOT a syntax error, so that the caret
// lands on the method name.
func buildTableSampler(method string, percent float64, seed uint32) (tableSampler, error) {
	// Order matters and is not arbitrary: upstream resolves the method name in
	// the PARSER (parse_tablesample_method) and checks the percentage in the
	// EXECUTOR (beginsamplescan), so `TABLESAMPLE FOOBAR (-1)` reports the
	// unknown method, not the bad percentage. Collapsing both into one
	// function here preserves that precedence explicitly.
	if method != "system" && method != "bernoulli" {
		return nil, &ExecError{Code: "42704", Message: "tablesample method " + method + " does not exist"}
	}
	cutoff, err := tsmCutoff(percent)
	if err != nil {
		return nil, err
	}
	if method == "system" {
		return &systemSampler{cutoff: cutoff, seed: seed}, nil
	}
	return &bernoulliSampler{cutoff: cutoff, seed: seed}, nil
}

// tsmSeedFromRepeatable converts a REPEATABLE value into the sampler seed,
// mirroring nodeSamplescan.c:270 `DatumGetUInt32(DirectFunctionCall1(hashfloat8,
// datum))`. Upstream's comment explains the choice: REPEATABLE is float8 at the
// SQL level so it behaves sensibly for users who expect an integer AND for
// users who expect setseed()'s -1..1 float, and hashing it "has the convenient
// property that REPEATABLE(0) gives a machine-independent result" — which is
// exactly what the regress expectations depend on.
//
// This reproduces hashfloat8 (hashfunc.c) rather than calling the SQL builtin:
// zero (and -0) short-circuits to 0, every NaN bit pattern is canonicalised
// first, and the remainder is hash_any over the 8-byte IEEE image.
func tsmSeedFromRepeatable(f float64) uint32 {
	if f == 0 {
		return 0
	}
	if math.IsNaN(f) {
		f = math.NaN()
	}
	var buf [8]byte
	binary.LittleEndian.PutUint64(buf[:], math.Float64bits(f))
	return uint32(pgHashBytesExtended(buf[:], 0))
}

// buildSamplerFromSpec evaluates a resolved TABLESAMPLE clause and returns its
// sampler. Ordering of the checks follows nodeSamplescan.c:230-275 exactly,
// because the regress case distinguishes all of them:
//
//	1. each argument must be non-NULL          -> 2202H, "TABLESAMPLE parameter cannot be null"
//	2. REPEATABLE must be non-NULL             -> 2202G, "TABLESAMPLE REPEATABLE parameter cannot be null"
//	3. the method must exist                   -> 42704
//	4. the percentage must be in [0,100]       -> 2202H
//
// Without REPEATABLE upstream picks a random seed once per scan
// (ExecInitSampleScan, nodeSamplescan.c:154) and reuses it across rescans;
// goopg does the same via the caller-supplied fallback seed.
func buildSamplerFromSpec(spec *optimizer.TableSampleSpec, ctx *Context, fallbackSeed uint32) (tableSampler, error) {
	if spec == nil {
		return nil, nil
	}
	// Upstream requires exactly one float4 argument for both built-in
	// methods (parameterTypes = list_make1_oid(FLOAT4OID)); a different
	// count is "tablesample method X requires N argument(s), not M".
	if len(spec.Args) != 1 {
		return nil, &ExecError{Code: "2202H", Pos: spec.Pos(), Message: fmt.Sprintf(
			"tablesample method %s requires %d argument, not %d", spec.Method, 1, len(spec.Args))}
	}
	d, err := evalExpr(spec.Args[0], nil, ctx)
	if err != nil {
		return nil, err
	}
	if d.Kind == KindNull {
		return nil, &ExecError{Code: "2202H", Message: "TABLESAMPLE parameter cannot be null"}
	}
	percent, ok := datumToFloat64(d)
	if !ok {
		return nil, &ExecError{Code: "2202H", Pos: spec.Pos(),
			Message: "tablesample method " + spec.Method + " requires a numeric argument"}
	}
	seed := fallbackSeed
	if spec.Repeatable != nil {
		rd, err := evalExpr(spec.Repeatable, nil, ctx)
		if err != nil {
			return nil, err
		}
		if rd.Kind == KindNull {
			return nil, &ExecError{Code: "2202G", Message: "TABLESAMPLE REPEATABLE parameter cannot be null"}
		}
		rf, ok := datumToFloat64(rd)
		if !ok {
			return nil, &ExecError{Code: "2202G", Message: "TABLESAMPLE REPEATABLE parameter cannot be null"}
		}
		seed = tsmSeedFromRepeatable(rf)
	}
	s, err := buildTableSampler(spec.Method, percent, seed)
	if err != nil {
		if ee, ok := err.(*ExecError); ok && ee.Code == "42704" {
			ee.Pos = spec.Pos()
		}
		return nil, err
	}
	return s, nil
}

// randSeedForSample supplies the seed for a TABLESAMPLE clause written WITHOUT
// REPEATABLE. Upstream draws it once in ExecInitSampleScan
// (nodeSamplescan.c:154) with `pg_prng_uint32(&pg_global_prng_state)` and
// deliberately does NOT redraw it per rescan, so a sampled scan on the inner
// side of a nested loop returns a consistent sample. goopg keeps that property
// by drawing here, in Open, and holding the sampler for the operator's life.
//
// math/rand/v2 rather than PG's xoroshiro128**: there is nothing to match. A
// seed that is not REPEATABLE is unobservable by definition — the regress
// expectations only ever pin REPEATABLE cases, and `SYSTEM (100)` without
// REPEATABLE is exact for any seed because the cutoff admits every hash.
func randSeedForSample() uint32 { return rand.Uint32() }
