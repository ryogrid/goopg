# 0125-0012 — TPC-DS Q8: a `ColumnRef` below a FROM-subquery `Project` keeps its outer-scope index

Status: accepted (executed 2026-07-29)
Date: 2026-07-29
Milestone: M0125-0012 (round-1 §4.2 fix #8; ledger row `tpcds-round2 Q8`, 2026-07-27)

> **§0 — EXECUTED 2026-07-29. Read this before §"Root cause of the residual".**
> The pre-registered root cause below (§"Root cause of the residual", inherited
> verbatim from the ledger row and marked "do not re-diagnose") is **refuted by
> direct measurement of the planned tree**, and D1/D2/D3 were therefore not the
> fix. The failing `ColumnRef` is **not** an unrepaired one inside a `Filter`
> below the subquery's `Project`; it is the subquery's **own `Project` target**,
> which `remapSubqueryColumnRefs` had **already numbered correctly** (`ca_zip/0`)
> and which a *later* pass then overwrote with an outer-scope index.
> The real defect is a **domain violation in the outer join-reorder remap**:
> `applyJoinTreePosMap` descended into a FROM-subquery `Project` and applied a
> position map that was never defined over that scope. See §R below for the
> measurement, the corrected mechanism, and the landed fix. The rest of the
> original document is kept unedited as the record of what was believed before
> the tree was inspected — §"Verification"'s V0 acceptance bar was correct and
> was honoured in full.

## Problem

Q8 is the **only unresolved member of round 1's nine goopg-only errors**, and one of only
two `ERROR` cells in the complete SF=1 sweep (the other, Q75, is M0125-0004). Measured at
HEAD (`analysis/tpcds-sf1-resweep-20260728/RESULTS.md`, row 8; classified in
`analysis/tpcds-sf1-goopg-20260728.md` §4):

```
goopg :65436   ERROR 26 s   ERROR: column ref ca_zip/57 out of MaterializedSlot range 1
PG    :65438   OK    0 s    0 rows
```

The server **survives** — `select 1` succeeds afterwards and the sweep continued on the same
process — so this is a contained statement error, not the crash it used to be.

**Iterate at SF0.5, not SF=1.** `bench/tpcds/runtime_goopg/tpcds-results-sf05/sweep-20260729-093056.txt`
reproduces the identical error in **12 s** against 26 s + a ≤30 s EXPLAIN capture at SF=1,
and the SF0.5 oracle carries a real checksum for Q8 (`8|OK|0|1f18d650d205d71d|1`) rather
than `n/a`. The SF0.5 gate therefore **does** see this defect today — which is not true of
all four tasks adopted in this wave; see the milestone's "Acceptance consequence" table.

### What already landed (do not re-attempt)

Two rounds of work have already moved this defect, and re-deriving them wastes the budget:

| commit | what it did | what it did **not** do |
|---|---|---|
| `9ddbc679` | `containsSetOp` guard at `pushdown.go:241`, `pushdown.go:264`, `planner.go:2078` | **Never protected the remap path** — which is exactly why Q8 kept failing after it |
| `9740fce9` | `buildBindingsPosMap` (`internal/planner/bushy.go`) gained `SetOp` / `RecursiveUnion` / `WorkTableScan` / `WindowAgg` / `ProjectSet` / `OrdinalityWrap` / `RowsFrom` / `IndexOnlyScan` opaque-leaf arms **plus a decline-on-unknown `default:`** — the only walker of that round to get one. Together with the `*MaterializedSlot` / `*Slot` bounds check in `evalExprSlot` (`internal/executor/expr.go:353`) this converted a backend-killing panic into a contained `XX000` | Did not fix the index itself |

The containment half was the higher-value one at the time: a dropped connection makes every
further planner index bug undebuggable and forces a server restart mid-benchmark. What is
left is the index.

### Root cause of the residual (already established — do not re-diagnose)

From the ledger row `tpcds-round2 Q8`:

> The stale index is `ca_zip` = 57, a **global FROM-order** index reaching a **1-column**
> `MaterializedSlot` — i.e. the INTERSECT-in-FROM subquery's own `Project` scope, which
> `buildBindingsPosMap` never governs. `remapSubqueryColumnRefs` rewrites only `Project`
> **TARGETS** by name; a `ColumnRef` inside a `Filter` predicate below that `Project` is
> left with the outer-scope index.

So there are **two different coordinate spaces** and only one of them is repaired:

- `buildBindingsPosMap` governs the **MHJ / join-tree** coordinate space. `9740fce9` made it
  complete for `SetOp`, which is why the *crash site* moved.
- `remapSubqueryColumnRefs` (`internal/planner/planner.go`, reached only from
  `planSubqueryRangeVar`) governs the **FROM-subquery's own** scope. It walks `Project`
  targets and nothing else.

Q8 puts an `INTERSECT` inside a FROM subquery, so the subquery's `Project` sits above a
`SetOp`, and a `Filter` below that `Project` still carries a `ColumnRef` numbered in the
outer FROM order (57) against a child that is one column wide.

## Design

### D1. Extend the subquery-scope pass past `Project` targets

Rewrite, in the subquery scope, the reference-carrying slots that the pass currently skips:

- `Filter.Predicate` — the site Q8 actually fails on
- `Join.LeftKey` / `Join.RightKey`
- `Aggregate.GroupExprs` / `Aggregate.Aggs`

### D2. ⚠️ Compose with M0125-0010 — do not revert its narrowing

`remapSubqueryColumnRefs` was **deliberately narrowed** by M0125-0010 (`2e09250b`) from an
unconditional rebind to **verify-then-repair**:

> a target whose existing index is in range **AND** names the column the ref asks for is
> left untouched (the only branch that can tell two same-named child columns apart, so it
> must precede any name search); only an out-of-range index, or one naming a different
> column — the actual leakage signature — is re-derived by name.

That narrowing exists because the unconditional version *was itself the bug*: an `Aggregate`
names its output columns after the aggregate **function**, so `select * from (select sum(a),
sum(b) …) d` planned a child schema of literally `[sum, sum]` and every target bound to slot
0.

**Every new node kind added by D1 must inherit the verify-first branch.** A name-only search
on `Filter.Predicate` or `Aggregate.GroupExprs` would be the **fourth recurrence** of
"ambiguous key resolved by silently taking the first match" — the fifth *instance*, on the
numbering commit `2e09250b` established when it called M0125-0010 the "**Third recurrence**
… after M0097-0003/-0032 and M0125-0009". A purely positional remap is equally wrong for the
same reason M0125-0010 rejected it: it breaks any `Project` that reorders or subsets its
child.

### D3. Prefer the driver over a fifth hand-rolled walker

If **M0125-0001** (`internal/planner/exprwalk.go` + the exhaustiveness gate) has landed,
route D1's new arms through it. Hand-rolling another node-kind switch here re-creates
exactly the copy-paste walker family that M0125-0001/-0002 exist to delete, and it will lack
the `default:` arm whose absence is the documented failure mode
(`docs/design/tpcds-round2-fixes/README.md` §0). This is the reason the milestone's
interleaving rule places M0125-0012 **after** M0125-0001.

## Verification

### ⚠️ V0. "0 rows" is NOT an acceptance criterion

**PG returns 0 rows for Q8 at SF=1.** Any bug that yields an empty result therefore "matches
PG" on both status and row count. This is the same structural blindness M0124-0001 measured
across the whole board (18 of 99 queries passed a row-count gate while returning wrong
answers) — here it is worse, because the correct answer is itself empty.

Acceptance is therefore three-part:

1. **No `ERROR`** — Q8 completes on goopg.
2. **Rows = PG's 0** at SF=1.
3. **A discriminating probe passes — and failed before the fix.** Construct the same
   INTERSECT-in-FROM shape with Q8's predicates relaxed until PG returns a **non-empty**
   set, and assert byte-equality of the values on both engines. Without (3), (1) and (2) are
   satisfiable by a plan that silently drops the subquery.

   **The probe must reproduce `column ref … out of MaterializedSlot range` on pre-fix
   HEAD.** Relaxing predicates can change the chosen plan into one that never reaches the
   defective path, so a probe that already passes is evidence of nothing. This is the repo's
   standing bar for a gate: M0125-0011's `TestExecMergeJoinAppliesResidualConjuncts` fails
   5/5 subtests pre-fix, and root-0036's gate fails 7 of 8 with the hunk stashed. If the
   relaxed form does not fail first, relax it differently.

Ship the probe from (3) as a planner/executor unit test so the discriminator outlives the
task. Reproductions: `QUERIES=8 … scripts/tpcds-sf05-regression.sh` (12 s, the iteration
loop) and `scripts/tpcds-bench-compare.sh 8` (SF=1, the end-to-end check).

### V1. Sibling-path audit (Hard-won Rule #2)

The pass repairs references in a subquery scope; its siblings are the other places that
renumber references across a scope boundary — `buildBindingsPosMap` (`bushy.go`),
`remapByPosMap`, and `shiftColumnRefs` / `cloneExprForShift`
(`internal/planner/mhj_input_rewrite.go`). Check whether any of them also stops at `Project`
targets, and record the answer either way.

## Gate

Planner/executor change → the full pre-commit bar:

- `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh`
- `scripts/tpch-spotcheck.sh` (canonical `Q12=2`, `Q13=35`)
- the TPC-DS SF0.5 gate — **this defect is an ERROR, so the gate does see it** (measured:
  `ERROR 12s` at HEAD), unlike the M0125-0009 class. The **nightly anchors do not**:
  `ci/batch/tpcds-row-anchors.csv` pins 61 queries and has no Q8 row, so closing this task
  means **adding** an anchor, not re-pinning one
- `make plan-diff` — with M0125-0004's `r5-default` fallback label until M0124-0002 lands
  `tpcds-round2-head`
- the pgbench smoke, via the commit hook

## §R. What was actually measured, and what landed (2026-07-29)

### R1. The planned tree, pre-fix

A doll-house replica of Q8's exact shape (`dd`/`st`/`ss` three columns each,
`ca` two; the INTERSECT arms, the `A2`/`V1` double wrapping and the
`substr(s_zip,1,2) = substr(V1.ca_zip,1,2)` residual all preserved) plans to:

