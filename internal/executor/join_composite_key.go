package executor

// join_composite_key.go — M0127-P2.2, design leftdeep-joins/05 §5 (stage E4),
// the EXECUTOR half of the multi-column-key sibling pair.
//
// Until P2.2 a goopg hash join hashed on exactly ONE equi-pair and re-checked
// every other conjunct — equality or not — with the interpreted evaluator, once
// per candidate match. Two costs followed from that, both recorded before this
// task existed:
//
//   - the degeneracy trap (M0125-0035b / TPC-DS Q78): when the one key column
//     is pinned to a constant on both inputs, every build row carries the same
//     key, the table is one bucket, and the join is quadratic behind a
//     PG-identical plan. `reselectDegenerateHashKeys` worked around it by
//     swapping the pair; P2.2 deletes the pass, because with all pairs in the
//     key a pinned column simply contributes nothing to bucket spread — which
//     is PG's situation exactly.
//   - an interpreted `evalExprSlot` per extra conjunct per candidate match,
//     where PG does no residual work at all on an all-equijoin join.
//
// Encoding. The planner hands over `JoinExecKeys` (join_exec_keys.go): the
// pairs it is safe to fold into the key, and the residual that remains. Given
// n > 1 keys there are two lanes:
//
//   - all key columns are machine ints: pack the n int64s big-endian into a
//     fixed 8n-byte buffer. Fixed width makes the encoding unambiguous with no
//     separators, and the probe side never allocates — Go compiles
//     `m[string(buf)]` into a lookup that does not copy.
//   - anything else: length-prefixed `datumKey` parts. The prefix, rather than
//     the NUL separator used elsewhere in this package, is what makes the
//     encoding injective: `datumKey` of a text datum can itself contain a NUL,
//     so `("a\x00b", "c")` and `("a", "b\x00c")` would otherwise share a key
//     and the join would over-emit.
//
// A single key keeps the pre-existing lanes untouched (int64 map, or a bare
// `datumKey` string), so the overwhelmingly common join shape executes exactly
// the code it did before.

import (
	"encoding/binary"

	"github.com/goopg/goopg/internal/planner"
)

// compositeKeyWidth is the per-column width of the packed int64 lane.
const compositeKeyWidth = 8

// initExecKeys resolves this join's executor key plan. Called once per Open of
// a lazy hash join, BEFORE the build (or before adopting a shared build), since
// the build and probe halves must encode keys identically.
//
// The build/probe expression split is derived here rather than at each use so
// the two loops cannot disagree about which side of a pair they evaluate — the
// failure mode being a table built on `l.a` and probed with `r.a`, which yields
// a join that runs and returns wrong rows.
func (o *joinOp) initExecKeys() {
	if o.plan == nil {
		return
	}
	plan := o.execKeyPlan()
	o.execKeys = plan.Keys
	o.execResidual = plan.Residual
	o.execKeyPackInt = len(plan.Keys) > 1 && plan.Int64Keys

	// Semi/Anti always build the right side (buildLazyHashTable defends the
	// same way); probeSideIsLeft is the shared statement of that rule.
	buildLeft := !probeSideIsLeft(o.plan)
	pairs := plan.Keys
	if len(pairs) == 0 && o.plan != nil {
		// No key plan (a join with no LeftKey, or a joinOp assembled
		// outside Plan()): keep the single-pair slots populated anyway so
		// the non-composite path evaluates exactly the expressions it did
		// before P2.2, nil included.
		pairs = []planner.JoinKeyPair{{Left: o.plan.LeftKey, Right: o.plan.RightKey}}
	}
	o.buildKeyExprs = make([]planner.Expr, len(pairs))
	o.probeKeyExprs = make([]planner.Expr, len(pairs))
	for i, k := range pairs {
		if buildLeft {
			o.buildKeyExprs[i], o.probeKeyExprs[i] = k.Left, k.Right
		} else {
			o.buildKeyExprs[i], o.probeKeyExprs[i] = k.Right, k.Left
		}
	}
	if n := len(plan.Keys) * compositeKeyWidth; cap(o.execKeyBuf) < n {
		o.execKeyBuf = make([]byte, 0, n)
	}
}

// ensureExecKeys initialises the key plan if Open did not. The build loops are
// reachable directly (unit tests drive buildLoopRight on a hand-built joinOp),
// and a loop that indexed an empty expression list would panic there rather
// than degrade.
func (o *joinOp) ensureExecKeys() {
	if o.buildKeyExprs == nil {
		o.initExecKeys()
	}
}

// execKeyPlan is initExecKeys' planner query, isolated so a joinOp with no
// plan (constructed directly by a test) degrades to the single-pair behaviour
// instead of panicking.
func (o *joinOp) execKeyPlan() planner.JoinExecKeys {
	if o.plan == nil {
		return planner.JoinExecKeys{}
	}
	return o.plan.ExecHashKeyPlan()
}

// multiKey reports whether this join hashes on more than one pair, i.e.
// whether the composite encoding is in play at all.
func (o *joinOp) multiKey() bool { return len(o.execKeys) > 1 }

