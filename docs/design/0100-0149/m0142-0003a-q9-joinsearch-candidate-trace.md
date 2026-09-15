# M0142-0003a — Q9 join-search candidate trace

Status: accepted (landed 2026-09-15)

## Task

Per `.ralph/fix_plan.md`'s M0142-0003a line (filed by M0142-0001's slice
list): does goopg's DP ever construct PG's Q9 topology
(`partsupp⋈part -> ⋈supplier -> ⋈nation -> ⋈lineitem -> ⋈orders`) as a
candidate, and if so at what cost relative to the chosen NLI chain? The task
line proposed adding a new `GOOPG_JOINSEARCH_TRACE=1`-gated trace point.
Measurement-only per `AGENT.md` §"Plan-parity harness" — no production code
changed in this task.

## Finding 0 — the instrument already exists; no new trace point needed

Before instrumenting anything, `internal/optimizer/joinsearchtrace.go`
(`GOOPG_PGSHAPED_DP_TRACE=1`, built by R53 Step-0/R54 Step-0/Step-1 in the
prior methodology phase, entered via this task's own citation right per
AGENT.md's round-directory access rule) turned out to be **exactly** the
instrument the task line asked for, down to the vocabulary ("relset pairing
... with its computed cost"):

- `DPTRACE pair`/`DPTRACE decline` — every `(outer, inner)` relset pairing
  `makeJoinRel` was offered or declined, per level, per phase.
- `DPTRACE cost` — every relset's winning path (kind, total, reqouter) and
  runner-up after `setCheapest`.
- `DPPATH` (`internal/optimizer/pathtrace.go`, same gate, built by R53
  slice-1 — `path.go:405`'s `Path.OuterRelids`/`InnerRelids`) — every
  individual **path** offered for a relset, **with the partition that
  produced it**, verdict (`accepted`/`dominated`), and full cost breakdown.

So M0142-0003a needed zero new code. Adding a second, redundant trace point
would have duplicated an existing, tested, reviewed instrument for no reason
— the task's actual work is running the existing one against Q9 post-M0138
and reading the result.

## Method

Built `/tmp/goopg-m0142-0003a` (`go build ./cmd/goopg`, plain HEAD, no
diff). Cloned `bench/tpch/runtime_goopg/data` (2.0G) to a private throwaway
dir (`/tmp/pp-m0142-0003a/data`, deleted after use) rather than touching the
shared `:65433` cluster — the trace needs `GOOPG_PGSHAPED_DP_TRACE=1` at
process start (`joinsearchtrace.go:49`), which the always-on shared bench
server does not have, and the harness explicitly forbids restarting the
shared block. Capped scratch server (`GOOPG_CG_UNIT=m0142-0003a`,
`scripts/goopg-test-run.sh`) on port `5534`.

One `psql` session, `SET max_parallel_workers_per_gather = 0;` (serial,
matching `-serial`'s corpus-capture convention) then `ANALYZE` on all eight
HammerDB tables (`tpch.Tables()`'s list) then plain `EXPLAIN` on Q9's exact
SQL (`internal/testutil/tpch/tpch.go`'s `Queries()[9]`) — the same
single-connection, ANALYZE-before-EXPLAIN protocol `estimate-audit
-plan-only` uses and that `docs/design/0100-0149/m0137-0003-*.md` names as
mandatory for TPC-H (per-connection stats). Trace lines land on the server's
stderr (redirected to a log file); one `DPTRACE problem`/`DPTRACE end` block
and its interleaved `DPPATH` lines cover the whole search. Artefacts (scratch,
untracked, same precedent as M0142-0001): `analysis/m0142/m0142-0003a-q9-{plan,dptrace,dppath}.txt`.

**Caveat found along the way — this run's absolute numbers are not
byte-comparable to M0142-0001's capture.** My throwaway clone's fresh
`ANALYZE` drew its own random sample, and goopg's per-connection stats have
no seed pin (`goopg_plan_pin_three_drift_sources`) — this run's top-level
estimate is `rows=75` vs M0142-0001's `rows=146` on the same data, a ~2x
swing from sampling noise alone. **What is stable and load-bearing for this
task is the join TOPOLOGY** (driven by clause connectivity, not stats) and
the qualitative shape of the pricing story, both of which reproduce exactly
(see Finding 2). Absolute costs below are this run's own internally
consistent numbers, not to be diffed against M0142-0001's `124545.31`
figure — deriving a byte-stable one needs the seed-pin fix that ledger entry
already names, out of scope here.

## Finding 1 — PG's topology is fully enumerated at every level; this is NOT a completeness gap

Every relset on PG's chain (`{ps,p}` shared -> `+s` -> `+n` -> `+l` ->
`+o` full) appears in the `DPTRACE cost` lines with a nonzero `npaths` and
is never named in a `DPTRACE decline` line:

| level | PG relset | rows | npaths | cheapest | total |
|---|---|---|---|---|---|
| L2 | `{part+partsupp}` (shared with goopg's spine) | 24240 | 4 | hash | 36010.42 |
| L3 | `{part+partsupp+supplier}` | 24240 | 5 | hash | 36796.72 |
| L4 | `{nation+part+partsupp+supplier}` | 24240 | 7 | hash | 37131.58 |
| L5 | `{lineitem+nation+part+partsupp+supplier}` | 75 | 12 | hash | 80066.43 |
| L6 (full) | all six, via `{L5}⋈{orders}` | 75 | — | (see Finding 2) | — |

This settles M0142-0001's open question in one direction: the divergence at
Q9 is **not** "a bushy shape PG can produce that goopg's search cannot
express" (the hard-failure branch M0142-0003b's slice named). The DP offers
PG's partition at every step; `add_path`/`setCheapest` had something to
choose between at every level, all the way to the full join.

## Finding 2 — at the decisive level, PG's own chain prices to a near-exact tie with the winner, and loses on tie-break, not on a clear cost gap

`DPPATH` gives the individual candidate paths for the full relset
`{part,supplier,lineitem,partsupp,orders,nation}`, tagged by which partition
produced them (`outer`/`inner`, bit positions `0..5` =
`part,supplier,lineitem,partsupp,orders,nation` per the problem's own
`rels=` header). Restricting to the two partitions this task cares about:

**goopg's own partition — `{part,supplier,lineitem,partsupp,orders}` (its
own L5) `⋈` `{nation}`** (the winner):

| producer | verdict | total |
|---|---|---|
| `join.hash` | **accepted (CheapestTotal)** | **80099.64** |
| `join.nestloop` | dominated | 80126.49 |
| `nestloop.index` | dominated | 80102.56 / 80108.48 |
| `mergejoin` | accepted (different pathkeys) | 80102.46 |

**PG's own partition — `{part,supplier,partsupp,nation}` + `lineitem` =
`{part,partsupp,supplier,nation,lineitem}` (PG's own L5) `⋈` `{orders}`**:

| producer | verdict | total |
|---|---|---|
| `nestloop.index` | dominated | **80099.64** |
| `nestloop.index` (2nd inner path) | dominated | 80100.75 |
| `join.hash` | dominated | 189144.55 |
| `join.nestloop` | dominated | 1814751.43 |
| `mergejoin` | accepted (different pathkeys) | 366911.87 |

**PG's own chain's cheapest candidate at the full join
(`nestloop.index`, `total=80099.64`) renders identically, to the trace's
two-decimal precision, to goopg's actual winner (`join.hash`,
`total=80099.64`) — yet is marked `dominated`, not accepted.** The two
numbers are not visibly different at the precision this (unmodified,
existing) trace line prints; distinguishing "PG's chain loses by a
sub-cent margin" from "PG's chain is an exact or better tie, discarded by
insertion-order tie-break" needs more decimal digits than `DPPATH`'s
`%.2f` format carries, which this task did not change (see Deferral —
changing an existing, tested trace format's precision is instrumentation
work of the same class as M0142-0003a itself, not a recon-task edit).

This is the same *shape* of finding R53 Step-0 made pre-M0138 (`L6
hash-vs-hash, 2.5% margin` — `docs/design/not_ralph/plan_parity_fix_take2/r53-q9-costing-step0/REPORT.md`
§1-2), reproduced post-M0138 with a **much tighter** margin (now inside
two-decimal rounding, vs 2.5% then). AGENT.md's own K61/K64 rule applies
directly: **the margin must be instrumented at full precision, never
inferred from a rounded sum.**

**The marginal-cost story explains why a near-tie total hides very different
per-step economics.** Comparing each chain's L5 total to its own L6 total:

- **goopg's spine**: L5 (`{part,supplier,lineitem,partsupp,orders}`)
  total=80097.05 (`nli`) -> L6 total=80099.64 (`join.hash`, adding
  `nation`). **Marginal cost of joining `nation` last: ~2.59.**
- **PG's spine**: L5 (`{part,partsupp,supplier,nation,lineitem}`)
  total=80066.43 (`hash`, *cheaper* than goopg's own L5 by ~30.62) -> L6
  candidate total=80099.64 (`nestloop.index`, adding `orders`). **Marginal
  cost of joining `orders` last: ~33.21.**

PG's spine is the *cheaper* four-relation base (80066.43 vs 80097.05) but
pays a *much larger* marginal cost to close the join with `orders` than
goopg's spine pays to close with `nation` — and the two totals land almost
exactly on top of each other. This is the identical structural pattern R53
Step-0 found pre-M0138 ("joining orders late costs ~411954 marginal,
joining nation late ~4169" — different absolute scale, same asymmetry:
closing with `orders` is expensive relative to closing with `nation` on
this data, in both stats epochs). That stability across a stats regime
change is itself evidence the asymmetry is a real cost-model property (some
term prices `orders` as the late join expensively — `nation`'s tiny 25-row
cardinality makes it a cheap last hash build in either chain, `orders`'s
1.5M-row cardinality does not), not an artifact of the particular ANALYZE
sample.

## Verdict — answers M0142-0003b's fork directly

M0142-0003b was filed as a two-way branch: "if PG's topology is generated
but priced worse, a costing-term audit; if never generated, a join-search
completeness gap." Finding 1 resolves the fork: **PG's topology is
generated, at every level, with no declines** — this is the costing-term
branch, not completeness. Finding 2 sharpens it further: the costing
question is not "which term overprices PG's chain by a wide margin" (the
framing M0142-0003b's doc line inherited from B8/B10) but **"which term
decides a two-decimal-precision tie, and does goopg's chosen plan actually
win on true cost or only on tie-break order"** — a materially narrower and
more tractable question than a general costing-term audit.

## Deferral

One ledger row: the L6 tie-break precision question (exact-digit comparison
of `join.hash` on goopg's partition vs `nestloop.index` on PG's partition),
resume point `internal/optimizer/pathtrace.go`'s `formatPathLine` (`%.2f` ->
higher precision, or a non-production probe reading `Path.Cost.Total`
directly) feeding into M0142-0003b. Not deferring Finding 1 (join-search
completeness) — it is a closed, answered question, not a gap.

## Gates

`go build ./...` unaffected (no source file changed — pure recon, three
`analysis/m0142/m0142-0003a-*` artefacts plus this doc and the two tracker
files). No values-gate re-run needed (no production code touched). Pre-commit
hook's pgbench smoke: runs as part of `git commit` per the mandatory-on-every-commit
policy. Throwaway server/clone (`/tmp/pp-m0142-0003a`, `GOOPG_CG_UNIT=m0142-0003a`)
stopped and removed after use; the shared `:65432`/`:65433`/`:65437`/`:65438`
bench clusters were never touched.
