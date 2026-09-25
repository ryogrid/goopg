# R87 REPORT — Q96 F-branch L2 selectivity and partial-hash cost attribution

## Outcome

**F1 — partial cardinality/selectivity transcription error.**  The earliest
model mismatch is that goopg's unique-index superkey shortcut removes the
equality clause and charges `1/raw-key-rows`, without charging the nullable
foreign-side join key.  PG 18.3 uses ordinary `eqjoinsel_inner` for these
TPC-DS tables (there is no declared FK), whose no-MCV branch includes both
operands' null fractions.  This mismatch does **not** explain the whole L2
cost gap: the much larger filtered-inner/hash startup difference is a faithful
cost consequence of the two different dimensions.

This is the earliest applicable R87 outcome.  A successor must scope the
unique-index extension specifically; it must not alter Gather admission, the
partial-path comparator, a GUC, or a hash-cost constant.

## Reproducible environment and controls

The measured goopg binary was built from committed `7e2c2ae2e`:

```
/tmp/pp2/r87/goopg-r87-clean
sha256 2496560a35455fabc8e87311d6dcbdfcc71184b6efaaa680069db44b67372aaa
```

It ran in the foreground under `scripts/goopg-test-run.sh`, cgroup
`goopg-r87`, against a fresh private `cp -a` clone at `127.0.0.1:5559`.
PG was the read-only `127.0.0.1:65438/tpcds025` oracle.  Every plan/value
session pinned `work_mem=64MB`, `max_parallel_workers_per_gather=4`, and
`parallel_leader_participation=on`; the normal scan/join enable settings were
on.  Raw artifacts are retained in `/tmp/pp2/r87/`.

`q96-natural-a.txt` and `q96-natural-b.txt` are byte-identical.  The diagnostic
`GOOPG_GATHER_PATHS=top` plan is also byte-identical to the natural rendered
plan; its trace exposes the otherwise vetoed partial candidates.  Temporary
R87 cost tracing was removed before this report: `q96-top.txt` equals
`q96-instrument.txt`, and the later clean-binary controls report identical
EXPLAIN output with trace off/on for Q9, Q41, Q91, and Q96
(`control-{off,on}-q{9,41,91,96}.txt`).  No R87 instrumentation reached
execution; nevertheless Q96 was re-run after removal and both engines return
the identical value `266` (`q96-values-{goopg,pg}.txt`).

`go test ./internal/optimizer ./internal/executor
./internal/testutil/estimateaudit` and the same-package `go vet` pass.  The
temporary trace was planner logging only, was removed, and all source diffs
for `joinpathsparallel.go` and `pathtrace.go` are empty; an SF0.25 execution
sweep is consequently not required by the R87 scope.

## Inputs and L2 decomposition

Relation counts agree: both engines have `store_sales=719876`,
`household_demographics=7200`, and `store=12`; both real filters yield
`hd_dep_count=0 -> 720` and `s_store_name='ese' -> 1`.  The physical page
counts differ only slightly here (goopg/PG: store_sales `12934/12936`,
household_demographics `50/53`, store `1/1`) and the common partial
store_sales scan costs `15256.18/15258.18` respectively.

Goopg does not expose `pg_statistic` as a queryable relation (the attempted
catalog query returns `relation "pg_statistic" does not exist`), so its planner
statistics cannot be read back through that SQL view.  PG's raw inputs are
captured in `pg-inputs-stats.txt`; the two relevant nullable fact keys are:

| PG column | `stanullfrac` | `stadistinct` |
| --- | ---: | ---: |
| `store_sales.ss_hdemo_sk` | 0.044066668 | 7068 |
| `store_sales.ss_store_sk` | 0.043800000 | 6 |
| `household_demographics.hd_demo_sk` | 0 | -1 |
| `store.s_store_sk` | 0 | -1 |

`pg-q96-all-key-stat-kinds.txt` shows that the two dimension-side keys have
no MCV slot.  Neither equality therefore has MCV lists on both sides, and the
PG no-MCV branch cited below applies.  `pg-q96-fks.txt` is empty, independently
confirming that these tables contribute no declared-FK exception.

The top diagnostic's exact L2 cost inputs and terms are:

| L2 candidate | outer / inner | join-key and filtered inner | goopg selectivity / output rows / width | startup decomposition | run decomposition after common outer total | total |
| --- | --- | --- | ---: | --- | --- | ---: |
| `ss ⋈ hd` | partial seq `ss`: 232218 rows, width 428, startup `0.00`, total `15256.18`; serial seq `hd`: 720 rows, width 48, startup `0.00`, total `140.00` | `ss_hdemo_sk = hd_demo_sk`; `hd_dep_count=0`, 720/7200 | `1/7200`; 23222 / 476 | `140.00 + 9.00 = 149.00` | outer hash `580.54` + output CPU `232.22` + bucket CPU `290.27` | 16508.22 |
| `ss ⋈ store` | same partial `ss`; serial seq `store`: 1 row, width 676, startup `0.00`, total `1.15` | `ss_store_sk = s_store_sk`; `s_store_name='ese'`, 1/12 | `1/12`; 19352 / 1104 | `1.15 + 0.01 = 1.16` | outer hash `580.54` + output CPU `193.52` + bucket CPU `290.27` | 16321.68 |

