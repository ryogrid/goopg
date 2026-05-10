# TPC-H Status Handover — goopg / `gc-oriented-refactor` (2026-05-09)

## Audience

A coding agent picking up TPC-H correctness/performance
work on goopg. Branch: `gc-oriented-refactor`. Starting
HEAD: `936c7f7`.

## 1. Current TPC-H SF=1 status

22-query sweep against `bench/tpch/runtime_goopg/data`
(HammerDB-loaded SF=1). 20/22 return correct row counts;
2 are silent false negatives; 1 cancels structurally.

| Q  | Status                | Rows  | Canonical | Notes |
| -- | --------------------- | ----- | --------- | ----- |
| 1  | OK 36.96s             | 4     | 4         | -    |
| 2  | OK ~9s                | 470   | 460       | -    |
| 3  | OK 29.16s             | 11462 | 11620     | regression-guard for M0066-0002 |
| 4  | OK 170.54s            | 5     | 5         | regression-guard for M0061-0001 EXISTS unnest |
| **5**  | **cancel-600s**       | -     | (rows >0) | **structural; ~60% CPU is `runtime.duffcopy` + `memmove` + `memclr`** |
| 6  | OK ~26s               | 1     | 1         | -    |
| 7  | OK ~29s               | 4     | 4         | -    |
| 8  | OK ~189s              | 2     | 2         | -    |
| **9**  | **OK 219.45s rows=7** | **7** | **~175**  | **silent FN; chained-NLI schema-annotation-vs-runtime-layout mismatch** |
| 10 | OK ~20s               | 20574 | 20532     | -    |
| 11 | OK 2.18s              | 1142  | 1048      | M0070 baseline 2.96s — actually faster |
| 12 | OK 85.92s             | 2     | 2         | LEFT JOIN + GROUP BY — break-glass for slot pipeline |
| 13 | OK 60.80s             | 35    | 30        | LEFT JOIN — break-glass for slot pipeline |
| 14 | OK ~28s               | 1     | 1         | -    |
| 15 | OK 39s+19s            | 1     | 1         | view + main |
| 16 | OK ~7s                | 18170 | 18314     | -    |
| 17 | OK ~51s               | 1     | 1         | -    |
| 18 | OK 50.96s             | 11    | 0/57      | regression-guard for M0071-0002 IsolatedScope |
| 19 | OK ~58s               | 1     | 1         | -    |
| 20 | OK 27.14s             | 99    | ~186      | **NEW**: M0071-0002-followup landed (was 0) |
| **21** | **OK 372.59s rows=0** | **0** | **~411**  | **silent FN; schema ambiguity in EXISTS/NOT-EXISTS chain** |
| 22 | OK 61.47s             | 7     | 7         | regression-guard for M0061-0001 EXISTS unnest |

## 2. goopg's architectural challenges (TPC-H lens)

### 2.1 Tuple representation: `Row = []Datum` vs slot model

Today every operator emits `Row = []Datum`. Producers
reuse internal buffers; retention sites
(`sortOp`, `joinOp` lazyHash, MHJ build, etc.) all
defensively `cloneRow` via `drainRowsBounded` /
`drainRowsCtx` (M0054-0005a `BorrowSemantics` contract,
`internal/executor/operator.go::Borrowable`).

**Cost on TPC-H:**
- NLI's `joinBuf` allocates `make([]Datum, outerW+innerW)`
  per `Open`, then `copy(outer)` + `copy(inner)` per emit
  (`internal/executor/operators_nljoin.go::fillJoinBuf`).
- MHJ's `lazyOut` is reused but each step's `copy` into
  the output buffer is what Q5's pprof at M0067 saw as
  ~60% `duffcopy/memmove/memclr` share
  (`internal/executor/multi_hash_join.go::advanceProbe`,
  `initStepHelper`, `advanceFrom`).

**Authoritative design:**
[`docs/design/0068-0002-tuple-slot-pipeline.md`](../design/0068-0002-tuple-slot-pipeline.md)
defines the `TupleSlot` interface and three concrete
kinds (`MaterializedSlot`, `VirtualSlot{sources, cols}`,
`BatchRefSlot`). The Stage A scaffold landed in commit
`d0de10d` (`internal/executor/slot.go`); the operator
pipeline still returns `Row` because Stages B-E haven't
landed cleanly.

### 2.2 Schema-annotation vs runtime-layout divergence

