# 0125-0012 — TPC-DS Q8: a `ColumnRef` below a FROM-subquery `Project` keeps its outer-scope index

Status: draft
Date: 2026-07-29
Milestone: M0125-0012 (round-1 §4.2 fix #8; ledger row `tpcds-round2 Q8`, 2026-07-27)

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