Both builds have `NBatch=1`; no width-dependent spill/page term is charged.
The `ss ⋈ hd` inner bucket fraction is `1/720`, and `ss ⋈ store` is `1`, so
both clamp to one bucket tuple and have the same `290.27` bucket-walk charge.
The common outer has three workers and a `3.1` parallel divisor (the latter is
also recorded by the upper trace); its startup is `0.00`.  Both dimensions
have zero partial workers, so their serial paths are deliberately replicated
as the inner build.  The observed `186.54` L2 gap is exact decomposition, but
not entirely an F1 effect: the faithful filtered-inner/hash startup difference
is `147.8375`, and the output-CPU difference is `38.70`; their sum rounds to
`186.54`.  There is no separate width, parallel, disabled-node, or batch
contribution.

The subsequent same-relset offers confirm the election rather than introduce
a second source of the issue:

| `{ss,hd,store}` prefix | outer / inner | rows / width | total | pathkeys / disabled | insertion / comparator result |
| --- | --- | ---: | ---: | --- | --- |
| PG-shaped `(ss ⋈ hd) ⋈ store` | `{ss,hd}` / `{store}` | 1935 / 1152 | 16615.81 | 0 / 0 | offered first; later evicted |
| retained `(ss ⋈ store) ⋈ hd` | `{ss,store}` / `{hd}` | 1935 / 1152 | 16562.60 | 0 / 0 | offered second; survivor |

The normal partial-path comparator retains the latter solely on its `53.21`
lower total.  The later parameterised time-dimension NLI has
`inputtotal=16562.60`, directly proving that it consumes the non-PG survivor.

## PG-equivalent selectivity proof

The local DP shortcut in `superkeyJoinSelectivity` consumes a unique-key
clause and multiplies `1.0 / best.rawTuples`
([joinrelsize.go:397-425](../../../../../internal/optimizer/joinrelsize.go)).
Its completed-plan sibling does the same at
[joinkeyproof.go:593-602](../../../../../internal/optimizer/joinkeyproof.go).
Because the equality is consumed, goopg never reaches its otherwise correct
null-aware `eqJoinSelectivityExt`
([joinselectivity.go:302-320](../../../../../internal/optimizer/joinselectivity.go)).

PG's FK-specific rule does use `1/ref_tuples`, but only for a **declared FK**
that the planner found ([costsize.c:5651-5681](../../../../../postgres/src/backend/optimizer/path/costsize.c)); it explicitly notes that it does not
derate FK estimates for nulls ([costsize.c:5793-5804](../../../../../postgres/src/backend/optimizer/path/costsize.c)).  These TPC-DS tables have no
such declared FK.  PG therefore reaches `eqjoinsel_inner`'s non-MCV formula,
`(1-nullfrac_left) * (1-nullfrac_right) / max(nd_left, nd_right)`
([selfuncs.c:2602-2629](../../../../../postgres/src/backend/utils/adt/selfuncs.c)).

Applying that source rule to PG's recorded inputs produces the selected PG
`ss ⋈ hd` estimate exactly at EXPLAIN precision:

| candidate | goopg shortcut | PG-equivalent calculation | PG / goopg rows |
| --- | --- | --- | ---: |
| `ss ⋈ hd` | `232218 * 720 / 7200 = 23221.8 -> 23222` | `232218 * 720 / 7200 * (1 - 0.044066668) = 22198.493 -> 22198` | 22198 / 23222 |
| `ss ⋈ store` | `232218 * 1 / 12 = 19351.5 -> 19352` | `232218 * 1 / 12 * (1 - 0.0438) = 18503.904 -> 18504` | PG DP loser not forced; 18504 is the source-rule prediction / 19352 |

Live PG visibly selects the `ss ⋈ hd` prefix with `rows=22198`
(`pg-q96-live.txt`).  The second PG number is deliberately not observed by
forcing a join order or disabling paths: R87 forbids that non-natural oracle
experiment.  It is a direct consequence of PG's documented/source formula,
not a claimed live loser measurement.

The null-fraction correction is the F1 finding, not a claim that it alone
flips Q96.  Holding every other L2 term fixed, it changes the output CPU terms
from `232.22/193.52` to approximately `221.98/185.04`; the candidate gap
falls only from `186.54` to about `184.78`.  The successor must remeasure the
whole path search and L3 election after applying the faithful estimator change;
R87 does not claim a post-fix winner.

## Decision and successor boundary

The existing unique-index behaviour is a deliberate goopg extension, not a
PG 18.3 transcription: it substitutes the FK selectivity rule for a bare
unique index even though bare uniqueness proves only an upper fan-out bound,
not the fraction of nullable foreign-side values that match.  This makes it a
proper F1 issue rather than F2/F3/G.

R88 must decide, with a focused design and review before implementation, how
to retain a sound unique-key row bound while letting non-FK equality clauses
keep PG's null-aware selectivity.  It must cover both the DP sizer and the
finished-plan estimator, prove declared-FK behaviour remains PG-faithful, and
measure Q96 plus the impacted TPC-H/TPC-DS corpus.  R87 authorizes no code
change and leaves the downstream R86 Gather/NLI evidence deferred.