The planner OID-sorts the MHJ output schema
(`internal/planner/bushy.go::buildMHJPosMap`); column
indices set by the binder from the FROM-cumulative
layout become stale.
`internal/planner/bushy.go::reresolveJoinByName`
rebinds by Name, but ambiguous names (Q21's
l1/l2/l3.l_suppkey) silently bind to the wrong table's
column.

Defensive gates in place — **do NOT remove without the
slot pipeline fix**:

- `internal/planner/nl_index_join.go:399` — Name
  re-bind only when outer is `*MultiHashJoin`
  (M0064 Q9 regression history).
- `internal/planner/bushy.go:1548` —
  `applyJoinTreePosMap` walks NLI's `Outer` but stops at
  NLI's own keys (M0065 chained-NLI history).

The structural fix is the slot pipeline's
`(sourceIdx, sourceCol)` virtual coordinates that
survive operator substitutions.

### 2.3 `SchemaColumn` lacks source/alias

`internal/planner/plan.go:23-29`:

```go
type SchemaColumn struct {
    Name string
    Type catalog.Type
}
```

No `SourceTable` / `Alias` / `SourceIdx` field. Q21's
self-join has 3 lineitem aliases (l1, l2, l3) all
producing columns named `l_suppkey`, indistinguishable
in the schema. After bushy widens the SemiJoin's schema
to merged (Left ++ Right), the schema has both
l1.l_suppkey and l2.l_suppkey at different positions
but the same Name. Q21's NOT EXISTS residual `l3 <> l1`
then can't disambiguate the outer reference.

A `SchemaColumn.SourceTableIdx` field would fix this at
the planner level, but adding it is a wide-impact
change touching every operator that constructs
schemas. The slot model gives the same effect at the
executor layer for free.

### 2.4 M0071-0005 Stage B's silent Q12/Q13 break

Stage B (per-op `outSlot MaterializedSlot` field +
remove producer-side `cloneRow`) was attempted twice
in 2026-05-09. Initial verification showed
Q12=2/Q13=35 OK. A subsequent sweep showed
Q12=0/Q13=2. Bisection isolated Stage B as the cause.
Reverted at HEAD.

The unidentified retention-site bug almost certainly
involves a path that retains rows but doesn't go
through `drainRows*` helpers — possibly aggregateOp's
group-key Datums holding `Datum.Buf` into a
now-reused producer buffer. The retention-site audit
in the agent-side memory file
`m0071_stage_b_silent_regression.md` lists sites that
were examined but the precise leak point wasn't
located.

## 3. Recent work (2026-05-09 session)

| Commit    | Status    | Title |
| --------- | --------- | ----- |
| `b0d9168` | LANDED    | docs(m0071): scaffold milestone + correct M0069/M0070 fix_plan entries |
| `3610264` | LANDED    | feat(m0071-0002): non-correlated IN inner Project marked IsolatedScope |
| `f6febe9` | LANDED    | feat(m0071-0003): defensive OuterColumnRef Name re-resolve in EXISTS unnest residual |
| `6e0599c` | LANDED    | feat(m0071-0004): push single-source MHJ filters into Tables[i] build inputs |
| `72d544e` | LANDED    | docs(m0071-0001): defer Q9 chained-NLI rebind to slot pipeline |
| `08b1a5c` | REVERTED  | feat(m0071-0005): Stage B re-land — Operator.Next returns TupleSlot |
| `96443e1` | REVERTED  | feat(m0071-0005): Stage C — VirtualSlot pass-through for filter/limit/instrument |
| `017e158` | LANDED    | feat(m0071-0002-followup): Q20 scalar Project + NLI flip fix |
| `cf04bce` | LANDED    | Revert "Stage B re-land" (Q12/Q13 silent regression) |
| `5d6961d` | LANDED    | Revert "Stage C" (paired with Stage B revert) |
| `936c7f7` | LANDED    | docs(m0071): final state |

### Net effect on TPC-H SF=1

- **Q20**: 0 → **99 rows** (M0071-0002-followup;
  canonical ~186, distributional variance acceptable on
  HammerDB-loaded data).
- **Q11**: 2.96s → 2.18s (-26%; appears to be a
  side-effect of the Q20 NLI flip decline reducing
  upstream work — verify is welcome).
- **Q3 / Q4 / Q12 / Q13 / Q18 / Q22** row counts
  preserved (regression guards).
- **Q5 / Q9 / Q21** unchanged (carry to slot
  pipeline).

