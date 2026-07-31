# 0125-0041 — a correlated scalar-aggregate sublink over a WITH item

*TPC-DS Q30 / Q81. Status: root cause found and fixed; the queries still time
out, for a different and now-isolated reason.*

## 1. The shape

Q30 and Q81 are the same query modulo the fact table:

```sql
with customer_total_return as
 (select wr_returning_customer_sk as ctr_customer_sk, ca_state as ctr_state,
         sum(wr_return_amt) as ctr_total_return
  from web_returns, date_dim, customer_address
  where wr_returned_date_sk = d_date_sk and d_year = 2000
    and wr_returning_addr_sk = ca_address_sk
  group by wr_returning_customer_sk, ca_state)
select … from customer_total_return ctr1, customer_address, customer
where ctr1.ctr_total_return > (select avg(ctr_total_return)*1.2
                               from customer_total_return ctr2
                               where ctr1.ctr_state = ctr2.ctr_state)
  and ca_address_sk = c_current_addr_sk and ca_state = 'AR'
  and ctr1.ctr_customer_sk = c_customer_sk
```

`M0125-0026` §C3 filed them next to the EXISTS class that `M0125-0036` closed,
and priced them at ≈2×10¹³ row-touches: ~10⁹ outer pairs, each re-scanning the
~2×10⁴-row CTE inside the scalar sublink.

The filing warned explicitly that `-0036`'s EXISTS→ANY pass could not be
expected to generalise, since a correlated `avg(...)` asks a per-group question
rather than a set-membership one, and named the scalar-sublink pull-up
(`unnest.go`: GROUP BY the correlation key, then hash-join) as the mechanism
that *should* already handle it. The open question was **why it declined**.

## 2. It did not decline — it crashed into a missing switch arm

Measured with a throwaway probe that planned the Q30 skeleton against a
synthetic catalog and printed each gate's verdict:

| gate | verdict |
|---|---|
| `canUnnestSubquery` | **true** |
| aggregate is NULL-on-empty (`nullOnEmptyAggregates["avg"]`) | true |
| target list strict (`nullPreservingScalarTarget` on `avg*1.2`) | true |
| conjunct is AND-reachable (`subqueryANDReachable`) | true |
| correlation params / residuals | 1 / 0 |
| `innerPlanIsIndexProbeCheap` | false (so the S6 policy does not veto) |
| `findFilterContainingSubquery` | found |
| **`buildUnnestedSubquery`** | **`XX000: clonePlanReplacingOuter: unsupported plan node`** |

Every guard the filing suspected *accepts* this shape. The pull-up then built
its decorrelated inner plan by cloning the sublink body with
`clonePlanReplacingOuter`, whose switch has arms for `Join`, `Filter`,
`Project`, `Aggregate`, `Sort`, `Limit`, `SeqScan`, `IndexScan`,
`MultiHashJoin` and `Values` — and no arm for `*planner.CTEScan`. The sublink's
FROM is a WITH reference, so the clone hit `default:` and returned an error.

`unnestSubquery`'s caller treats a clone failure as "leave it as a SubPlan", so
the query planned fine and simply kept the per-outer-row shape. Nothing logged,
nothing failed — the decorrelation was unreachable for *any* query whose
correlated sublink reads a CTE.

That is the general lesson worth keeping: **a bail-shaped error path made a
capability gap look like a policy decision.** The filing's "find out why the
pull-up declines" was the right instruction; the answer was that it never got
to decide.

## 3. The fix

`clonePlanReplacingOuter` gains two arms (`internal/planner/unnest.go`):

- **`*CTEScan`** — copy the node, share the body subtree verbatim. The
  correlation can never live *inside* the body, because `WITH` is not `LATERAL`:
  a CTE body is a closed query level, and the correlation sits in a `Filter`
  above the `CTEScan` (which is exactly the shape measured). Sharing the body is
  also what the CTE machinery already does for two consumers of one WITH item —
  planner.go's Stage A inlining hands each consumer the same `ce.body` — and it
  is what makes the decorrelated plan cheap: the executor's name-keyed
  `ctx.CTERowCache` materializes the body once and both scans replay it.
- **`*MaterializedCTEScan`** — the DML-CTE sibling; a leaf reading
  `ctx.MaterializedCTEs[name]`, so the copy is unconditional.

**The one hazard, and its guard.** That name-keyed cache is precisely why a body
carrying an outer reference must *not* be shared: a rewritten body and an
un-rewritten consumer of the same CTE name would collide on one cache entry, and
the second scan would replay the first's rows. `planSubtreeHasOuterRefDeep`
(a sublink-descending variant of `planHasOuterRefRemaining`) refuses the clone in
that case, returning the pull-up to the SubPlan path. No level/depth arithmetic
is attempted — any outer reference at all is a bail, the safe direction.

`planCloneSupported` — the sibling predicate that reports whether a subtree is
clonable at all — gains the matching arms, with the same outer-reference
condition, so the two cannot drift (`docs/design` house rule: sibling paths
change together).

## 4. Result

Q30's plan on the SF=0.5 cluster loses `SubPlan 1` entirely:

```
 Hash Join (INNER)
   Filter: ((ctr_total_return > (avg * 1.2)) AND (customer_address.ca_state = 'AR'))
   ->  Nested Loop (INNER)                       -- ctr1 ⋈ customer (index probe)
     ->  Nested Loop (CROSS)                     -- ctr1 × customer_address  ← §5
       ->  CTE Scan on customer_total_return ctr1
       ->  Seq Scan on public.customer_address (rows=50000)
     ->  Index Scan using public.customer_pkey on public.customer
   ->  HashAggregate (1 keys)                    -- avg(ctr_total_return) BY ctr_state
     ->  CTE Scan on customer_total_return ctr2
```

which is the intended form: one grouped aggregate over the CTE, hash-joined to
the outer on `ctr_state`, replacing ~10⁹ re-evaluations.

Correctness is pinned by an **equivalence** test rather than a plan-shape
assertion (`internal/executor/cte_scalar_sublink_unnest_test.go`): the Q30
skeleton runs over the same data with the pull-up on and off, and the two row
sets must be identical. The control arm asserts it really took the SubPlan path,
so the comparison cannot silently become vacuous. The fixture includes a NULL
correlation key, where both paths must drop the row for different mechanical
reasons (empty group ⇒ `avg` NULL ⇒ NULL comparison, vs. a NULL hash key that
never probes).

### 4.1 Gates

- **TPC-DS SF0.5, full 99-query sweep**: `PASS=89 MISMATCH=0 CKMISMATCH=0
  ERROR=0 TIMEOUT=6 SKIP=4` — **all 99 cells identical in status, rows AND
  checksum** to the pre-change baseline (`sweep-20260731-121447.txt` vs
  `sweep-20260731-141216.txt`).
- **TPC-H plan A/B**, same cluster, `git stash` on `unnest.go` only:
  **0/22 changed, byte-identical** (`plan_snapshots/m0125-0041-{before,after}.txt`).
  TPC-H has no correlated scalar sublink over a CTE, so plan-neutrality here is
  the expected result and the check is a blast-radius bound, not a proof.
- `scripts/tpch-spotcheck.sh` `RESULT=PASS` (Q12=2, Q13=35).

**Observation for whoever runs the next plan-shape gate.** `make plan-diff
LABEL=tpcds-round2-head` now reports **22 / 22 queries diverged**, and it does so
with *or* without this change (the A/B above shows the live plans are identical
in both arms), so it is **pre-existing baseline staleness, not a regression**.
The divergence is systematic: the stored snapshot has bare `Seq Scan on
public.orders` and no `Gather`, while every live plan carries `(stats)`
annotations, real row estimates and parallel workers. That is the signature of a
baseline captured S-cold against a cluster the warm-statistics programme
(`M0125-0028`…`-0030`, which made the bench build scripts ANALYZE) has since
warmed. The label needs re-capturing before it can discriminate anything again;
until then a plan A/B against a stashed working tree is the only TPC-H
plan-shape evidence that means something.

## 5. What still times out, and why it is not this task

**Q30 and Q81 remain `TIMEOUT` at SF=0.5 — Q30 measured at both 300 s and
1200 s budgets on 2026-07-31.** Doubling the budget four-fold does not reach it,
so, exactly as with Q21 under `M0125-0032`, this is a shape defect and not a
crossing near the budget.

The residual is visible in §4's plan: `Nested Loop (CROSS)` between the CTE
(~2×10⁴ rows) and a full `customer_address` scan (5×10⁴ rows) — **10⁹ pairs,
each driving an index probe into `customer`**, before the top filter runs. That
is C1, filed as `M0125-0034` ("goopg emits a Cartesian product whenever a join
input …"), which `M0125-0026` §C3 already named as *compounding* this class.

Note also that `ca_state = 'AR'` — a single-table local filter that would shrink
`customer_address` by ~50× — is still sitting in the top `Filter` alongside the
(formerly sublink-bearing) conjunct rather than on the scan. Whether that is a
consequence of the sublink having been in the same predicate is the concrete
first probe for `-0034`'s work on this query.

So the two factors of §1's 2×10¹³ price are now separated: this task removed the
CTE-rescan factor; the 10⁹-pair factor is `M0125-0034`. **`M0125-0041` stays
unchecked** — its acceptance is a completing Q30 — with the dependency recorded
in `.ralph/deferral_ledger.md`.

## 6. Deferred

- **Q30/Q81 acceptance** — blocked on `M0125-0034` (C1). Ledger row 2026-07-31.
- **`WITH` inside a sublink does not parse.** goopg's parser rejects
  `… > (WITH c AS (…) SELECT avg(x) FROM c)` with a syntax error, while
  PostgreSQL accepts a `WITH` clause in any sub-`SELECT`
  (`postgres/src/backend/parser/gram.y`, `simple_select`/`select_with_parens`).
  A consequence for this change: the outer-reference guard in §3 is currently
  **unreachable from SQL**, since the only way to put an outer reference inside a
  CTE body is to define the CTE inside the correlated subquery. The guard is kept
  — it is the condition that makes verbatim body-sharing sound, and the parser
  gap is not a property the planner should depend on. Ledger row 2026-07-31.
