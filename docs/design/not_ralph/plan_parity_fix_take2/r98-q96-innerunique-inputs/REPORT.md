# R98 result: Goopg inputs reconcile; PG-private inputs remain unobservable

R98 ran the reviewed, default-off diagnostic against a private SF0.25 clone
with `GOOPG_GATHER_PATHS=top`, `GOOPG_PARTIAL_AGG_PATHS=on`, and
`GOOPG_PGSHAPED_DP_TRACE=1`. Two Q96 opt-in `EXPLAIN (COSTS ON, VERBOSE)`
captures were byte-identical. The diagnostic binary, its flag exemption, and
the temporary service were removed before this report.

## Goopg reconciliation

Every R90/R91 inner-unique call reconciled its reported final cost exactly.
The two first-level serial candidates that decide the Q96 order are:

| outer → inner | base startup/run | matched + unmatched | final total |
|---|---:|---:|---:|
| `store_sales → hdem` | 149.000000 / 22620.220000 | 85.971250 + 81.387375 | 22936.578625 |
| `store_sales → store` | 1.162500 / 22505.670000 | 71.652500 + 82.819250 | 22661.304250 |

Both have one hash clause, no residual delta, 128 MiB work memory, and no
spill (`NBatch=1`). Hdem uses `bucket=1/720`, `matchFrac=68777/719876`,
and PG-private-model geometry `1024×1`; store uses `bucket=1`,
`matchFrac=57322/719876`, and `1024×1`. Goopg fixes `matchCount=1`, so the
effective scan fraction is `2/(1+1)=1`. The full final-cost increments are
therefore 167.358625 and 154.471750 respectively. Their 275.274375 level-two
gap selects store first; subsequent alternatives also reconcile exactly to
their `DPPATH` filed totals.

Partial candidates use the same total-coordinate fractions and complete inner
builds, while `rint` applies their own outer rows: e.g. hdem
232218→22186 and store 232218→18491. Their printed terms reconcile likewise.

## PG comparison and ruling

PG18.3 source confirms the formula, but the available live EXPLAIN capture
does not expose Q96's `SemiAntiJoinFactors.outer_match_frac`, `match_count`,
or `numbuckets*numbatches`. No captured PG statistics/path inputs provide a
lawful reproducible derivation of those hidden values. Matching Goopg's own
arithmetic is not evidence that PG's hidden inputs agree.

**Ruling: UNOBSERVABLE.** R98 found no mis-transcribed Goopg arithmetic and
does not authorize a cost change. Do not tune `outerMatchFrac`, invent a
match count, change virtual geometry, or force Q96's order. Any future work
needs new PG-observable evidence or must investigate another independently
evidenced planner input.

Focused optimizer tests and vet pass after the temporary code was removed;
the source diff is empty except for this report/TODO record.