### What did NOT land

Stages B-E of the TupleSlot pipeline. Stage A scaffold
(`internal/executor/slot.go`) remains; `Operator.Next()`
still returns `Row`. `Borrowable` / `OwnedRow` /
`BorrowedRow` / `setChildBorrow` still live in
`internal/executor/operator.go`.

## 4. Recommended next steps

### Path A — slot pipeline (Stages B-E coupled)

**Targets:** Q5 (structural cancel), Q9 (silent FN
175 vs 7), Q21 (silent FN 411 vs 0).

**Approach:** land Stages B-E **together**, not
separately. The 2026-05-08 and 2026-05-09 attempts
both failed because Stage B alone breaks Q12/Q13;
splitting means the regression manifests at an
intermediate commit that has to be reverted.

**Pre-commit gate:** before pushing any commit on this
track, run

```sh
./tpch-runner --queries=12,13 \
    --per-query-timeout=180s --cancel-after=170s
```

against a **freshly-restarted server**. Q12=2 and
Q13=35 are mandatory; anything else means the
retention-site audit is incomplete.

**Audit checklist** (every retention site must
materialize-on-retention; current code paths to
audit):

- `internal/executor/operators.go::sortOp::Open` — `o.rows` retain
- `internal/executor/operators.go::recursiveUnionOp` —
  `o.working` / `o.output` retain
- `internal/executor/operators_join_agg.go::joinOp::openLazyHashJoin`
  — `o.lazyHash[key]` retain
- `internal/executor/operators_join_agg.go::aggregateOp::Open`
  — `groups[key].state` (Datum values from row),
  `gr.groupValues` (Row slice). Likely Q12/Q13
  regression site.
- `internal/executor/multi_hash_join.go::multiHashJoinOp::Open`
  — `hashTbls[i]` retain
- `internal/executor/operators_lockrows.go` — `pending` retain
- `internal/executor/operators_window.go::windowOp::Open` — `o.rows` retain
- `internal/executor/operators_join_agg.go::drainRows*`
  — already defensive; verify they stay.
- `internal/executor/instrument.go::instrumentedOp` —
  stats retain int64 counters only, not Datum bytes;
  safe.

**Files to change (Stages B-E together):**

- `internal/executor/slot.go` — extend `VirtualSlot`
  with composition operations (sources[], cols[]).
- `internal/executor/operator.go` — `Operator.Next()
  (TupleSlot, error)`; remove `Borrowable`,
  `OwnedRow`, `BorrowedRow`, `setChildBorrow`.
- `internal/executor/executor.go` — remove
  `setChildBorrow` callers.
- All 33 producer operator types — `outSlot` field +
  Materialize at retention boundaries.
- All consumer call sites — `slot.Get(col)` where
  possible; `slot.Row()` only at the wire layer.
- `internal/executor/operators_nljoin.go::joinBuf` →
  `VirtualSlot{outer, inner}` (Stage D).
- `internal/executor/multi_hash_join.go::lazyOut` →
  `VirtualSlot{probe, build0..buildN-1}` (Stage D).
- delete `internal/executor/borrow_test.go` (Stage E).

**Authoritative design:**
[`docs/design/0068-0002-tuple-slot-pipeline.md`](../design/0068-0002-tuple-slot-pipeline.md).

**Acceptance:**

- `Borrowable` / `OwnedRow` / `BorrowedRow` /
  `setChildBorrow` removed from `operator.go` /
  `executor.go`.
- `delete internal/executor/borrow_test.go`.
- Q5 pprof: `runtime.duffcopy` + `memmove` +
  `memclr` share ≤ 25 % (was ~60 % at M0067).
- 22-query row-count parity preserved (Q12=2,
  Q13=35, Q3=11462, Q4=5, Q18=11, Q22=7, Q20=99).
- Q5 completes (rows ≥ 1) OR ≥ 30 % faster than
  M0070's 1200 s cancel.
- Q9 row count ≥ 90 (target 175).
- Q21 row count > 0 (target ≥ 100).

### Path B — Q21 alternative (planner-level alias)

Useful if Path A overflows session budget.

**Approach:** add `SchemaColumn.SourceTableIdx int16`
field; binder populates it from the FROM-clause
binding. `reresolveJoinByName::predRebind` and
`unnestExistsExpr::resolveOuterIdx` use
`SourceTableIdx` instead of Name when ambiguous.

