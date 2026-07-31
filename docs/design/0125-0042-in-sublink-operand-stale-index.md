# 0125-0042 — an OR-ed `IN (subquery)` operand keeps a stale column index

Status: **diagnosis complete, fix not landed** (2026-07-31, loop #11).
Evidence: `analysis/m0125-0042/README.md`. Task: `.ralph/fix_plan.md` M0125-0042.

## Problem

Two OR-ed uncorrelated `col IN (subquery)` sublinks under a SEMI join over a
`MultiHashJoin` produce a **silent wrong answer**: TPC-DS SF0.5 probe answers
1329 where PG 18.3 answers 1294. Either sublink *alone* is exact, and the OR-ed
pair without the enclosing EXISTS is exact.

## Root cause

The `InExpr.Operand` ColumnRef carries the correct `Name` and a **stale
`Index`**. It is resolved against partial two-table join layouts while the plan
is assembled and is never re-resolved against the final node schema:

- bind time (`planInExpr`) → `13` = `c_customer_sk` under `customer_address ++ customer`
- at `remapWithBindings` → `9` = `c_customer_sk` under `customer_demographics ++ customer`
- required at runtime → `22` = `c_customer_sk` under `customer_address ++ customer_demographics ++ customer`

Index 9 of the executed layout is `ca_zip`, a **string**. The predicate becomes
`ca_zip IN (<integer key set>)`; `compareEq`'s string↔int coercion makes it
answer `true`/`false` instead of raising, so the query returns a plausible,
wrong row set (nearly disjoint from the correct one, similar in size).

Three independent mechanisms keep this invisible:

1. **No name re-resolution reaches the node.** `reresolveJoinByName` — the only
   pass that rebinds a predicate's ColumnRefs by `Name` — is driven from
   `applyJoinTreePosMap`'s `*Join` arm. When `remapWithBindings` runs on this
   query the outer tree is `Filter → MultiHashJoin`, with no `*Join` node, so it
   never fires. `remapByPosMap` is a no-op on an index it considers final.
2. **A single `IN` is masked.** It unnests to a semi-join whose keys go through
   `rebind`/`predRebind`, which resolve by `Name`. Only the OR-ed form — which
   cannot unnest and survives as a SubPlan filter — consumes the raw `Index`.
3. **EXPLAIN is masked.** The filter renders `c.c_customer_sk` from the `Name`
   while the executor reads index 9, so the plan output cannot expose the defect.

Not involved, each ruled out by measurement: the hashed SubPlan probe
(`GOOPG_HASHED_SUBPLAN=off` reproduces identically), parallelism
(`max_parallel_workers_per_gather=0` reproduces identically), the SEMI join
itself (EXISTS-only is exact), and the value set (a constant operand over the
same sublinks is exact).

## Relation to prior work

This is the same hazard class as **M0071-0003**, **M0125-0013** (a
`buildBindingsPosMap` arm that skipped a `*Filter`-wrapped MHJ leaf) and
**M0125-0036**, whose own first implementation read a stale post-MHJ index and
was fixed by `resolveHostOperandIdx` — a by-**Name** re-resolution against the
host row schema, introduced precisely because "MHJ packing OID-re-sorts its
output and treats a sublink body as opaque, so an index recorded inside one is
not trustworthy after it".

It is also an instance of CLAUDE.md's "sibling paths must change together":
the Name-based path and the Index-based path disagree, and only the Index-based
one executes.

## Proposed fix (not yet implemented)

Generalise `resolveHostOperandIdx` (`internal/planner/exists_to_any.go`) to
hand-written `InExpr` operands: after the last remap pass, re-resolve the
operand ColumnRef of every SubPlan-bearing `InExpr` **by Name** against the
output schema of the node whose predicate holds it.

Guards this must keep, all learned from prior loops:

- **Unique-match only.** Use the `findUniqueColumnIndex` rule — leave the index
  untouched when the name is absent or ambiguous. M0125-0039 recorded that a
  confidently wrong qualifier is worse than none, and the underlying cause
  (`nextSourceIdx` restarts per query level, so `SourceTableIdx` can collide
  across scopes) is unfixed; do not lean on `SourceTableIdx` alone.
- **Self-joins** must disambiguate by `(Name, SourceTableIdx)` before falling
  back to Name, matching `resolveSide`.
- **Scope boundaries.** Do not descend into the sublink's own `Plan` — those
  indices are inner-scope. `remapByPosMap`'s `scopeIgnore` policy and
  `exprChildSlots`' `slotInnerPlan` already express this boundary.

An adjacent gap found while reading, worth fixing in the same pass or filing
separately: `remapByPosMap`'s inner-plan switch handles `*ExistsExpr`,
`*SubqueryExpr`, `*ArraySubqueryExpr` and the `MultiAssignSubq*` pair, but has
**no `*InExpr` arm**, so a *correlated* `IN (subquery)`'s `OuterColumnRef`s are
never translated through `posMap`. The query in this task is uncorrelated, so
that gap is not what it hits — it is a separate latent defect of the same class.

## Bar for the fix

Planner change ⇒ units, `scripts/tpch-spotcheck.sh` (canonical Q12=2/Q13=35),
TPC-H `make plan-diff LABEL=m0125-0005-relsize-default-stage2`, and the full
99-query TPC-DS SF0.5 gate. Acceptance: `probe35g.sql` answers **1294** and
`pAA.sql` answers **377**, plus a planner-level regression test asserting the
operand index equals the host schema position.

## Note on reproducing

A synthetic 6-table minimal case reaches a structurally identical plan
(`Hash Join (SEMI)` + OR-ed SubPlan filter over a re-sorted 3-table MHJ, SubPlan
bodies as joins, projected column off index 0) and still answers **correctly**.
The trigger is the *binding history* that leaves a partial-layout index behind,
not the final plan shape — so the regression test should assert on the planner's
operand index, not merely on a query result from a small fixture.
