package executor

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/goopg/goopg/internal/optimizer"
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
//   - hashFamNumeric: KindInt and KindNumeric in BOTH mantissa lanes.
//     datumKey routes them through canonicalNumericKey /
//     canonicalBigNumericKey, whose shared trailing-zero normalisation
//     matches compareDatum's scale-aware equality (`1` == `1.0` ==
//     `1.00`) and makes the two lanes converge on one key. (Before
//     R3-3 the big lane was excluded here because datumKey read the
//     lossy int64 accessor — which was also silently dropping pairs in
//     hash JOINS on such columns, the reason that fix landed in
//     datumKey rather than in a probe-local workaround.)
//   - hashFamString: KindString, arena-backed or not. Both resolve
//     through d.StringValue(), and datumKey's `"s:" + …` concatenation
//     copies the bytes into a fresh heap string, so the map key never
//     aliases arena storage. (The pre-M0107-0002 layout had a separate
//     KindStringArena that datumKey did not handle; that Kind no longer
//     exists.) Equality agrees with the linear oracle by construction:
//     compareEq compares two strings with plain `==`, exactly what
//     datumKey's key equality expresses — compareDatum's UUID / pg_lsn
//     / row / array normalisations are ORDERING helpers that the
//     equality path never reaches.
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
		// R3-3: big-mantissa numerics are hashable now that datumKey
		// routes them through canonicalBigNumericKey, which shares the
		// int64 lane's normalisation (so the two lanes converge on one
		// key and the family need not split).
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
func evalInHashProbe(x *optimizer.InExpr, operand Datum, values []Datum, ctx *Context) (Datum, bool) {
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

// ---------------------------------------------------------------------------
// M0146-0015c slice 3 — tuple-key hash for a row-operand ANY sublink
//
// The EXISTS→ANY conversions (keptExistsToAny, and a future widening of the
// flat pass) produce InExpr{Operand: RowExpr{…}, Plan: uncorrelated} whose
// inner set must be materialised once and probed per outer row — upstream's
// ExecHashSubPlan over a multi-column testexpr. goopg's row-IN executor
// (evalRowConstructorInExpr) still re-runs the inner plan per outer row with
// NULL-precise three-valued semantics; this hash serves only the shape the
// conversions emit, where InExpr.UnknownEqFalse licenses a two-valued answer
// and NULL elements can never form a match — which is what lets goopg skip
// PG's partial-match table entirely.
//
// Every decline (unknownEqFalse unset, correlated or unlowered link,
// volatile inner, unhashable or mixed datum families, a width mismatch)
// returns "not served" and the caller falls back to the exact linear path —
// a missed optimisation, never a wrong answer.

// subPlanRowHash is the tuple-key analogue of subPlanHash: inner rows keyed
// by the composite of per-element datumKeys, plus the per-column datum
// family recorded at build so a probe can demand pairwise family equality
// (the same rule the single-column probe applies to its one column).
// unusable marks the cached "do not retry" sentinel: the build ran once and
// found the shape unhashable (mixed/unkeyable families or an uncacheable
// inner) — without it every outer row would pay a rebuild before falling
// back to the linear path.
type subPlanRowHash struct {
	set      map[string]struct{}
	fams     []subPlanHashFamily // per column, only over fully non-NULL rows
	width    int
	unusable bool
}

const subPlanRowHashKeySuffix = "\x00rowhash"

// rowTupleKey joins element datumKeys with explicit length prefixes so a
// string payload can never forge a component boundary (datumKey is free-form
// text: "s:" + payload admits arbitrary bytes).
func rowTupleKey(elems []Datum) string {
	var b strings.Builder
	for _, d := range elems {
		k := datumKey(d)
		b.WriteString(strconv.Itoa(len(k)))
		b.WriteByte(':')
		b.WriteString(k)
		b.WriteByte('\x00')
	}
	return b.String()
}

// buildSubPlanRowHash keys the materialised inner rows. Rows containing a
// NULL element are dropped: under unknownEqFalse they can contribute
// neither TRUE nor a distinguishable NULL answer. A column's family is
// pinned by the first fully non-NULL row that supplies it; a later
// disagreement, or an unkeyable kind, makes the whole set unhashable — the
// coercion rules compareEq applies across kinds stay on the linear path.
func buildSubPlanRowHash(rows [][]Datum, width int) *subPlanRowHash {
	h := &subPlanRowHash{set: make(map[string]struct{}, len(rows)), width: width}
	fams := make([]subPlanHashFamily, width)
	init := false
	for _, r := range rows {
		null := false
		for _, v := range r {
			if v.IsNull() {
				null = true
				break
			}
		}
		if null {
			continue
		}
		for i, v := range r {
			f := subPlanHashFamilyOf(v)
			if f == hashFamNone {
				return &subPlanRowHash{width: width, unusable: true}
			}
			if init && fams[i] != f {
				return &subPlanRowHash{width: width, unusable: true}
			}
			fams[i] = f
		}
		init = true
		h.set[rowTupleKey(r)] = struct{}{}
	}
	h.fams = fams
	return h
}

// subPlanRowHashSize is the resident-byte estimate charged to the shared
// sublink budget (same shape as subPlanHashSize).
func subPlanRowHashSize(key string, h *subPlanRowHash) int64 {
	const perEntryOverhead = 96
	const perKeyOverhead = 48
	n := int64(len(key)) + perEntryOverhead
	for k := range h.set {
		n += int64(len(k)) + perKeyOverhead
	}
	return n
}

// materializeSubPlanRowHash runs the inner plan once and keys its rows.
// Volatile or LockRows-bearing inners return the unusable sentinel instead
// of a set, for the reason evalInHashProbe records on its own sentinel.
func materializeSubPlanRowHash(x *optimizer.InExpr, width int, ctx *Context) (*subPlanRowHash, error) {
	if !subPlanResultCacheable(ctx, x, x.Plan, false) {
		return &subPlanRowHash{width: width, unusable: true}, nil
	}
	op, done, err := acquireSubPlanOp(ctx, x, x.Plan, false)
	if err != nil {
		return nil, err
	}
	defer done()
	var rows [][]Datum
	for {
		slot, err := op.Next()
		if err == EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		r := slotRow(slot)
		if len(r) != width {
			return nil, &ExecError{
				Code: "42601", Pos: x.Pos(),
				Message: fmt.Sprintf("row value has %d columns but subquery has %d columns",
					width, len(r)),
			}
		}
		// Rows outlive their slot — copy (slot.Row() may alias buffers the
		// next op.Next() overwrites).
		rows = append(rows, append([]Datum(nil), r...))
	}
	return buildSubPlanRowHash(rows, width), nil
}

// rowHashFor returns the built tuple hash, building and caching it on the
// first probe. served=false means "fall back to the linear path": the
// recorded unusable sentinel or a width disagreement.
func rowHashFor(x *optimizer.InExpr, width int, ctx *Context) (h *subPlanRowHash, err error, served bool) {
	key := nonCorrelatedCacheKey(x) + subPlanRowHashKeySuffix
	// Scoped store, matching evalInHashProbe's reasoning: the hash
	// shadows data whose IsNonCorrelated flag is only trustworthy until
	// the depth changes, so it shares the scoped store's lifetime guard.
	store := ctx.subqCacheStore(true)
	if v, ok := store.Get(key); ok {
		h = v.(*subPlanRowHash)
		return h, nil, !h.unusable && h.width == width
	}
	h, err = materializeSubPlanRowHash(x, width, ctx)
	if err != nil {
		return nil, err, true
	}
	store.Put(key, h, subPlanRowHashSize(key, h))
	return h, nil, !h.unusable
}

// evalRowHashProbe answers a row-operand uncorrelated ANY sublink from the
// tuple hash. Returns (datum, nil, true) when the probe served the answer;
// (_, nil, false) means fall back to evalRowConstructorInExpr;
// (_, err, true) means the probe took ownership and hit a real error —
// the linear path would fail identically, so the error propagates.
//
// The UnknownEqFalse licence is doing the semantic work: a NULL operand
// element, or an inner tuple that cannot fully match, answers FALSE —
// exactly what the EXISTS this ANY was converted from reports. It is only
// set by conversions that fire exclusively in qual positions.
func evalRowHashProbe(x *optimizer.InExpr, rowOp *optimizer.RowExpr, slot SlotView, ctx *Context) (Datum, error, bool) {
	if ctx == nil || x.Plan == nil || !x.IsNonCorrelated || !x.UnknownEqFalse ||
		!hashedSubPlanEnabled() {
		return Datum{}, nil, false
	}
	n := len(rowOp.Elems)
	if n == 0 {
		return Datum{}, nil, false
	}
	stat := ctx.subPlanStat(x)
	stat.Calls++
	elems := make([]Datum, n)
	for i, e := range rowOp.Elems {
		v, err := evalExprSlot(e, slot, ctx)
		if err != nil {
			return Datum{}, err, true
		}
		elems[i] = v
	}
	for _, v := range elems {
		if v.IsNull() {
			return NewBoolDatum(x.Negated), nil, true
		}
	}
	h, err, served := rowHashFor(x, n, ctx)
	if err != nil {
		return Datum{}, err, true
	}
	if !served {
		return Datum{}, nil, false
	}
	if len(h.set) == 0 {
		// No inner tuple can match — no coercion consult needed, so no
		// family check either.
		return NewBoolDatum(x.Negated), nil, true
	}
	for i, v := range elems {
		if subPlanHashFamilyOf(v) != h.fams[i] {
			// Cross-kind comparison — compareEq's coercions apply; the
			// linear path computes them exactly.
			return Datum{}, nil, false
		}
	}
	if _, hit := h.set[rowTupleKey(elems)]; hit {
		return NewBoolDatum(!x.Negated), nil, true
	}
	return NewBoolDatum(x.Negated), nil, true
}