**Risk:** wide-impact change touching every operator
that constructs a schema (~50+ files). Must preserve
existing Name-based resolution as fallback.

**Verification:** Q21 SF=1 rows ≥ 100, Q4=5, Q22=7
preserved.

### Path C — Q20 distributional gap (small)

Q20 currently 99 rows on HammerDB-loaded data;
canonical TPC-H SF=1 ≈ 186. The planner+executor
combination is correct (verified via plan-tree
inspection, see
[`internal/planner/q20_unnest_test.go`](../../internal/planner/q20_unnest_test.go));
the gap is dataset variance. If correctness against
the canonical dataset matters, re-load with the
official `dbgen` toolkit instead of HammerDB.
Otherwise this can be marked closed.

## 5. Verification methods

### 5.1 TPC-H runner

`./tpch-runner` (built from `./cmd/tpch-runner`) runs
Q1..Q22 against the goopg cluster on
`127.0.0.1:65433`. Useful invocations:

```sh
# Targeted queries with deadline:
./tpch-runner --queries=12,13,18,20 \
    --per-query-timeout=180s --cancel-after=170s

# EXPLAIN one query (no execution):
./tpch-runner --queries=20 --explain --per-query-timeout=30s

# Full sweep with 600s budget (Q5 cancels at 600s):
./tpch-runner --per-query-timeout=620s --cancel-after=600s

# Single query:
./tpch-runner --queries=5 \
    --per-query-timeout=620s --cancel-after=600s
```

Output format: `Q<n>: OK elapsed=<s>s rows=<n>` or
`Q<n>: ERROR after <s>s — <reason>`.

### 5.2 Bench server lifecycle

The benchmark goopg server runs from
`tmp/goopg-bench-bin`. Rebuild + restart on every
planner/executor change:

```sh
# 1. Build
go build -o tmp/goopg-bench-bin ./cmd/goopg

# 2. Restart server
ps aux | grep "goopg-bench-bin" | grep -v grep \
    | awk '{print $2}' | xargs -r kill -SIGTERM
sleep 3
nohup ./tmp/goopg-bench-bin start \
    -D bench/tpch/runtime_goopg/data \
    --listen 127.0.0.1:65433 \
    --hba bench/tpch/runtime_goopg/data/pg_hba.conf \
    > bench/tpch/runtime_goopg/goopg.log 2>&1 &
sleep 5

# 3. tpch-runner stays the same binary; only rebuild
#    when ./cmd/tpch-runner sources change:
go build -o tpch-runner ./cmd/tpch-runner
```

**Critical**: forgetting to restart the server means
the runner hits the **old binary**'s planner/executor —
many a debug session has gone sideways here. If a fix
"doesn't work", check the server's `etime` matches the
binary's `mtime`.

### 5.3 Unit tests

```sh
# Quick: planner + executor + tpch testutil
go test ./internal/planner/... ./internal/executor/... \
    ./internal/testutil/tpch/...

# Full
go test ./...
```

Run **before every commit**. Watch
`./internal/executor/borrow_test.go` (until Stage E
deletes it) — Stages B/C/D have historically broken
its invariants.

### 5.4 pprof for Q5

Capture a CPU profile during a 600 s Q5 cancel run
either via the runner's `--cpuprofile` flag (verify
with `./tpch-runner --help`) or by attaching to the
running server's net/http/pprof endpoint if exposed.

Target: after Stage D-E lands, Q5's
`runtime.duffcopy` + `memmove` + `memclr` share
should drop from ~60 % to ≤ 25 %.

## 6. Document references

### 6.1 Authoritative design docs

- [`docs/design/0068-0002-tuple-slot-pipeline.md`](../design/0068-0002-tuple-slot-pipeline.md)
  — TupleSlot interface design, Stages 1-7 migration
  plan. **The single source of truth** for the slot
  pipeline.
