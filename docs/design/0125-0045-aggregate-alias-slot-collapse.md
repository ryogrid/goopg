# 0125-0045 — Two aliases of one table collapse onto one AGGREGATE slot

**Status:** FIXED and landed 2026-08-01 (M0125-0045).
**Branch:** `tpcds-fix2`. **Filed by:** `M0125-0044`, which fixed the GROUP BY
half of the identical cause. **Acceptance:** planner unit tests + a byte-identical
PG-oracle diff on a hand-written query (no SF0.5 query reaches this defect — the
sweep ran as a no-regression check only).

## The defect

`aggregateCallKey` (`internal/planner/planner.go`) builds an aggregate call's
dedup key from `parserExprKey`, whose ColumnRef arm drops the table/schema
qualifier on purpose (GROUP BY `c` must satisfy SELECT `t.c` — M0097-0003). So
`count(d1.y)` and `count(d2.y)` over a self-joined table hash to ONE key, and
the key is consulted at every point of the aggregate pipeline:

- `collectAggregateCalls` kept only the first call (`seen` map);
- `buildAggregateStage`'s `if _, exists := aggByKey[k]; exists { continue }`
  would have discarded the second even if collected;
- `resolveExprAfterAggregate` and `resolveExpr`'s havingAgg outer-reference
  path dispatch SELECT/ORDER BY/HAVING copies of a call through the same map.

Measured before the fix (the -0044 filing's probe): on
`SELECT count(d1.y), count(d2.y) FROM fact, dim d1, dim d2 WHERE …` BOTH
targets resolve to agg slot 0 — `count(d2.y)` silently reports `count(d1.y)`.
Same *right cardinality, wrong values* signature as M0125-0044, M0125-0013 and
M0125-0009: every row-count gate stays green.

PostgreSQL keys aggregate equality on the **resolved** argument Vars —
`equal()` over `Aggref->args` (`src/backend/nodes/equalfuncs.c`) — which
separates the aliases by varno and merges genuinely-equal spellings.

## The fix

The -0044 contested-key treatment, applied to aggregate calls. `parserExprKey`
and `aggregateCallKey` stay blind — their blindness is load-bearing for dedup
across clauses (`count(x)` in SELECT must find `count(x)` in HAVING). A SECOND
key keeps the qualifiers and is consulted only where the first is contested.

- `qualifiedAggregateCallKey` (`internal/planner/groupby_alias_key.go`) =
  `aggregateCallKey` + `#` + the -0044 reflective qualifier walk
  (`appendRefQualifiers`) over the whole `parser.FuncCall` — arguments,
  FILTER, in-argument ORDER BY — so computed arguments (`sum(d1.y + 0)`)
  come free, with no second hand-written switch.
- `collectAggregateCalls` dedups on the QUALIFIED key, so both aliases survive
  collection. Plain repetition (`count(d1.y)` twice) still collapses later.
- `buildAggregateStage` pre-scans the collected calls (targets + HAVING
  subquery calls): a blind key claimed by calls whose qualified forms differ is
  marked in `aggregateAmbiguous`. Contested calls dedup and register in
  `aggregateByKeyQual`; uncontested behaviour is byte-identical to before.
- Both resolution sites (`resolveExprAfterAggregate`, `resolveExpr`'s
  havingAgg arm) check `aggregateAmbiguous[k]` and dispatch contested calls
  through the qualified map, falling back to the blind binding on a miss
  rather than failing.

"Contested" is *qualified forms differ*, not *key seen twice* — the -0044
lesson that `GROUP BY a, a` is one slot named twice carries over: the same
call written twice is one aggregate.

## Recorded deferral (ledger row 2026-08-01)

Keying contested calls on the qualified **parser** form splits only — it can
never wrongly merge — but it can split what PG merges: `count(y)` and
`count(t.y)` naming the SAME binding differ in qualified form, so if a third
spelling makes the key contested, they get two slots computing identical
values. Redundant work, never a wrong answer. Merging them requires the
resolved-form equality PG uses (`equal()` over `Aggref->args`), i.e. a
canonical key over the *resolved* `AggregateCall` — and -0044 recorded why a
naive resolved-form comparison is a trap: resolved indices are against the
child schema, which join reordering permutes. Resume point:
`buildAggregateStage`, a content key over the resolved `pa` (argument exprs,
DISTINCT, FILTER, ORDER BY, WITHIN GROUP) computed AFTER the child plan is
final.

## Verification (all PASS, 2026-08-01)

- New `internal/planner/aggregate_alias_collapse_test.go`: distinct slots for
  `count(d1.y)`/`count(d2.y)` (targets AND `Aggregate.Aggs` arg indices),
  computed-argument variant, repetition-still-dedups guard (SELECT twice +
  HAVING), and the -0044+-0045 stacked shape with ORDER BY dispatch.
- PG-oracle diff: hand-written dim/fact data with asymmetric NULLs
  (`count(d1.y)=3, count(d2.y)=1`), three query shapes (plain, GROUP BY +
  ORDER BY, HAVING comparing the two counts) — goopg output byte-identical to
  PG 18.3 (`analysis/tpcds-sf05-m0125-0045/m0125-0045-oracle.sql` + the two
  captured outputs beside it, `m0125-0045-goopg.txt` / `m0125-0045-pg.txt`).
- planner + executor + parser suites; `RALPH_PRECOMMIT_SCOPE=units`.
- `scripts/tpch-spotcheck.sh` RESULT=PASS (Q12=2, Q13=35).
- Full 99-query SF0.5 gate (no-regression only), one private binary
  `tmp/goopg-sf05-m0125-0045-bin`, 3 chunks
  (`analysis/tpcds-sf05-m0125-0045/gate/`): **PASS=95 MISMATCH=0 CKMISMATCH=0
  ERROR=0 TIMEOUT=0 SKIP=4**, diffed cell-by-cell vs loop #18 — ZERO movement
  in status, rows or checksum.