// encodeCompositeKey evaluates every key expression against slot and leaves
// the encoded key in o.execKeyBuf.
//
// Returns ok=false when ANY key column is NULL: SQL equality is not true for a
// NULL operand, so a row with a NULL in any key matches nothing — the same rule
// the single-key path applies, extended componentwise. That is what lets the
// caller treat a NULL key as "no match" without consulting the other columns.
//
// packMiss=true means the int64 lane was promised by the plan's types but a
// datum did not fit it. On the build side that demands a demotion (the table
// must be re-keyed); on the probe side it simply means no build key can equal
// this one, because every build key IS int64-representable.
func (o *joinOp) encodeCompositeKey(exprs []planner.Expr, slot SlotView) (ok bool, packMiss bool, err error) {
	o.execKeyBuf = o.execKeyBuf[:0]
	for _, e := range exprs {
		v, err := evalExprSlot(e, slot, o.ctx)
		if err != nil {
			return false, false, err
		}
		if v.IsNull() {
			return false, false, nil
		}
		if o.execKeyPackInt {
			ik, iok := datumToInt64Key(v)
			if !iok {
				return false, true, nil
			}
			o.execKeyBuf = binary.BigEndian.AppendUint64(o.execKeyBuf, uint64(ik))
			continue
		}
		part := datumKey(v)
		o.execKeyBuf = binary.BigEndian.AppendUint32(o.execKeyBuf, uint32(len(part)))
		o.execKeyBuf = append(o.execKeyBuf, part...)
	}
	return true, false, nil
}

// encodeBuildCompositeKey encodes the build-side composite key for slot into
// o.execKeyBuf, demoting the packed lane if a datum breaks the plan's int64
// promise. ok=false means a NULL key, i.e. a row that can match nothing.
//
// Split from the insert so callers copy the build row only after the key has
// survived — ownedBuildRow is the expensive half.
func (o *joinOp) encodeBuildCompositeKey(slot SlotView) (ok bool, err error) {
	keyOK, packMiss, err := o.encodeCompositeKey(o.buildKeyExprs, slot)
	if err != nil {
		return false, err
	}
	if packMiss {
		// Same contract as demoteIntHash on the single-key path: the plan
		// said "machine integers", a datum said otherwise, and the answer is
		// to fall back to the always-correct representation rather than to
		// drop the row. Re-encoding is exact because the packed lane holds
		// int64s and datumKey(KindInt(v)) is canonicalNumericKey(v, 0), so a
		// row filed before the demotion lands under the identical key as one
		// filed after it.
		o.demoteCompositeIntKeys()
		keyOK, packMiss, err = o.encodeCompositeKey(o.buildKeyExprs, slot)
		if err != nil {
			return false, err
		}
		if packMiss {
			// Unreachable: the pack lane is off now.
			return false, nil
		}
	}
	return keyOK, nil
}

// fileCompositeBuildRow files row under the key currently in o.execKeyBuf.
// Must follow an encodeBuildCompositeKey that returned ok.
func (o *joinOp) fileCompositeBuildRow(row Row) {
	if o.lazyHash == nil {
		o.lazyHash = make(map[string][]Row)
	}
	sk := string(o.execKeyBuf)
	o.lazyHash[sk] = append(o.lazyHash[sk], row)
}

// demoteCompositeIntKeys abandons the packed int64 encoding mid-build and
// re-keys everything filed so far into the general length-prefixed form.
func (o *joinOp) demoteCompositeIntKeys() {
	o.execKeyPackInt = false
	if len(o.lazyHash) == 0 {
		return
	}
	n := len(o.execKeys)
	rekeyed := make(map[string][]Row, len(o.lazyHash))
	buf := make([]byte, 0, n*16)
	for packed, rows := range o.lazyHash {
		buf = buf[:0]
		for i := 0; i < n; i++ {
			off := i * compositeKeyWidth
			v := int64(binary.BigEndian.Uint64([]byte(packed[off : off+compositeKeyWidth])))
			part := canonicalNumericKey(v, 0)
			buf = binary.BigEndian.AppendUint32(buf, uint32(len(part)))
			buf = append(buf, part...)
		}
		k := string(buf)
		rekeyed[k] = append(rekeyed[k], rows...)
	}
	o.lazyHash = rekeyed
}

// compositeProbeMatches looks up the build rows for the current probe row.
// Called only when multiKey() — a NullAware (`NOT IN`) join is forced
// single-key by ExecHashKeyPlan, so the `ok=false` returned below can never
// reach the branch that reads it as a three-valued-NULL signal.
//
// The lookup is written as `o.lazyHash[string(o.execKeyBuf)]` on purpose: the
// compiler special-cases that exact form into a no-copy map access, which is
// the whole point of the fixed-width lane on a multi-million-row probe side.
// `wantKey` forces the key to be materialised anyway, for the FOR UPDATE path
// whose parallel CTID map is keyed alongside lazyHash.
func (o *joinOp) compositeProbeMatches(slot SlotView, wantKey bool) (matches []Row, key string, ok bool, err error) {
	keyOK, packMiss, err := o.encodeCompositeKey(o.probeKeyExprs, slot)
	if err != nil {
		return nil, "", false, err
	}
	if packMiss {
		// Every build key is int64-representable, so a probe key that is not
		// cannot equal any of them. Report a NULL-like miss rather than a
		// key: `ok` drives the Semi/Anti and outer-join branches, and this is
		// genuinely "no match", not "no key" — but the two coincide for every
		// branch that reads it, exactly as on the single-key int lane.
		return nil, "", false, nil
	}
	if !keyOK {
		return nil, "", false, nil
	}
	if wantKey {
		key = string(o.execKeyBuf)
		return o.lazyHash[key], key, true, nil
	}
	return o.lazyHash[string(o.execKeyBuf)], "", true, nil
}