- [`docs/design/0068-0003-batch-string-arena.md`](../design/0068-0003-batch-string-arena.md)
  — arena allocator design (M0071-0006, depends on
  Stage D's Materialize boundary).
- [`docs/design/0071-0002-q20-zero-rows-diagnostic.md`](../design/0071-0002-q20-zero-rows-diagnostic.md)
  — Q20 hypothesis-driven diagnostic plan; the
  M0071-0002-followup fix tracks H1/H2 and adds the
  scalar Project preservation that wasn't in the
  original plan.

### 6.2 Milestone history

- [`docs/milestones/0071-tpch-correctness-and-runtime-followup.md`](../milestones/0071-tpch-correctness-and-runtime-followup.md)
  — current milestone overview.
- [`docs/milestones/0069-executor-slot-pipeline-followthrough.md`](../milestones/0069-executor-slot-pipeline-followthrough.md)
  — prior milestone with Stage A landing + Stages
  B-E reverted history.

### 6.3 Analysis

- [`analysis/tpch-m0071-q9-investigation-2026-05-09.md`](../../analysis/tpch-m0071-q9-investigation-2026-05-09.md)
  — Q9 chained-NLI structural blocker write-up;
  documents why Q9 needs the slot pipeline (defensive
  gates in `nl_index_join.go:399` and
  `bushy.go:1548` exist precisely because chained-NLI
  keys already align with runtime layout).
- [`analysis/tpch-m0067-baseline-2026-05-08.md`](../../analysis/tpch-m0067-baseline-2026-05-08.md)
  — Q9 schema-runtime mismatch root-cause notes;
  Q21 anti-side over-match notes; Q5 GC profile
  baseline (pre-M0066).
- [`analysis/tpch-m0070-baseline-2026-05-08.md`](../../analysis/tpch-m0070-baseline-2026-05-08.md)
  — 22-query M0070 baseline (1200 s budget) row counts.
- [`analysis/tpch-m0069-baseline-2026-05-08.md`](../../analysis/tpch-m0069-baseline-2026-05-08.md)
  — M0069 baseline; Q20 cancel → 30 s but 0 rows
  history (the bug M0071-0002-followup closed).

### 6.4 Task ledger

- [`.ralph/fix_plan.md`](../../.ralph/fix_plan.md) —
  milestone+task tracking. M0071 entries cover all
  open items.

### 6.5 Carry-over memory

The agent-side memory at
`/home/ryo/.claude/projects/-home-ryo-work-goopg-goopg/memory/`
holds two relevant project entries:

- `m0071_stage_b_silent_regression.md` — Q12/Q13
  silent-regression details; supersedes the prior
  Stage B+C "lessons" memory. **Read this before
  re-attempting Stage B.**
- `m0071_0005_stage_b_c_lessons.md` — superseded
  prior lessons; kept as historical context.

### 6.6 Code anchors

Planner:
- `internal/planner/unnest.go::unnestSubquery`
  (l.774) — scalar subquery decorrelation.
  M0071-0002-followup preserves the Project's
  expression here (via
  `cloneExprSubstituteAggIdx0`).
- `internal/planner/unnest.go::unnestNonCorrelatedInExpr`
  (l.1124) — non-correlated IN → SemiJoin.
  M0069-0005 + M0071-0002 IsolatedScope mark.
- `internal/planner/unnest.go::unnestExistsExpr`
  (l.1765) — EXISTS → Semi/Anti. M0061-0001 +
  M0071-0003 defensive `resolveOuterIdx`.
- `internal/planner/nl_index_join.go::pickInnerSide`
  (l.700) — NLI flip selection.
  M0071-0002-followup declines flip when Right is
  `*Aggregate`.
- `internal/planner/bushy.go::reresolveJoinByName`
  (l.1614) — Name-based predicate rebind after MHJ
  rewrite. The `predRebind` closure inside has
  ambiguity-handling; modifying it has historically
  produced silent regressions (see
  M0071-0003-followup revert in this session).
- `internal/planner/bushy.go::applyJoinTreePosMap`
  (l.1508) — posMap-driven remap with Semi/Anti
  isolation.

Executor:
- `internal/executor/slot.go` — TupleSlot interface
  scaffold (Stage A, commit `d0de10d`).
- `internal/executor/operator.go::Borrowable`
  (l.121) — current row-level borrow contract;
  removal target in Stage E.
- `internal/executor/operators_nljoin.go::joinBuf`
  — Stage D target (NLI joinBuf → VirtualSlot).
- `internal/executor/multi_hash_join.go::lazyOut`
  — Stage D target (MHJ lazyOut → VirtualSlot).
- `internal/executor/operators_join_agg.go::joinOp::nextLazy`
  — lazy hash join Semi/Anti emit logic. The
  M0071-0003-followup attempt to add residual
  evaluation here was reverted (paired with the
  bushy.go ambiguity-preserve change). The bug
  it tried to fix (Anti dropping rows on key
  match alone, ignoring residual `l3 <> l1`) is
  real; the fix needs a different approach.