```
Filter  out=10 [d_date_sk d_qoy d_year s_store_sk s_store_name s_zip
                ss_sold_date_sk ss_store_sk ss_net_profit ca_zip]
  PRED: … substr(Col(s_zip/5)) = substr(Col(ca_zip/9))      ← CORRECT
  Join  out=10
    MultiHashJoin out=9    (reordered: dd, st, ss)
    Project out=1 [ca_zip]
      TGT: Col(ca_zip/6)                                     ← WRONG, must be 0
      SetOp out=1 [ca_zip]
```

Both halves of the pre-registered mechanism fail against this:

1. The outer `Filter`'s `ca_zip` index — **9** in the replica, **57** at SF=1 —
   is *already correct*; it addresses the 10-column joined row. No `Filter`
   below the subquery `Project` carries a stale ref at all.
2. The failing ref is the V1 `Project`'s own target above a 1-column `SetOp`,
   whose correct value is `0`. `remapSubqueryColumnRefs` produced exactly that.
   Something downstream replaced it.

### R2. Corrected mechanism — an out-of-domain application, and a sibling divergence

The overwriting pass is `applyJoinTreePosMap` (`internal/planner/bushy.go`),
reached from `remapWithBindings` after the MHJ reorder. Its `*Project` arm
descended into every `Project` not flagged `IsolatedScope` (the M0063-0001
view-rename wrapper) and remapped its targets with the **outer** bindings'
position map. `posMap(0)` matches the outer FROM binding that starts at
offset 0 — `store_sales` in real Q8, `ss` in the replica — and returns that
table's *reordered* MHJ offset: `dd 3 + st 3 = 6` in the replica,
`date_dim 28 + store 29 = 57` at SF=1. Hence `ca_zip/57` against a 1-wide
`MaterializedSlot`, and hence the number 57 — which the ledger read as a
"global FROM-order index" but which is in fact an **MHJ-order** offset, the
signature of this pass rather than of an unrepaired one.

