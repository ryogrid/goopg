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

### 2.1 CORRECTION (measured 2026-07-23): the divergence is in the NLI *output*, not the probe

Declining the composite `partsupp` NLI (moving it to a hash join) did **not** fix
Q9 — the composite `partsupp` **Hash Join** *also* drops to 9 069. The reason: its
OUTER subtree still contains OTHER NLIs (`orders_pk`, `supplier`, `lineitem`
index scans), and **an NLI emits a runtime tuple whose column order diverges from
its OID-sorted `Output()` schema** (the exact tension `nl_index_join.go:481-489`
documents). So *any* downstream join — hash or NLI — that binds its keys to that
schema reads the wrong runtime columns. This is why:

- **all-hash** Q9 (no NLIs anywhere) = 175 ✓ (no divergent producer),
- **any-NLI-in-the-plan** Q9 = 0 ✗ (the NLI subtree feeds a downstream join),
- yet a **flat** 6-table query with the same NLIs = 16 216 ✓ — so the divergence
  is triggered specifically by the **derived-table (IsolatedScope) scope**, where
  `remapWithBindings` reorders the NLI subtree's runtime layout differently than in
  a flat query.

**Revised one-line bug:** *inside a derived-table scope, an NLI subtree's runtime
tuple order diverges from its `Output()` schema, so every downstream join that
binds keys to that schema (hash or NLI) reads the wrong columns.* The composite
probe is just the first visible symptom, not the cause. A composite-NLI-only
decline (§4 Phase 1, first attempt) is therefore INSUFFICIENT and was reverted.

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

### Phase 1 — Correctness safety net: decline NLI inside an IsolatedScope (REVISED)

The first attempt (decline only the *composite* NLI) was **insufficient** (§2.1):
the divergence is produced by ANY NLI in the sub-query scope, not the composite
join. **Revised safety net: decline NLI *conversion* for any join built inside an
`IsolatedScope` (derived-table / sub-query) scope**, so the whole sub-query plans
all-hash (measured correct: flat all-hash = 16 216, Q9 = 175).

- Requires threading an `inIsolatedScope` flag through `walkRewriteNLI` (set when
  recursing through a `*Project{IsolatedScope:true}`), and returning early from
  `tryBuildNLI` when set. Bounded, local, revertible.
- Correct because hash is order-agnostic; the sub-query's NLIs become hash joins,
  removing the divergent producers.
- Trade-off: derived-table sub-queries lose NL-index acceleration until Phase 2.
  Verify this does not regress Q2/Q7/Q11/Q15/Q17/Q18/Q20/Q22 row counts (they use
  sub-queries) — correctness must hold; some may slow.

Gate: Q9 → 175; `tpch-spotcheck` PASS; all 22 row counts equal the all-hash
reference on SF1 (no silent regression); each query completes under timeout.

### Phase 2 — The real reconciliation (performance recovery)

Given §2.1, the reconciliation is broader than the probe keys: **an NLI's runtime
output must match its `Output()` schema** so downstream joins (which bind to that
schema) read the right columns. The fix must make `remapWithBindings` (and the NLI
executor) agree on ONE layout for an NLI subtree's output — either by keeping the
NLI runtime in the OID-sorted order its schema advertises, or by not OID-reordering
NLI subtree schemas in the first place. Only then can probe keys (and downstream
hash keys) be bound reliably. Concretely, one of:

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
