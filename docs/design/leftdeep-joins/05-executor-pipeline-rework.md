# 05 — Executor Rework: the Fusion-Free Hash Cascade

| field | value |
| --- | --- |
| status | draft (DESIGN ONLY) |
| date | 2026-08-02 |
| PG oracle | `postgres/src/backend/executor/nodeHashjoin.c` (state machine `HJ_BUILD_HASHTABLE`/`HJ_NEED_NEW_OUTER`/`HJ_SCAN_BUCKET`, :180-182; `ExecHashJoinImpl` :221); `postgres/src/backend/executor/nodeHash.c` (`ExecHashTableCreate` :446) |
| adopts | `docs/design/0126-0004-legacy-build-path-slot-chaining.md` (deferred at ledger row 2026-08-01/M0126-0004 — un-deferred by this bundle as stage E1) |
| retires (on completion) | `fusedHashJoinOp` + `tryFuseHashCascade` (`internal/executor/fused_hash_join.go`, permanently-off since stage2 verdict); `multiHashJoinOp` (`internal/executor/multi_hash_join.go`) with its plan node ([08](08-migration-and-removal.md)) |

## 1. The claim to make true

> A left-deep chain of binary hash joins with base-rel build sides must
> execute with the same asymptotic work and within noise of the same
> constant factors as `multiHashJoinOp` did: **N base-relation hash tables
> built at Open, one streaming probe pass, zero per-tuple heap allocation
> and zero row materialisation at the seams.**

Structural equivalence already holds: `joinOp.Open` → `openLazyHashJoin` →
`buildLazyHashTable(inner)` → `openProbeSide(outer)` → child `joinOp.Open` →
… builds all tables before the first probe row moves, and `nextLazy` streams
the probe (`internal/executor/operators_join_agg.go:1247`), emitting through
a shared `VirtualSlot` with zero allocation
(`ensureLazyVirtual`, `:1075`). MHJ's remaining advantages are five concrete
per-row costs, each with a stage below. Stage letters are the implementation
units (ordering + gates in [IMPLEMENTATION-TODO.md](IMPLEMENTATION-TODO.md)).

Counting rule (second-try correction): benefits scale with **probe-side
seams** — a `joinOp` child on the *probe* path of its parent. Build-side
children pay their cost once into the hash table, not per probe row. Under
the [02](02-plan-shape-contract.md) contract with `BuildLeft=false`, every
interior seam is a probe seam — the worst and the common case.

## 2. Stage E1 — de-materialise the probe seam on the legacy Build path

**The single largest item.** On the legacy path (all aggregate-topped
queries), each seam does `r := slotRow(probeSlot)`
(`operators_join_agg.go:1254`) → `VirtualSlot.Row()` (`slot.go:159-166`):
pooled-row Get + width-wide zero + width-wide 48-byte-Datum copy per emitted
tuple, never released. The slab path already fixed its half
(M0126-0003: `fillFromTupleSlot` VirtualSlot fast path + slot-based key
eval, commits `5c1c0e21`, `d197365c`).

Design = `0126-0004`'s, adopted verbatim with its named hazard:

- `nextLazy` holds the child's slot **as a source** of `lazyVirtualOut`
  instead of flattening it to a `Row`: probe-side source rebinding per pull,
  with the F7 mitigation — the child may return different concrete slots
  across pulls (`lazyVirtualOut` / `lazyOuterOnlySlot` / fresh
  `Materialize()` / `asSlot` from `rowsOp`/`spillOp`), so rebind on pointer
  change and fall back to one copy when the concrete type changes.
  Lifetime is safe by control flow: a new probe row is pulled only after all
  matches of the current one are drained.
- The vestigial `lazyKeyRow` field (`operators_join_agg.go:77`) and the
  never-released pool acquisition disappear with the seam.
- Risk note carried from the ledger: 0b's Q12=0 regression showed this
  path's fragility → the stage gate is the full regress-port suite + TPC-H
  spotcheck *before* any planner change stacks on top
  ([09](09-verification-and-acceptance.md) §2).

Alternative rejected: migrating `Aggregate` to the slab (`buildRec`) instead,
which would let the slab seam fix cover star queries. Rejected **as the
primary vehicle** because the legacy path remains structurally reachable
(workers via `BuildWorker`, EXPLAIN ANALYZE instrumentation, extended
protocol) and a correct seam must not depend on which builder ran. Slab
`Aggregate` migration stays desirable, independent, and out of scope here.

## 3. Stage E2 — kill the per-row allocations Stage-0b introduced

`mergedKeySlot` (`operators_join_agg.go:986-1014`): five allocations per
probe row and per build row (fresh `nullRow`, `MaterializedSlot`,
`[]virtualCol`, sources slice, `VirtualSlot`). The shape is invariant per
`Open` — hoist construction to `Open`, rebind `.row` pointers per pull, zero
steady-state allocation. Same treatment inside `fusedHashJoinOp` is
unnecessary (it dies), and `buildLazyHashTable`'s per-build-row call sites
(`:590`, `:646`, `:702`) reuse the same hoisted slot.

## 4. Stage E3 — single-pass, single-map build

Two changes to `buildLazyHashTable`:

1. **Single pass**: insert into the hash table during the budgeted drain
   instead of drain-to-`[]Row`-then-re-iterate (the re-iteration allocates a
   `MaterializedSlot` per row, `spill.go:435-442`, and copies every row an
   extra time). `drainRowsBounded`'s budget logic folds into the build loop;
   the row-copy into owned memory stays (M0097-0058 aliasing class — build
   rows must not alias scan buffers).
2. **Single map**: key representation is decided **before** build from
   planner-provided key type info (the searched plan knows both key exprs'
   types), not discovered by populating `map[string][]Row` and
   `map[int64][]Row` simultaneously (`lazyHashInsertDatum`, `:1016-1033`).
   int64-eligible keys build only `lazyIntHash`; others build only the
   string map. Halves peak build memory. The int64 fast path also extends to
   Semi/Anti builds (today INNER-only, `:658-670`) — the CTID-preserving
   exception stays on the generic map.

Stage E3 is a prerequisite for spill ([06](06-hash-spill-and-memory.md)):
batches partition rows at insert time, which requires the single-pass shape.

## 5. Stage E4 — multi-column hash keys (planner + executor sibling change)

`planner.Join` grows `HashKeys []JoinKeyPair` (`{Left, Right Expr}`)
replacing single `LeftKey`/`RightKey` for hash and merge; the search
([03](03-join-search-pg-dp.md) §5.1) fills it with **all** usable equality
clauses; residual `Predicate` keeps only genuinely non-equijoin conjuncts.
Executor: composite key encoding — all-int64 keys pack into a fixed-width
byte key (or remain single-int64 fast path when len==1); mixed keys use the
existing `datumKey` encoding concatenated. Effects:

- deletes the degeneracy trap by construction (a constant-pinned column no
  longer collapses the bucket space — the other key columns still
  discriminate); `reselectDegenerateHashKeys`
  (`internal/planner/inner_join_qual_pushdown.go:676`) retires;
- removes the per-match interpreted residual call for the all-equijoin
  common case (`joinPredicateMatchSlot`, `:1060`);
- merge join gets true multi-column sort keys for free (same `HashKeys`
  list feeds pathkeys, [07](07-other-join-operators.md) §2).

This is the recorded "PG-shaped multi-column keys, planner+executor
sibling-pair change" from the `reselectDegenerateHashKeys` doc comment and
the ledger — scheduled, not optional: single-key + residual is also why
every extra conjunct today costs an interpreted eval per candidate match.

## 6. Stage E5 — compiled key and residual evaluation

Join hash keys and residuals are 100 % interpreted `evalExprSlot` today; the
compiled `ExprNode`/`evalFastExpr` sibling (`internal/executor/exprnode.go`)
is used only by filter/project/limit. Stage E5 compiles, at `Open`: (a) each
`HashKeys[i].Left/.Right` accessor, (b) the residual conjunction. Fallback to
the interpreter for unsupported kinds is the existing `ExprAdapter` path —
sibling-path rule applies (any semantic divergence between the twins is a
release blocker; the overflow-parity precedent is
`docs/design/0097-0037-fast-path-int-overflow.md`).

## 7. What emphatically does NOT change

- The `Operator`/`TupleSlot` interfaces (`operator.go:34`, `slot.go:18`) —
  this is a rework *under* the contract, not a new executor.
- Build-side drain discipline: the build side is still fully consumed at
  `Open`; goopg keeps PG's blocking hash build semantics.
- The shared-hash-build parallel path (`parallel_hash_build.go`): leader
  builds once, workers adopt (`lookupSharedHashBuild`,
  `openLazyHashJoin:494-507`). Stage E3/E4 must keep `sharedHashBuild`'s
  fields in sync (it carries `hash`/`intHash`/`hashIsInt` — becomes
  single-map + key-spec). Parallel build itself stays out of scope.
- `nestedLoopIndexJoinOp` — already fully streaming, zero-copy
  (`operators_nljoin.go`); untouched except path-generation provenance.

## 8. Why fusion and MHJ become deletable, by the numbers

After E1–E4, per emitted tuple per seam the cascade does: probe-slot source
rebind (pointer writes), one int64/bytes key eval (compiled), one map
lookup, residual only when a non-equijoin conjunct exists. That is
`multiHashJoinOp.initStepHelper`'s exact work profile
(`multi_hash_join.go:547-613`: read key from composed slot, lookup, bind,
filter) minus MHJ's five structural defects (no spill; silent edge drops in
the spanning walk; arbitrary-key fallback for unreached tables; the
planner-side coordinate round-trip; the incomplete residual walker). The
stage0 A/B gaps (Q3 2.92×, Q10 3.44×, Q18 2.52×) are seam-cost gaps —
E1+E2 remove the seam cost's alloc/copy component entirely; the E-series
exit gate re-runs that exact A/B and requires ≤ 1.2×
([09](09-verification-and-acceptance.md) §3) before the deletion stage may
start. `fusedHashJoinOp`'s odometer (`fused_hash_join.go:257-341`) served as
the reference for E1's "key from the composed slot" pattern; with E1–E2
landed it optimises nothing that remains, and it carries an open correctness
verdict (Q14) — deleted, not fixed.