`posMap` is defined **only** over the coordinate space that
`buildBindingsPosMap`'s `collect` walker traversed, and `collect`'s own
`*Project` arm stops at *every* join-tree `Project`:

> *"Any Project in the join-tree subtree passed to collect() is a
> subquery-derived table … For `IsolatedScope=true` this was already the
> contract. **Extend it to all Projects**: advance `off` by the projected output
> width and stop."*

The build half was generalised to all `Project`s; the apply half kept the narrow
`IsolatedScope` test. That is CLAUDE.md's "sibling paths must change together"
law, and it is the mirror image of `9740fce9`: that commit gave `collect` its
`SetOp`/`RecursiveUnion`/`WindowAgg`/… opaque-leaf arms so `off` advances past a
set operation, while leaving this applier free to walk into the scope above it.
V1's sibling-path audit is therefore answered — and answered in the *opposite*
direction from the one it anticipated.

### R3. The fix

`applyJoinTreePosMap`'s `*Project` arm now stops at every join-tree `Project`:
no recursion, no target remap. Nothing is lost —

- the subquery's inner plan was already normalised into its own coordinate space
  by `remapSubqueryColumnRefs` when the derived table was planned, and
- `Project`s **above** the join tree are covered by the separate
  `remapTopProjection` pass, which exists precisely because
  `applyJoinTreePosMap` does not reach them.

