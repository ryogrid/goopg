# Retiring the `rows<=1` CTE fallback guard (M0145-0012)

Status: MEASURED 2026-09-21, and the task as filed is a **NO-GO**. Both routes
its text offers are refuted by measurement, and the mechanism that would make
retirement correct is a third thing neither route names. No removal landed.
The task is marked `[!]` pending an owner decision.

Task: `.ralph/fix_plan.md` M0145-0012. Kind: impl. Parent: none.

## What the arm is

`initialRelRows` (`internal/optimizer/joinsearch.go`) computes an initial rel's
cardinality. For a derived leaf it reads the subtree's own estimate, and then
the M0129-S1 arm fires: if that estimate collapses to `<= 1` and the leaf is a
`*CTEScan`, substitute the CTE body's **unfiltered** row count.

The collapse it defends against is real. A `*CTEScan` has no per-column
statistics, so `filterSelectivity` charges `defaultEqSelectivity` (0.005) per
conjunct; four conjuncts over a 8325-row body give `0.005^4 x 8325 ~ 0.0001`,
which floors to 1 and makes every nested loop above it look free.

The arm is a goopg-only shape. `set_cte_size_estimates` keeps the collapsed
estimate and only floors it at 1 (`clamp_row_est`,
`postgres/src/backend/optimizer/path/costsize.c`) — PostgreSQL never
substitutes the unfiltered body count. That is why the task exists.

## Apparatus

`GOOPG_CTE_ROWS_FALLBACK=off` removes the arm (default ON = today's
behaviour), registered in the flag-provenance **table** — it is plan-shaping,
so an artefact that does not name it cannot say which cardinality the DP costed
on. A `CTEROWSFALLBACK cte=<name> collapsed=<n> body=<n>` line rides the DP
trace (`GOOPG_PGSHAPED_DP_TRACE=1`) and records each engagement.

## Where the arm engages — the whole corpus, three queries

TPC-DS SF0.25, DEFAULT arm, all 99 queries EXPLAINed:

```
query31  3 fires   cte=ws          collapsed=1  body=1846
query39  4 fires   cte=inv         collapsed=1  body=20
query74  4 fires   cte=year_total  collapsed=1  body=8325
```

No other query engages it. With the arm off, zero fires corpus-wide.

## Route 1 — "removing the arm reproduces no collapse-class plan change" — FALSE

Plan channel, arm ON vs OFF, all 99 queries: exactly those three move, and each
moves **into** the collapse class the arm was written for.

```
Q39  Hash Join                -> Nested Loop     CTE Scan inv       20   -> 1
Q31  Merge Join + Hash Joins  -> Nested Loop x3  CTE Scan ws      1846   -> 1
Q74  Hash Join x3             -> Nested Loop x3  CTE Scan year_total 8325 -> 1
```

In every case the equi-conditions stop being join conditions and become
`Join Filter` conjuncts on a nested loop — the "nested loops look free" failure
the M0129-S1 comment predicts, reproduced exactly.

Values and timing (same cluster, fresh server per arm):

```
        arm ON                 arm OFF              values
Q39     3433 ms   32 rows      3564 ms   32 rows    ck b2627d9fc1cd386d both
Q31     2133 ms    3 rows      2381 ms    3 rows    ck b2388fbfe28a7fde both
Q74     1509 ms    0 rows     25005 ms    0 rows    ck d41d8cd98f00b204 both
```

**Q74 is 16.6x slower without the arm.** Values are identical everywhere, which
is the important methodological point: the corpus value gate cannot see this
regression at all — only the plan shape and the clock move. And by M0145-0011's
own finding the class is scale-dependent, so SF1 would be worse, not better.

`derived >= guard effect` therefore does NOT hold. Removing the arm is an
automatic no-go by the task's own criterion.

## Route 2 — "qual-aware estimation through single-ref CTE bodies" — INAPPLICABLE

The task names `pushQualsThroughSingleRefCTEs`, whose inline path requires
`refs == 1` (PG 12+'s `cte_inline` criterion). Every one of the three corpus
fires is a **multi-reference** CTE: `ws` is scanned as ws1/ws2/ws3, `inv` as
inv1/inv2, `year_total` as t_s_firstyear/t_s_secyear/t_w_firstyear/t_w_secyear.
The refs==1 path can serve none of them.

## What PG actually does, and it is neither route

PG does not collapse in the first place, and the reason is upstream of
`set_cte_size_estimates`. `examine_simple_variable`
(`postgres/src/backend/utils/adt/selfuncs.c:5736-5870`) has an explicit
non-recursive-CTE arm:

```c
else if ((rte->rtekind == RTE_SUBQUERY && !rte->inh) ||
         (rte->rtekind == RTE_CTE && !rte->self_reference))
```

It walks `cteroot->parse->cteList` to find the CTE, takes its `plan_id` from
`cte_plan_ids`, fetches the corresponding `subroot` from `glob->subroots`,
reads the target-list entry for the referenced attribute
(`get_tle_by_resno`), and **recurses on the underlying `Var`**. So a qual on
`year_total.dyear` is estimated from `date_dim.d_year`'s real `pg_statistic`
row, not from `DEFAULT_EQ_SEL`. It punts only for set operations, grouping
sets, whole-row references and non-Var target entries (a constant literal in
the CTE's target list, such as `year_total.sale_type`, lands in that last case
and does fall back to the default).

**Correction (M0145-0024, 2026-09-23):** that holds for plain-SELECT CTEs,
not for set-operation CTEs. `examine_simple_variable` punts when the CTE query
has `setOperations` (selfuncs.c:5845), and Q74's `year_total` is a UNION ALL.
PG 18.3 therefore collapses Q74 too, with `rows=1` CTE Scans under three Nested
Loop Join Filters, which is goopg's arm-OFF plan. After M0145-0020/0020a, Q74
is the arm's only remaining fire. See
`m0145-0024-q74-residual-collapse-recon.md`.

**That is the port that retires this arm.** Substituting the unfiltered body
count is a crude stand-in for statistics lookup through the CTE's target list.

This also sharpens M0145-0009's finding rather than contradicting it: that
census asked which CTE-output *columns* goopg fails to resolve and concluded PG
would not resolve them either. The population here is different — these are
CTE outputs that ARE plain `Var`s over base-table columns, exactly the case
`examine_simple_variable` resolves and goopg does not attempt.

## Recommendation to the owner — the loop does not pick

1. File the `examine_simple_variable` CTE/subquery arm as its own task
   (statistics lookup through a derived rel's target list). It is the real
   prerequisite, it subsumes route 2, and it would let the arm go without
   reproducing the collapse.
2. Until then M0145-0012 is not actionable as written. Marked `[!]`.
3. If the owner prefers the arm gone before that port lands, the cost is
   explicit and measured: Q74 16.6x at SF0.25, worse at SF1, invisible to every
   value gate.

## Hard constraints honoured

No removal landed; the arm is byte-identical on the default arm (the flag
defaults to today's behaviour) and the full gate set confirms it. All
arm-off captures are private evidence on a private clone and a private port.
