# 0125-0036 — EXISTS → ANY: making an OR-ed correlated EXISTS hashable

Status: LANDED (2026-07-31)
Milestone: `M0125-0036` (class **C3** of `M0125-0026`'s timeout taxonomy)
Code: `internal/planner/exists_to_any.go`, wired from `planner.Plan`
Tests: `internal/planner/exists_to_any_test.go`
Evidence: `analysis/m0125-0036-exists-to-any/`

---

## 1. The defect, as measured

`M0125-0026` §C3 classified TPC-DS Q10 / Q35 (and, as a variant, Q30 / Q81)
as *"a correlated SubPlan is re-evaluated per outer row with no hashing or
caching"*. goopg's Q10 plan:

```
Hash Join (SEMI)
  Filter: (EXISTS(SubPlan 1) OR EXISTS(SubPlan 2))
  SubPlan 1 -> Hash Join (INNER)  rows=131280740
                Filter: (($0 = ws_bill_customer_sk) AND (d_year = 2001) AND …)
```

PG 18.3, same query:

```
Filter: ((ANY (c_customer_sk = (hashed SubPlan 2).col1)) OR
         (ANY (c_customer_sk = (hashed SubPlan 4).col1)))
```

The arithmetic §C3 recorded: outer ≈1×10⁵ rows × (`web_sales` 3.6×10⁵ +
`catalog_sales` 7.2×10⁵) ≈ **1×10¹¹ row-touches**, and Q35's measured 8.16 s
per outer row put it at **≈9 days at SF=1**. Both queries were `TIMEOUT`
members of the class this milestone is named after.

**The trigger is the `OR`.** §C3's own control is Q69, whose three EXISTS are
`and not exists … and not exists`: `unnestExistsExpr` turns those into a
`Hash Join (ANTI)`/`(SEMI)` chain and Q69 completes in 17 s. An OR-ed sublink
is not a top-level conjunct, so the semi-join pull-up bails by construction —
and before this change goopg had **nothing between "unnest to a semi-join" and
"re-execute the body once per outer row"**.

### Why caching is not the answer here

`0124-0004` §D4's standing rule says that when `CacheMisses ≈ Calls` the
indicated fix is hashed-SubPlan caching rather than decorrelation. That rule
holds — but per-correlation-key memoisation cannot reach this shape: the outer
key is `c_customer_sk`, unique per outer row, so **every call is a miss by
construction**. What makes the value set shareable is removing the correlation,
which is exactly what PG's plan above shows it doing.

## 2. Upstream's mechanism

`make_subplan` (`postgres/src/backend/optimizer/plan/subselect.c:263`) builds
the ordinary correlated SubPlan first, then — for a "simple" EXISTS —
re-plans the subquery through `convert_EXISTS_to_ANY` (`subselect.c:1731`).
That function splits the body's WHERE into

* clauses with the outer var on one side and no outer var on the other →
  **lifted** into an upper-level test expression, and
* everything else → **put back** into the body,

and fails if any outer reference survives in the put-back set
(`contain_vars_of_level`). The lifted form is an ANY sublink whose body is
uncorrelated, hence hashable; `subpath_is_hashable` then checks it fits
`hash_mem`, and `setrefs.c` picks between the two alternatives at the end.

goopg already has the *second* half of this: `executor/subplan_hash.go`
(Stage 11 / D4.3) builds a hash table for a plain-equality
`InExpr{Plan: …, IsNonCorrelated: true}` once and probes it per outer row.
What was missing was the planner-side conversion that produces such an
`InExpr` from an EXISTS. That is what this change adds.

## 3. What the pass does

`rewriteExistsToAny` runs from `planner.Plan`, **after** every rewrite that
can renumber a `ColumnRef` (bushy DP, MHJ packing, NLI) and **before**
`lowerSubPlanParams`. Both bounds are load-bearing; see §5.

For each qual it walks the AND/OR spine and, for a non-negated `ExistsExpr`
reached through at least one `OR`, it:

1. requires the body's spine to be a plain restriction over a scan/join
   (`existsBodySpineSimple`) — no `Aggregate`, `Distinct`, `Sort` or `Limit`;
2. requires **exactly one** correlation reference in the body's own scope, at
   `Level 1`, and no reference in a nested sublink reaching past the body (the
   same accounting rule `collectUnnestParamsAndResiduals` applies);
3. requires that reference to sit in a top-level `=` conjunct of the body's
   own qual holder — a `*Filter`, or an INNER `*Join` (§5.2);
4. validates the sub-side column by index **and name** against the holder's
   output schema, and re-resolves the outer side against the host row (§5.1);
5. then, and only then, mutates: drops the lifted conjunct, wraps the holder
   in a one-column `Project` of the sub-side column, and returns

```go
&InExpr{Operand: <host ColumnRef>, Plan: <projected body>, IsNonCorrelated: true}
```

Every check that can fail runs before the first mutation, so a decline leaves
the `ExistsExpr` byte-identical — the SubPlan path is always the correctness
reference.

### NULL semantics — the one real hazard

EXISTS is two-valued; `IN` is three-valued. The forms differ in exactly one
cell: **operand does not match AND the value set contains a NULL** → EXISTS
says FALSE, `IN` says NULL. That difference is invisible wherever NULL and
FALSE select the same rows, which is every *qual* position: a `Filter`
predicate, and a join condition of any join type (a NULL join qual is a
non-match, like FALSE). It is **visible** under `NOT` (`NOT FALSE` = TRUE,
`NOT NULL` = NULL).

The pass therefore (a) never converts a negated EXISTS, and (b) descends
through `AND`/`OR` only — never `NOT`, `CASE`, a function argument or a
comparison operand. Upstream expresses the identical condition as
`isTopQual` → `subplan->unknownEqFalse` in `build_subplan`.

`rewriteExistsToAnyQual`'s type switch is pinned in
`exprwalk_inventory_test.go` under a role added for it, **`boundedQualSpine`**:
its arm set is not an omission to be completed by an `exprwalk` driver, it is
the transformation's correctness invariant. Widening it would make the pass
wrong, and the pin is how a later edit that widens it gets audited.

## 4. Result

SF=0.5, one binary, quiet host (`analysis/m0125-0036-exists-to-any/`):

| query | before | after | oracle rows | verdict |
|---|---|---|---|---|
| Q10 | TIMEOUT (>300 s) | **16.9 s** | 0 | rows = oracle |
| Q35 | TIMEOUT (>300 s) | **14.0 s** | 100 | rows = oracle |
| Q69 (control) | 17 s | 17 s | 100 | unchanged, still a semi/anti chain |
| Q30, Q81 | TIMEOUT | TIMEOUT | 31 / 100 | untouched — see §6 |

Q10's plan is now structurally PG's:

```
Hash Join (SEMI)
  Filter: ((c.c_customer_sk = ANY (SubPlan 1)) OR (c.c_customer_sk = ANY (SubPlan 2)))
  SubPlan 1 -> Hash Join (INNER)  rows=71886
                Filter: ((d_year = 2001) AND (d_moy >= 3) AND (d_moy <= 6))
```

Gates: units PASS; `tpch-spotcheck.sh` `RESULT=PASS` (Q12=2 Q13=35, 32.7 s);
TPC-H plan-diff vs `m0125-0035-c2-qual-placement` **1/22 (Q17)** — and the run
with `GOOPG_EXISTS_TO_ANY=off` produced the **same 1/22**, so this change is
**plan-neutral on all 22 TPC-H queries** and the Q17 divergence belongs to
`M0125-0035a`.

## 5. Two traps, both found by measurement

### 5.1 The operand index is stale after MHJ packing — Q35 returned 0 rows

The first working version took the operand's column index verbatim from the
body's `OuterColumnRef`, exactly as `subplan_lower.go:slotFor` does for the
PARAM_EXEC path. Q10 passed (its oracle is 0 rows, so it could not detect the
fault) and **Q35 returned 0 rows against an oracle of 100**.

`MultiHashJoin` packing re-sorts its output schema by OID, and `visitColumnRefs`
treats `*OuterColumnRef` and `*ExistsExpr` as opaque — so an index recorded
inside a sublink body can be stale by the time this pass runs. Reading the
stale slot yields a value set that matches nothing, which is why the failure
mode was a *silent zero* rather than an error. This is the same hazard
`M0071-0003` recorded for `unnestExistsExpr`'s residual lifting, and the
remedy is the same: re-resolve against the row the qual is actually evaluated
against.

`resolveHostOperandIdx` is deliberately **stricter** than unnest.go's
`resolveOuterSchemaIdx`, which returns the caller's stale index as a last
resort. That fallback is right for a caller already committed to a rewrite;
this pass has not committed, and a decline costs only the optimisation — so an
ambiguous name is refused, not guessed.

The isolating probe (customer-only vs +MHJ vs +SEMI) is in
`analysis/m0125-0036-exists-to-any/`. It is worth keeping as a method note:
**Q10's own acceptance row could not have caught this**, and the bug was found
only because Q35 was checked in the same pass.

### 5.2 The predicate row is not the node's output row

For a `*Join` the predicate is evaluated against `left ++ right`, but
`Join.Output()` drops the right side for SEMI/ANTI. `joinedRowSchema` builds
the former explicitly; the qual holder inside an EXISTS body is restricted to
`*Filter` or an INNER `*Join` for the same reason (there, and only there, is
the holder's `Output()` the row its own predicate sees, which is what the
projection in step 5 needs).

## 6. Deliberate divergences and what is left (ledger rows, 2026-07-31)

* **No alternative-subplan arbitration.** PG builds both forms and lets
  `setrefs.c` choose. goopg has no such machinery and — per `M0125-0026` §C5 —
  no cardinality above base scans to choose with, so the conversion is
  unconditional when the shape matches. `GOOPG_EXISTS_TO_ANY=off` is the
  escape.
* **Single correlation equality only.** PG's testexpr can be a ROW comparison
  over N pairs; goopg's `InExpr` operand is single-column, so a composite
  correlation keeps the SubPlan.
* **Only under an OR.** An AND-ed EXISTS is left to `unnestExistsExpr`, which
  produces a *streaming* semi-join rather than a materialised value set. Where
  that pull-up declines for a reason of its own — a correlation folded into an
  `IndexScan` key, or one held in a `Join.Predicate` that
  `collectUnnestParamsAndResiduals`' Filter-only walk never inspects — the
  SubPlan survives as before.
* **No plan-time `hash_mem` gate.** `subpath_is_hashable` refuses a body wider
  than `hash_mem` before committing. goopg's bound is the statement's shared
  sublink-result budget (`WorkMem/4`, ch.06 D6.4) applied at *execution* time
  by `subqCachePut`; a body that overflows it is recomputed per outer row,
  which is no worse than the SubPlan it replaced but forfeits the win.
* **Q30 / Q81 are not addressed.** §C3 lists them as the
  correlated-scalar-aggregate variant: their sublinks are scalar subqueries
  with aggregating bodies, both of which this pass declines. They remain
  `TIMEOUT` and need their own treatment.
* **A separate, pre-existing defect was found while probing** and is filed as
  its own task: two *hand-written* uncorrelated `IN (subquery)` sublinks OR-ed
  together under a SEMI join over an MHJ over-match (goopg 1329 vs PG 1294),
  while either one alone is exact (377 = PG). It is not reachable through this
  pass — the converted form answers 1294 — but it is on the same executor
  path.

## 7. Reading list

* `postgres/src/backend/optimizer/plan/subselect.c` — `make_subplan`,
  `convert_EXISTS_to_ANY`, `simplify_EXISTS_query`, `subpath_is_hashable`,
  `build_subplan`'s `unknownEqFalse`.
* `internal/executor/subplan_hash.go` — the probe this pass feeds.
* `internal/planner/unnest.go` — `unnestExistsExpr` (the AND-ed alternative),
  `collectUnnestParamsAndResiduals`, `resolveOuterSchemaIdx`.
* `analysis/m0125-0026-timeout-plans/README.md` §C3 and §C5.
