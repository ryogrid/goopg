package executor

import (
	"os"
	"sync/atomic"

	"github.com/goopg/goopg/internal/planner"
)

// Stage 11 (S3, design bundle D4.3): hashed execution for uncorrelated
// IN / NOT IN sublinks.
//
// Upstream PostgreSQL loads a hashable uncorrelated ANY-SubPlan's output
// into an in-memory hash table once and probes it per outer row
// (buildSubPlanHash, postgres/src/backend/executor/nodeSubplan.c:477;
// ExecHashSubPlan, nodeSubplan.c:101), with a second partial-match table
// preserving NOT IN's three-valued NULL logic. goopg's IN test
// expressions are single-column, so the partial-match table degenerates
// to one bit — "did the inner set contain a NULL" — and the probe truth
// table is:
//
//	operand matches a non-NULL value  → TRUE
//	no match, inner contained a NULL  → NULL (never FALSE)
//	no match, no NULLs                → FALSE
//	(x.Negated inverts TRUE/FALSE; NULL stays NULL)
//
// The hash replaces the per-outer-row LINEAR scan over the cached
// []Datum in evalInExpr; the slice itself survives as the non-hashable
// fallback — goopg's cached slice is its own materialization, so no
// separate Material wrapper node is needed (deliberate divergence from
// subselect.c:530-536, recorded in the design bundle §5.2).
//
// Scope: `x.Plan != nil && x.IsNonCorrelated` plain-equality IN/NOT IN
// only (the PG shape). Correlated INs keep the linear scan over their
// per-correlation-key cached slices; AnyOp/AllOp/NotEqualAny forms use
// non-equality operators and cannot hash.

// hashedSubPlanOn is the operational kill switch. Default ON;
// GOOPG_HASHED_SUBPLAN=off at server start (or
// SetHashedSubPlanEnabled(false) from tests) restores the pure linear
// path. Same pattern as GOOPG_SUBPLAN_RESCAN (subplan.go).
var hashedSubPlanOn atomic.Bool

func init() {
	hashedSubPlanOn.Store(os.Getenv("GOOPG_HASHED_SUBPLAN") != "off")
}

// SetHashedSubPlanEnabled toggles the hashed IN probe. Test-only API;
// the operational switch is the environment variable read at init.
func SetHashedSubPlanEnabled(on bool) { hashedSubPlanOn.Store(on) }

func hashedSubPlanEnabled() bool { return hashedSubPlanOn.Load() }

// subPlanHashFamily classifies datum kinds into families inside which
// datumKey equality provably agrees with compareEq equality — the
// correctness condition for replacing the linear compareEq scan with a
// key lookup.
//
// The hash-safe families are deliberately narrow:
//
//   - hashFamNumeric: KindInt and int64-mantissa KindNumeric.
//     datumKey routes both through canonicalNumericKey, whose
//     trailing-zero normalisation matches compareDatum's scale-aware
//     equality (`1` == `1.0` == `1.00`). Big-mantissa numerics
//     (flagBigNumeric) are excluded: datumKey would read the lossy
//     int64 lane and collide distinct values.
//   - hashFamString: KindString only. The arena variants are NOT safe —
//     datumKey has no arena case and would collapse every arena string
//     into one `k:<kind>` bucket.
//   - hashFamBytes / hashFamBool / hashFamTime: exact-value kinds whose
//     datumKey is injective (Time uses UnixNano; compareEq uses
//     Time.Equal — same instant relation).
//
// Everything else (enum-vs-string label coercion, int-vs-string
// coercion, toast pointers, intervals, floats-in-string, arena kinds)
// returns hashFamNone and takes the linear path, where compareEq's
// coercion rules apply unchanged. A cross-family operand/value mix
// (e.g. `5 IN ('5', ...)` — compareEq coerces, datumKey does not) is
// rejected by the family-equality check at probe time.
type subPlanHashFamily int8

const (
	hashFamNone subPlanHashFamily = iota
	hashFamNumeric
	hashFamString
	hashFamBytes
	hashFamBool
	hashFamTime
)

func subPlanHashFamilyOf(d Datum) subPlanHashFamily {
	switch d.Kind {
	case KindInt:
		return hashFamNumeric
	case KindNumeric:
		if d.Flags&flagBigNumeric != 0 {
			return hashFamNone
		}
		return hashFamNumeric
	case KindString:
		return hashFamString
	case KindBytes:
		return hashFamBytes
	case KindBool:
		return hashFamBool
	case KindTime:
		return hashFamTime
	}
	return hashFamNone
}

