# 13 — Composite-NLI Outer-Layout Reconciliation (the Q9=0 fix)

| field | value |
| --- | --- |
| status | design (proposed) |
| date | 2026-07-23 |
| blocks | C4 cost-driven join order (Q9 returns 0 rows) |
| relates to | reverted `M0067-0003`, deferred `M0068`, hung `M0072-0002` |

## 0. Summary

Under the cost-driven planner, TPC-H **Q9 returns 0 rows** (correct: 175). The
root cause is **not** the cost model, cardinality, or coordinates — it is a
pre-existing, documented-but-deferred defect in the **composite-index
Nested-Loop-Index-Join (NLI)**: the probe keys are bound to the outer subtree's
`Output()` **schema** positions, but in a derived-table (sub-query) scope the
outer's **runtime tuple layout diverges from that schema**, so the composite
probe reads the wrong columns and matches ~3 % of rows.

This is the exact mismatch the in-tree comment already names
(`nl_index_join.go:142-150`: *"the rebind still surfaces the
schema-annotation-vs-runtime-layout mismatch … the rebind picked the wrong
runtime slot"*), deferred as `M0068`. The cost-driven plan shape merely makes Q9
choose the composite NLI inside a sub-query scope, exposing it.

## 1. Evidence (measured, SF1 @ `l_orderkey < 300000`, ~3 s repro)

- Genuine **all-hash** Q9 = **175 ✓**; **NLI-on** Q9 = **0 ✗** (same query, same
  scale — only NLI differs). So the defect is in the NLI path.
- NLI `EXPLAIN ANALYZE`: `lineitem⋈orders` = 300 588 ✓ → composite `partsupp_pk`
  NLI (`ps_partkey=l_partkey AND ps_suppkey=l_suppkey`) = **9 069** (3 %, same
  ratio as full-scale 179 197 / 6 M) → `part` = 0.
- **Planner binding is CORRECT** (`GOOPG_NLI_DEBUG`): the composite keys resolve
  `ps_partkey→l_partkey`, `ps_suppkey→l_suppkey`, and each `ColumnRef.Index`
  matches its name in `outerNode.Output()` ("ok").
- **Runtime probe values are WRONG** (`GOOPG_PROBE_DEBUG`): at the outer index the
  schema calls `l_suppkey` the probe reads **1,2,3,4,5,6…** (= `l_linenumber`),
  and at the `l_partkey` slot it reads unrelated small values. → the runtime
  tuple's column order ≠ its `Output()` schema.
- **Isolation of the trigger:** the composite NLI is CORRECT for **flat** outers
  of 2/3/4/5/6 tables (all 300 588; 6-table+green = 16 216 ✓). It breaks ONLY
  inside a **derived-table (IsolatedScope sub-query)**:
  - `SELECT count(*) FROM (SELECT * FROM <6 tables> WHERE … l_orderkey<300000) x`
    → **0**
  - `SELECT count(*) FROM <same 6 tables> WHERE … l_orderkey<300000` → **16 216 ✓**
  - It is **not** column-pruning (`SELECT *` also = 0).

### Minimal fast reproduction (~1–3 s)

Cost-driven, NLI **on** (do *not* set `GOOPG_DISABLE_NLI`), stats loaded:

```sql
SELECT count(*) FROM (
  SELECT * FROM part, supplier, lineitem, partsupp, orders, nation
  WHERE s_suppkey=l_suppkey AND ps_suppkey=l_suppkey AND ps_partkey=l_partkey
    AND p_partkey=l_partkey AND o_orderkey=l_orderkey AND s_nationkey=n_nationkey
    AND p_name LIKE '%green%' AND l_orderkey < 300000
) x;
-- returns 0; correct is 16216
```

## 2. Root-cause analysis

`tryBuildNLI` (`nl_index_join.go:293`) binds each composite probe key's
`ColumnRef.Index` against `outerNode.Output()` — the outer subtree's **cached
schema** at rewrite time. The pipeline then runs later reorder passes
(`remapExprRefsToMHJ`, `remapWithBindings`, `planner.go:1004-1016`).

The existing code (`nl_index_join.go:481-489`) is explicit that, for NLI outers,
those passes **deliberately leave NLI keys at their pre-rewrite indices** because
"the schema annotation looks OID-sorted [but] the runtime layout matches the
pre-rewrite indices." In other words: for simple/flat plans the executor emits
the outer tuple in the **pre-rewrite** order, so keys left at pre-rewrite indices
read correctly, even though the *schema annotation* was OID-reordered.

The composite path violates this: `pickIndexCoveringAllLeadingColumns`
(`nl_index_join.go:950`) + the rebind (`:513-570`) bind the composite keys to the
**post-annotation (schema) indices**, not the pre-rewrite (runtime) indices. For
a flat outer, schema ≈ runtime for the referenced columns, so it happens to work.
Inside a derived-table scope, `remapWithBindings` reorders the outer's **runtime**
layout (the sub-query binding correction) so the schema-indexed keys now point at
the wrong runtime slots — measured: `l_suppkey`-slot → `l_linenumber`.

**One-line statement of the bug:** *the composite NLI probe keys are indexed in
the outer's `Output()` schema coordinate space, but the NLI executor reads the
outer tuple in a different (pre-rewrite / runtime) coordinate space, and the two
diverge once a sub-query scope reorders the runtime layout.*

## 3. Why prior attempts failed (constraints on the fix)

- `M0067-0003` (composite-NLI hoist) — reverted: moved the conjunct correctly but
  still hit this exact mismatch ("Q9 1 row not 7").
- `M0072-0002` (full rebind rewrite) — **hung** at runtime. The practice card
  therefore mandates: *bound the change and verify incrementally; do not retry
  the whole rewrite blind.*

So the design MUST be incremental and each step MUST be verified against the 3-s
repro **and** `tpch-spotcheck` (Q12/Q13) **and** the full 22-query row counts
(silent-regression risk — this is a shared executor/planner path).

## 4. Proposed design

The fix has two layers; ship them in order so correctness lands first at low risk.

### Phase 0 — Confirm the exact coordinate spaces (no behaviour change)

Instrument (temporarily) `tryBuildNLI` and `indexScanOp.lookupKeys` to print, for
the composite case: (a) each key's bound `Index`, (b) the outer's `Output()` name
at that index, (c) the **actual runtime value** read (already done once — reads
`l_linenumber`). Also print the outer node kind chain (`Join`/`NLI`/`Project`) and
whether `remapWithBindings` altered the outer's runtime layout vs its schema.
Deliverable: a one-paragraph confirmation of *which* pass reorders the runtime and
*which* coordinate space the executor actually uses. This pins the fix target.

### Phase 1 — Correctness safety net: decline the unreconcilable composite NLI

**Goal: Q9 correct immediately, zero risk to working plans.** Extend the existing
decline (`nl_index_join.go:466`, which already refuses NLI for a direct
`IsolatedScope` `Project` outer) to also refuse the **composite** (`len(keys) > 1`)
NLI when the outer is a multi-relation subtree whose runtime layout cannot be
guaranteed to match its schema — concretely, when the NLI is being built **inside
an `IsolatedScope`** (sub-query) and the outer is a `*Join` / `*NestedLoopIndexJoin`
rather than a base scan.

- Correct because the hash join over the same predicate is order-agnostic
  (all-hash Q9 = 175 ✓).
- Bounded because it only affects composite NLIs with a multi-relation outer in a
  sub-query scope — single-key NLIs and flat composite NLIs (2–6 tables, all
  measured correct) are untouched.
- The cost is that these specific joins run as hash instead of NL-index (a
  performance trade, not a correctness one) until Phase 2.

Gate: Q9 → 175; `tpch-spotcheck` PASS; all 22 row counts unchanged vs all-hash.

### Phase 2 — The real reconciliation (performance recovery)

Bind the composite probe keys in the **same coordinate space the NLI executor
actually reads** — the pre-rewrite / runtime layout the single-key path already
uses successfully — instead of the `Output()` schema space. Concretely, one of:

1. **Late binding:** defer composite-key index assignment until *after*
   `remapWithBindings`, resolving against a runtime-truthful layout descriptor
   (the same one the executor's `outerSlot` presents), mirroring how single-key
   NLI survives the reorder passes.
2. **Runtime-layout descriptor:** give each NLI outer an explicit
   `runtimeColumnOrder` the executor honours, and bind keys against it, removing
   the schema-vs-runtime ambiguity entirely (this generalises `M0068`).

Phase 2 is the harder, `M0068`-class change; it lands only after Phase 1 has
restored correctness, and is developed against the 3-s repro with the same triple
gate. Each sub-step is independently revertible so a `M0072`-style hang is caught
at the first failing query, never on a blind full rewrite.

## 5. Verification

- **Repro gate:** the §1 minimal query returns 16 216 (not 0); full Q9 @ SF1
  returns 175.
- **Silent-regression gate:** `scripts/tpch-spotcheck.sh` (Q12=2 / Q13=33) PASS,
  then all 22 TPC-H row counts equal the all-hash reference on SF1.
- **Unit pin:** a planner test that builds a composite NLI whose outer is a
  reordered multi-relation subtree and asserts the probe keys resolve to the
  runtime layout (Phase 2), plus a decline test for Phase 1.
- **No new hang:** each phase runs the repro to completion under the per-query
  timeout before commit.

## 6. Scope note

This is a pre-existing NLI correctness defect, independent of the cost model, but
it **blocks** the C4 milestone because cost-driven order chooses the composite NLI
inside Q9's sub-query scope. Two orthogonal real bugs found during the same
investigation are already fixed and landed (executor arena-retention `65965d47`;
planner int64 cardinality overflow `9106121e`); neither is this bug's cause.