Nor can it silently disable the outer remap: whenever the root handed to
`remapWithBindings` is itself a `Project` (or a `Filter` over one), `collect`
already stopped there, `entries` came back empty and `buildBindingsPosMap`
returned `nil`, so `remapWithBindings` was a no-op before this change too. The
only live case in which the recursion reaches a `Project` is the one being
fixed: scans on one join side, a derived-table scope on the other.

D1/D2/D3 were **not implemented**: no new walker was hand-rolled, so D3's
concern is moot, and D2's verify-then-repair narrowing is untouched — declining
to rewrite an index that was never in the map's domain adds no new instance of
the "first match wins" family.

### R4. Acceptance (V0 honoured in full)

| gate | result |
|---|---|
| `internal/planner/q8_subquery_scope_posmap_test.go` — every `Project` target `ColumnRef` addresses its own child's output | PASS post-fix; **FAILS pre-fix**: `Project target 0 (ca_zip/6) is outside its child's 1-column output` |
| `internal/executor/q8_subquery_scope_remap_test.go` — end-to-end values | PASS post-fix; **FAILS pre-fix**: `XX000: column ref ca_zip/6 out of MaterializedSlot range 1` |
| same DDL + query on PostgreSQL 18.3 (throwaway cluster) | `alpha\|5`, `beta\|7` — goopg byte-identical |
| real `query8.sql` at SF0.5 | ERROR at ~11 s → no error; see R5 |

The probe returns a **non-empty** answer, so V0's trap ("0 rows also matches
PG") is closed, and it reproduces the `MaterializedSlot range` signature on
pre-fix HEAD exactly as V0 requires. `TestQ8SubqueryScopeDefectiveShapeStillReachable`
guards the guard: it skips loudly if the planner stops producing the
MHJ + cross-NL + `SetOp` shape, so the value assertion cannot go vacuous unseen.

### R5. Residual, deferred — Q8 leaves the ERROR class and joins the timeout class

Removing the error unmasks the plan's true cost, which the abort had hidden.
The residual `substr(s_zip,1,2) = substr(V1.ca_zip,1,2)` sits on a **cross
join** above the full three-way `store_sales ⋈ date_dim ⋈ store` MHJ, and
`d_qoy = 2 AND d_year = 1998` are in that same post-join `Filter` rather than
pushed into `date_dim`, so every `store_sales` row reaches the filter once per
V1 row. Measured at SF0.5 post-fix: **exceeded a 1500 s client budget (elapsed
1633 s, `timeout` status 124)**, where pre-fix it errored at ~11 s.

This is a **pre-existing plan-quality defect, not a regression from this
change**: the fix alters exactly one `ColumnRef` index and leaves plan *shape*
untouched. Q8 simply moves out of the `ERROR` class into the timeout class this
milestone is named after (RC-8 / M0125-0003 territory). Recorded as a deferral
ledger row with a resume point.

## References

- `docs/design/tpcds-round2-fixes/README.md` §4 (RC-2 — the panic, the missing `SetOp` arm,
  and why it dropped the connection), §13.1 phase 1.3
- `docs/design/tpcds-section4.2-fixes/README.md` §6 (fix 8), §9 addendum
- `.ralph/deferral_ledger.md` — `tpcds-round2 Q8` (2026-07-27)
- `docs/design/0125-0009-parser-expr-key-structural.md` §9 — M0125-0010's verify-then-repair
  narrowing, which D2 must preserve
- `postgres/src/backend/parser/parse_relation.c` — `expandRTE` resolves subquery output
  references by **resno/position**; PG never repairs them after the fact, which is the
  standing argument that goopg's leak is upstream of this pass (ledger row 2026-07-29)