// subPlanHash is the hashed form of one uncorrelated IN-sublink's value
// set. family == hashFamNone marks the "unhashable" sentinel: the value
// set mixes families or contains a kind outside the safe set (or the
// inner plan is volatile/LockRows-bearing) — cached so the build is not
// re-attempted per outer row.
type subPlanHash struct {
	set     map[string]struct{}
	hasNull bool
	family  subPlanHashFamily
}

// subPlanHashKeySuffix distinguishes hash entries from the value-slice
// entries they shadow in the same kvcache store. Sharing the store (and
// key stem) is deliberate: the hash inherits the values' lifetime,
// LRU/budget pressure, and the scoped store's clear-on-depth-change
// guard, so it can never outlive the slice it was derived from under a
// mistrusted IsNonCorrelated flag (see subq_cache.go).
const subPlanHashKeySuffix = "\x00hash"

func buildSubPlanHash(values []Datum) *subPlanHash {
	h := &subPlanHash{set: make(map[string]struct{}, len(values))}
	fam := hashFamNone
	for _, v := range values {
		if v.IsNull() {
			h.hasNull = true
			continue
		}
		f := subPlanHashFamilyOf(v)
		if f == hashFamNone || (fam != hashFamNone && f != fam) {
			// Unhashable kind or mixed families → sentinel.
			return &subPlanHash{family: hashFamNone}
		}
		fam = f
		h.set[datumKey(v)] = struct{}{}
	}
	if fam == hashFamNone {
		// Only NULLs (or nothing) — probing needs no set, but a
		// family is required for the operand check; all-NULL sets
		// answer identically for every operand family, so tag with
		// the operand-agnostic marker via hasNull handling at probe
		// time. Keep it simple: mark unhashable; the linear loop
		// over an all-NULL slice is O(n) of pure IsNull checks.
		return &subPlanHash{family: hashFamNone}
	}
	h.family = fam
	return h
}

// subPlanHashSize estimates the resident bytes of a built hash for
// budget accounting (keys + map bookkeeping; same spirit as
// subqResultSize).
func subPlanHashSize(key string, h *subPlanHash) int64 {
	const perEntryOverhead = 96
	const perKeyOverhead = 48
	n := int64(len(key)) + perEntryOverhead
	for k := range h.set {
		n += int64(len(k)) + perKeyOverhead
	}
	return n
}

// evalInHashProbe answers a plain-equality IN / NOT IN over an
// uncorrelated subquery from a hashed value set. Returns (result, true)
// when the probe served the answer; (Datum{}, false) means the caller
// must fall back to the linear scan (switch off, correlated sublink,
// unhashable kinds, cross-family coercion, empty set, or budget
// pressure kept the hash from being cached).
//
// Precondition: operand is non-NULL (evalInExpr handles the NULL
// operand before reaching the plain-equality branch).
func evalInHashProbe(x *planner.InExpr, operand Datum, values []Datum, ctx *Context) (Datum, bool) {
	if !hashedSubPlanEnabled() || ctx == nil || x.Plan == nil || !x.IsNonCorrelated || len(values) == 0 {
		return Datum{}, false
	}
	opFam := subPlanHashFamilyOf(operand)
	if opFam == hashFamNone {
		return Datum{}, false
	}
	key := nonCorrelatedCacheKey(x) + subPlanHashKeySuffix
	// The hash lives in the SCOPED store alongside the constant-key
	// value slice it is derived from (see collectInValues: uncorrelated
	// sublinks always use the scoped store because IsNonCorrelated is
	// only trustworthy after lowering verified it).
	store := ctx.subqCacheStore(true)
	var h *subPlanHash
	if v, ok := store.Get(key); ok {
		h = v.(*subPlanHash)
	} else {
		// Volatile / LockRows-bearing inners are never hashed: their
		// cached slice may be LRU-evicted and recomputed to a
		// DIFFERENT set, which would strand a stale hash. (The slice
		// cache itself keeps InitPlan once-per-statement semantics;
		// the hash must not weaken re-execution correctness under
		// eviction.) Cached as the unhashable sentinel so the
		// volatility walk runs once, not per outer row.
		if !subPlanResultCacheable(ctx, x, x.Plan, false) {
			h = &subPlanHash{family: hashFamNone}
		} else {
			h = buildSubPlanHash(values)
		}
		store.Put(key, h, subPlanHashSize(key, h))
		// A failed Put (budget pressure) just means the next row
		// rebuilds — same order of work as one linear scan.
	}
	if h.family == hashFamNone || h.family != opFam {
		return Datum{}, false
	}
	if _, hit := h.set[datumKey(operand)]; hit {
		return NewBoolDatum(!x.Negated), true
	}
	if h.hasNull {
		return NullDatum, true
	}
	return NewBoolDatum(x.Negated), true
}
