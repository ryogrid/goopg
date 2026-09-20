# M0137-0019b — admit a partial nested loop under a SEMI join

Status: LANDED 2026-09-20 (§4's decision was written BEFORE any parity number
was taken — AGENT.md C3; the fourth gate is an amendment made by measurement,
recorded in §4)
Kind: impl
Parent: M0137-0019
Milestone: M0137 (plan-parity harness)

## 1. The one query, and why it is the only planner-side member left

M0137-0019's triage classified the 16 parallel-mode `parallelism` divergences
into four families and M0137-0019a then showed families A and B are both
**executor-model divergences** that no planner change can close. TPC-H **Q4** is
the single remaining planner-side member, and it is the corpus's only plan that
is **fully SERIAL in parallel mode**:

```
goopg:  HashAggregate
          -> Nested Loop Semi Join
               -> Seq Scan on orders                 <- no partial path at all

PG:     Finalize GroupAggregate
          -> Gather Merge
               -> Partial GroupAggregate
                    -> Sort
                         -> Nested Loop Semi Join
                              -> Parallel Seq Scan on orders
```

**PG's exact shape is NOT the target of this task.** Its `Gather Merge` sits
directly over a `Partial GroupAggregate`, which needs the partial aggregate to
emit rows — the same thing M0137-0019a proved goopg cannot do
(`AggModePartial` publishes into a shared accumulator and emits ZERO rows,
`operators_join_agg.go:2351-2356`). Chasing it would repeat that loop's
mistake.

The reachable target is goopg's own split shape, which is expressible today:

```
Finalize HashAggregate -> Gather -> Partial HashAggregate -> Nested Loop Semi Join -> Parallel Seq Scan on orders
```

and the only thing standing between Q4 and it is that goopg files no partial
path beneath a SEMI nested loop.

## 2. Three coupled gates, all refusing for the same stated reason

| site | gate |
|---|---|
| `internal/optimizer/joinpathsnli.go:436` | `if jt != parser.JoinInner { refuse }` — the producer |
| `internal/optimizer/gatherpaths.go:551` | `if p.Jointype != parser.JoinInner { return PathPrebuilt }` — the path classifier |
| `internal/optimizer/parallel.go:1194` | `return p.Type == JoinTypeInner` — the ordinary-NL node twin |
| `internal/optimizer/parallel.go:1159` | `if p.Type != JoinTypeInner` — the FUSED `*NestedLoopIndexJoin` twin (found mid-loop; see §4) |

All three cite R94, and all three say the refusal is **scope**, not
correctness. `nestedLoopJoinIsPartialCapable`'s own doc comment is explicit:

> LEFT/SEMI/ANTI are worker-local on the same rationale as the twins, but Q96
> (the only consumer) is INNER and the refusal is **deliberate
> scope-minimization, not a correctness boundary** — do not widen it without a
> separate scope.

This task is that separate scope, and Q4 is the named consumer.

PG admits the wider set at the dispatch gate the producer already cites —
`{INNER, LEFT, SEMI, ANTI}`, `postgres/src/backend/optimizer/path/joinpath.c:2022-2031`.

## 3. The correctness argument, verified in the executor

A nested loop is partial through its OUTER side: each worker joins its
partition of the outer against the WHOLE inner, which it materializes and
replays itself (`openNestedLoop`, `join_nl_stream.go`). SEMI is admissible
because its verdict is **per-outer-row and worker-local**:

```go
// internal/executor/join_nl_stream.go — finishOuter()
if m.semiAnti {
        hit := m.outerMatched
        ...
        return append(Row(nil), m.outerRow...), true, nil
}
```

`m.outerMatched` is per-stream state on the worker's own outer tuple. One
qualifying inner tuple decides that tuple and the scan breaks; the joined row
is never emitted (the join's schema is outer-only). Partitioning the outer is
therefore transparent: each worker sees a disjoint slice of outer rows, probes
the complete inner, and emits at most one row per outer row. No duplicates, no
misses, no cross-worker reduction.

The state that WOULD need a cross-worker reduction — `markInner`/`fillInner`,
the inner-matched bitmap that RIGHT and FULL need — is not touched on the semi
path. That is precisely why PG's set includes SEMI and excludes RIGHT/FULL, and
why this change follows PG's set rather than inventing one.

## 4. The decision, and what is deliberately NOT widened

**Widen the jointype gates from `INNER` to `INNER or SEMI`. Nothing else.**

**Amended mid-loop, by measurement:** it is FOUR gates, not three. With the
three above widened, Q4 was still planned fully serially. The reason is that
Q4's semi join is the **fused** `*NestedLoopIndexJoin` shape — its inner is a
parameterised index probe (`Index Cond: l_orderkey = o_orderkey`), not a whole
materialised inner — so it is governed by
`NestedLoopIndexJoinIsPartialCapable` (`parallel.go:1155`, M0142-0005a) rather
than by `nestedLoopJoinIsPartialCapable`, whose own doc says a parameterised
shape "never reaches this predicate: it is a different node type, not a flag".
The fourth gate is widened with the identical argument, and the ordinary-NL
widening stays because the two twins must agree on a jointype set or a later
refactor reopens the hole. This is recorded rather than quietly folded in: the
first three edits alone would have been a correct-but-inert change.

`LEFT` and `ANTI` are in PG's set and are worker-local by the same argument,
and they are still refused here. That is not an oversight:

- **Attributability.** Q4 is the corpus's named consumer and it is SEMI. If
  LEFT and ANTI were widened in the same commit, any parity movement could not
  be attributed to the shape this task was filed for — the error this lineage
  has already had to correct three times.
- **ANTI has a second-order risk worth measuring separately.** An anti join
  emits the outer rows that did NOT match, so a mis-partitioned outer produces
  *extra* rows rather than missing ones — a louder failure, but one whose
  values gate deserves its own arm rather than sharing this one's.

Both are recorded in the deferral ledger with this doc as the resume point.

## 5. Result

### 5.1 Q4 is parallel

```
before:  Sort -> HashAggregate -> Nested Loop Semi Join -> Seq Scan on orders
         (cost 507361.63)

after:   Sort -> Finalize HashAggregate -> Gather (Workers Planned: 3)
              -> Partial HashAggregate -> Nested Loop Semi Join
              -> Parallel Seq Scan on orders
         (cost 162106.07)
```

The corpus no longer has a fully serial plan in parallel mode; family **D**
retires with one member.

### 5.2 Q4's per-query parity record

```
before:  Q4 SHAPE-DIFF [join-order,aggregation-strategy,sort-strategy,parallelism]
after:   Q4 SHAPE-DIFF [sort-strategy,parallelism]
```

Two categories retire on the query. It still carries `parallelism`, and that is
the honest reading rather than a disappointment: Q4 has moved OUT of family D
and INTO family B — PG uses `Gather Merge` + `Finalize GroupAggregate` where
goopg now uses `Gather` + `Finalize HashAggregate`, which M0137-0019a showed is
executor-floored. The record that remains is the one this task could not
remove.

### 5.3 TPC-H SF1, parallel canonical mode

Pinned epoch `e4a554b2a4cfb710`, binary `2326ec51aef8b431`, plans
`75599dae4efbf31c`.

```
before: PLAN-PARITY: queries=22 match=2 shapediff=20 unparsed=0 missingnode=0 error=0 timeout=0
        CATEGORIES-EXCL-MATCH: join-order=16 join-method=10 scan-type=10 parameterisation=7 aggregation-strategy=7 sort-strategy=11 parallelism=15 qual-placement=4 rendering=2

after:  PLAN-PARITY: queries=22 match=2 shapediff=20 unparsed=0 missingnode=0 error=0 timeout=0
        CATEGORIES-EXCL-MATCH: join-order=15 join-method=10 scan-type=10 parameterisation=7 aggregation-strategy=6 sort-strategy=11 parallelism=15 qual-placement=4 rendering=2
```

`join-order` 16 → 15 and `aggregation-strategy` 7 → 6, both from Q4 alone.
**`parallelism` does not move**, for the reason §5.2 gives.

### 5.4 TPC-DS SF0.25

`MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0`, and `queries=99 same=99
changed=0` — **inert**. No SF0.25 query pairs a semi nested loop with a
partial-capable outer, so nothing moved there.

### 5.5 stats epoch / route / seam census

Epoch `e4a554b2a4cfb710` on both TPC-H arms (pinned seed); TPC-DS is
consecutive same-day sweeps on the gate cluster. Route: PG-shaped search,
default. Seam-decline census: `N/A — this change widens a jointype admission;
it declines no join-search seam`.

### 5.6 wall time of every query whose plan changed

Q4 is the only plan that changed on TPC-H, and its estimated cost fell 3.1x
(507 361 → 162 106); no executed timing was taken (the capture is `-plan-only`).
On TPC-DS nothing changed; the sweep's two runtime moves (Q58 5s→2s, Q95 6s→3s)
are both FASTER and both are queries whose plans did not change — they are the
same pair that read slower in the M0144-0011b-1 sweep, i.e. host noise
returning to baseline.

### 5.7 Movement

`Movement: none` — match 2 → 2, and every `CATEGORIES-EXCL-MATCH` delta is
inside the ±3 band (`join-order` −1, `aggregation-strategy` −1). Q4 gaining
parallelism and shedding two of its four categories is a real structural
advance, and it is not movement under S3.

## 6. Gates

| gate | result |
|---|---|
| `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` | PASS (after the §7 test-boundary updates) |
| `scripts/tpch-spotcheck.sh` | PASS — Q12 rows=2, Q13 rows=33 |
| `scripts/tpcds-sf025-regression.sh sweep` | PASS — `MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0` |
| `scripts/tpch-acceptance-arm.sh` | PASS — 24/24 labels MATCH on VALUES |
| floor capture + `pg-plan-parity-diff.py` | PASS — TPC-H match 2, TPC-DS SF0.25 match 2 |
| `make ea-ratchet` | `N/A — no estimate, selectivity or statistics code is touched` |

## 7. Tests moved, and why that is not weakening them

Seven assertions across three files pinned the old INNER-only boundary
(`partial_nestloop_test.go`, `partial_nli_memoize_test.go`,
`internal/executor/parallel_nli_memoize_test.go`). Each was moved from the
refusal set to the admitted set for SEMI **and given a positive assertion in
its place** — SEMI must now be accepted by the predicate, by
`partialPathDrivingKind`, by all four optimizer walks and by all three
executor walks. The refusal sets keep LEFT, ANTI, RIGHT, FULL, CROSS and every
probe-shape refusal, and a new case pins that the jointype widening did not
become a bypass of `lateralProbeIsPartialProbe` (a SEMI NLI with a bitmap
inner is still refused).

## 8. Still open

- **LEFT and ANTI** remain refused in all four gates by scope, not by
  correctness. Ledger row
  `m0137-0019b-partial-nl-left-anti-still-refused`.
- **Q4's residual `parallelism` record** needs the row-emitting partial
  aggregation M0137-0019a is blocked on; it is not reachable from here.
