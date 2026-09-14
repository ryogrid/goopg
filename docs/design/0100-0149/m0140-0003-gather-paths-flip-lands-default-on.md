# M0140-0003 — land the `GOOPG_GATHER_PATHS` flip on the category metric

Status: accepted
Milestone: M0140 — TPC-DS parallelism
Type: root-cause fix (production) + default flip (production) + four test re-pins

## Goal

Land `GOOPG_GATHER_PATHS=all` as the default (was `off`), per the pre-registered
success criterion in `.ralph/fix_plan.md`: **category movement, not a match-count
rise** — "R43 rev 3 measured TPC-H parallelism 18->16 under the flip with no new
match, and K38 measured Gather 42->104 / Parallel Hash 0->167 at all with parity
not improving." M0140-0001 re-measured the failing-test set at HEAD; M0140-0002
adjudicated each failure against PG 18.3 and found six of the seven were either a
test-harness bug (fixed) or stale shape/timing pins, but flagged
`TestQ2DecorrelatedGroupKeyResolvesInAggregateInput` /
`TestSlice3CorrelatedBodyDeclinesParentAware` — TPC-H Q2's scalar-aggregate
decorrelation declining entirely under the flip — as "the strongest candidate for
an actual fix before M0140-0003" and asked this task to isolate the root cause
before landing default-on.

## What landed

### 1. Root-cause fix: `clonePlanReplacingOuter` had no `*Gather`/`*GatherMerge` arm

Instrumented `canUnnestSubquery` / `unnestSubquery` (`internal/optimizer/unnest.go`)
with throwaway `println` probes (removed before commit) to find exactly which of
`unnestSubquery`'s seven early-return points fires for TPC-H Q2 under the flip.
`canUnnestSubquery` passed every gate; the decline was `buildUnnestedSubquery`
returning the error `clonePlanReplacingOuter: unsupported plan node (byte 0)`.

`clonePlanReplacingOuter` walks the scalar subquery's own inner plan (Q2's
4-table `partsupp ⋈ supplier ⋈ nation ⋈ region` aggregate) substituting
`OuterColumnRef`s so the correlated subquery can be spliced in as a joined,
grouped aggregate. Its type switch — written before `GOOPG_GATHER_PATHS`
existed — had a case for every plan node kind **except** `*Gather` and
`*GatherMerge`. Under the flip, the subquery's own inner join can win a
partial path on one of its base rels and come back from `Plan()` wrapped in a
`Gather`; the cloner's `default:` arm then declines with the "unsupported
plan node" error, `unnestSubqueriesInPlan`'s driver loop swallows it (`if err
!= nil || newOuter == nil { break }`), and the sublink silently stays a
per-outer-row `SubPlan` instead of decorrelating.

This is the **same bug class** M0140-0002 already fixed once this milestone
(`q21_live_test.go`'s `visit()` helper) and that R11 fixed once in production
code (`createplanroot.go`'s `boundaryWalkChildren`) — a tree walker written
before the Gather-admission flag existed, with no case for the node it later
started producing. The two initial hypotheses this task tried and refuted
before finding the real one, in order:
1. `jsgDecorrelatedAgg` (the *test's own* walker in
   `joinsearchunnestgroupkey_test.go`) looked Gather-blind by inspection —
   added `unwrapGather`/two switch arms, re-ran, **still failed identically**.
   A throwaway plan-tree dump (`TestZZProbeQ2Gather`, deleted) showed the
   final plan tree has no `*Aggregate` node reachable through `Node`
   children at all in either arm's tree — the walker was reporting nil
   correctly; decorrelation genuinely had not happened. Reverted the
   unneeded (if harmless) test-file edit rather than keep code that didn't
   fix anything.
2. Suspected `canUnnestSubquery`'s `plan.(*Aggregate)` type assertions might
   see `*Gather` and bail. Instrumented every `return false` site: all
   passed under the flip (`canUnnestSubquery: ALLOWED` printed for both
   arms). Refuted.
3. Instrumented every `unnestSubquery` bail point in sequence
   (`eup nil`, `buildUnnestedSubquery` error, `subPlan nil`,
   `planHasOuterRefRemaining`, `findFilterContainingSubquery` nil,
   `!subqueryANDReachable`) — the second one, `buildUnnestedSubquery`'s
   error, fired and printed the exact message above. Confirmed.

**Fix**: added `case *Gather` / `case *GatherMerge` to
`clonePlanReplacingOuter`, each a straight single-child recursion (a Gather
carries no probe key of its own; a harvested correlation living on a scan
beneath it is still reached by the existing `*IndexScan`/`*BitmapHeapScan`
arms once the walk descends into `Child`). Confirmed both previously-failing
tests PASS under the flip, both were unaffected at the default (arm existed
before this task), and the full `go test ./internal/optimizer/...
./internal/executor/...` sweep stayed green in both arms after the fix
(module-wide, not just the two named tests).

### 2. The flip: `GOOPG_GATHER_PATHS` defaults to `all`

`internal/optimizer/gatherpaths.go`'s `gatherPathModeFromEnv`: an
unset/empty environment now resolves to `gatherPathsAll` (was
`gatherPathsOff`). An explicit unrecognised value (a typo, or the opt-out
spelled some other way) still resolves to `off` — the fail-closed behaviour
this function has always had, now protecting an opt-out instead of an
opt-in. `off`/`false` are the two literal ways to reproduce the
pre-M0140-0003 arm exactly, for any future A/B that needs the control.

Updated the file header comment (which used to say "flipping the default is
a measured decision... and this slice does not take it" — now says which
task took it and what it measured), the two `flaglabels.go` provenance-table
comments, and `docs/design/planner-c19d-gather-paths/DESIGN.md` §5 (a dated
addendum in front of the preserved pre-landing analysis, matching that
doc's own existing §5.1a CORRECTION convention rather than rewriting
history). Regenerated `scripts/planner-flags.env` via
`go run ./cmd/gen-planner-flag-labels` — `TestFlagProvenanceEnvIsGenerated`
is a golden-file test that fails on exactly this kind of unregenerated
default flip (M0127-P5.9-q's bar) and caught the omission on the first
sweep.

### 3. Four stale-pin re-baselines (all pre-adjudicated by M0140-0002 as safe)

| test | package | change |
|---|---|---|
| `TestSlice3LiveQ9ShapeDerivation` | optimizer | narrow-build count 7→6 (`{s_suppkey,n_name}` merges into an ancestor build under the Gather-admitted shape); `{p_partkey}`→`{p_partkey,p_name}` |
| `TestSlice3FilterColumnSurvivesNarrowing` | optimizer | asserts `p_name` now rides through exactly one build (the part leaf) instead of asserting no build keeps it — the filter-column-drop optimisation declines under a Gather-admitted ancestor, not a correctness break |
| `TestOwnedBuildPoisonPrebuiltBoundary` | executor | same mechanism, mirrored fix |
| `TestPartialPathIsNeverTheFinalPath` | optimizer | the safety property (`walk(final)`: no partial path, no nonzero-worker path, ever reaches the *chosen* tree) stays unconditional; the second loop (join rels have zero partial paths before a later pass) is now scoped to `gatherPathsMode == gatherPathsOff` only, because `addPartialHashJoinPath` (C-19f) is a separate, earlier producer the fixture's timing assumption never accounted for |

`TestSplitEqualityForHashMultiKey/searched_enumerator` needed no further
action — M0140-0002 already fixed its `visit()`-helper Gather-blindness.

Full sweep after all four re-pins, both arms, `-count=1` confirmed then
warm-cache confirmed:

```
go test -count=1 ./internal/optimizer/... ./internal/executor/...          # default (all): ok
GOOPG_GATHER_PATHS=off go test -count=1 ./internal/optimizer/... ./internal/executor/...  # off: ok (old-shape pins intentionally live only under this arm)
GOOPG_GATHER_PATHS=all go test -count=1 ./internal/optimizer/... ./internal/executor/...  # all (explicit): ok
```

## Measurement

### TPC-DS SF0.25 (`:65437` vs live PG `:65438`), same commit, same stats epoch

Stats epoch verified identical across arms
(`scripts/check-stats-epoch.sh`: `MATCH — 5d4dc56356f3d676`).

| | off (pre-M0140-0003) | all (landed default) |
|---|---|---|
| match | 2 | 2 |
| shapediff | 69 | 69 |
| missingnode / error / timeout / unparsed | 25 / 3 / 0 / 0 | 25 / 3 / 0 / 0 |
| join-order | 88 | 90 |
| join-method | 63 | 69 |
| scan-type | 56 | 61 |
| parameterisation | 48 | 45 |
| aggregation-strategy | 69 | 70 |
| sort-strategy | 76 | 76 |
| **parallelism** | 86 | 85 |
| qual-placement | 15 | 21 |
| rendering | 20 | 23 |

Non-regression floor held exactly: match stayed at the M0137-0004 canonical
`2` (Q9, Q41) on both arms — **no new match, and none lost**, precisely the
pre-registered outcome. The 3 `error` queries are the pre-existing Q36/Q70/Q86
`SKIP_QUERYGEN` cases (verified identical query set in both arms' `ERROR`
blocks) — not new errors from the flip.

**Shape-delta** (off vs all, same engine, isolates exactly what the flag
moved): `queries=99 match=46 shapediff=50 error=3(same 3) unparsed=0
missingnode=0 timeout=0` — 50 of 99 queries changed shape, and per-query
every one of the 50 carries a `parallelism` tag (`CATEGORIES: ... parallelism=50
...`), confirming the moved plans are attributable to the flag and not noise.
The values-gate companion to this shape movement:

```
scripts/tpcds-sf025-regression.sh sweep
=== SUMMARY: PASS=96 (60 ck-verified, 36 ck=n/a) MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0 SKIP=3 ===
```

(`SKIP=3` is the same querygen-excluded Q36/Q70/Q86; `ERROR=0` here is the
goopg-sweep-vs-oracle channel, not the parity-diff `ERROR` verdict above —
the two count different things and are not expected to agree.) A `--strict`
plan-shape companion run inside the same sweep independently reported
`same=49 changed=50` against the immediately-prior commit — consistent with,
though not identical in scope to, the clean off/all A/B above (that run
diffs two different commits, this task's clean A/B diffs one commit's two
arms).

### TPC-H (`:65433` vs live PG `:65432`), same commit, same stats epoch

Two protocols, deliberately kept separate:

**Canonical scoreboard protocol** (`estimate-audit -plan-only`, `-serial`
defaults `true`, `max_parallel_workers_per_gather=0` on both engines): the
flip is provably inert here, because `considerparallel.go`'s
`parallelModeOK` gate never admits a partial path when the worker cap is 0
regardless of `GOOPG_GATHER_PATHS` — confirmed live rather than assumed:

```
PLAN-PARITY: queries=22 match=6 shapediff=14 unparsed=0 missingnode=2 error=0 timeout=0
CATEGORIES: ... parallelism=0 ...
```

`match=6` is the AGENT.md floor (state at filing: TPC-H 6/22) and is
untouched, as it structurally must be — this is the protocol that measures
the category **out**, not the one that tests the flip.

**Non-serial diagnostic protocol** (`-serial=false`, matching the historical
R43/K38 measurement — the one that actually exercises the flag), stats
epoch `253df6b1d97f9f5b` on both arms:

| | off | all (landed default) |
|---|---|---|
| match | 2 | 2 |
| shapediff | 20 | 20 |
| join-order | 18 | 17 |
| join-method | 10 | 12 |
| scan-type | 11 | 9 |
| parameterisation | 5 | 4 |
| aggregation-strategy | 10 | 10 |
| sort-strategy | 14 | 13 |
| **parallelism** | 17 | 16 |
| qual-placement | 4 | 3 |
| rendering | 2 | 1 |

`parallelism 17→16, no new match` independently reproduces R43 rev 3's
historical `18→16` finding (off by one from the ~87-rounds-old figure, which
is exactly the drift M0140-0001 already flagged as expected, not a
discrepancy to chase) at HEAD, with a live PG reference, on this task's own
run. `match=2` here is this diagnostic protocol's own number, not a
regression against the `6/22` floor — the two protocols are not
comparable to each other, only each to its own prior run, which is why
this doc reports the canonical serial protocol's `match=6` (unmoved)
separately above and never lets the two collide into one row of a table.

### Values gates

- **TPC-H**: `scripts/tpch-spotcheck.sh` PASS, canonical `Q12=2/Q13=34`, under
  the new default (`GOOPG_GATHER_PATHS` unset → `all`).
- **TPC-DS SF0.25**: `scripts/tpcds-sf025-regression.sh sweep` — see above,
  `MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0`.
- **Unit/component suite**: `go test ./internal/optimizer/...
  ./internal/executor/...` green in all three arms (default/`off`/`all`).
  `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh`'s only two
  failures (`internal/parser`, `bak` build failure) are **pre-existing and
  unrelated**: `internal/parser`'s ~60 `AST drift`/golden-fixture failures
  are already filed as `AI-20260914-235643-001` (nightly, 2026-09-14, before
  this task started) and fix_plan.md's M-NIGHTLY section already carries it
  unchecked; `bak/` is untracked scratch debris from unrelated concurrent
  work, not a Go module this task touches. Neither package was modified by
  this task (`git status --short` confirms zero changes under
  `internal/parser/` or `bak/`).

## Operational note: measuring the shared TPC-H bench cluster safely

`:65433` is a persistent shared cluster (CLAUDE.md); `scripts/tpch-private-clone.sh`'s
snapshot mechanism requires the port to stop answering entirely before it will
copy the data directory, which a long-idle-but-still-listening server never
satisfies. Before touching it, confirmed via `pg_stat_activity` (only this
task's own probe connection, no other backends) and WAL/log mtimes (last
write over 24h old) that the cluster was genuinely quiescent, then used the
sanctioned lifecycle scripts (`bench/tpch/stop_goopg.sh` /
`bench/tpch/setup_goopg.sh`, never `pkill`) to bring it down for each
measurement window and back up immediately after — ending in the correct
final state (default, unset `GOOPG_GATHER_PATHS`). Total downtime windows:
three, each under a minute, all with zero observed connections beforehand.

## Files changed

- `internal/optimizer/unnest.go` — `clonePlanReplacingOuter` `*Gather`/`*GatherMerge` arms (the fix).
- `internal/optimizer/gatherpaths.go` — default flip + comments.
- `internal/optimizer/flaglabels.go` — two provenance-table comments.
- `internal/optimizer/pathtarget_test.go` — `TestSlice3LiveQ9ShapeDerivation`, `TestSlice3FilterColumnSurvivesNarrowing` re-pins.
- `internal/optimizer/considerparallel_test.go` — `TestPartialPathIsNeverTheFinalPath` mode-scoped re-pin.
- `internal/executor/owned_build_poison_test.go` — `TestOwnedBuildPoisonPrebuiltBoundary` re-pin.
- `scripts/planner-flags.env` — regenerated.
- `docs/design/planner-c19d-gather-paths/DESIGN.md` — landed-status addendum to §5.
- `docs/design/README.md` — this doc indexed.
- `.ralph/fix_plan.md` — M0140-0003 checked off.

## No ledger row

Every mechanism this task touched is a goopg-internal planner/optimizer
interaction (partial-path admission vs. a subquery-cloning walker, vs. test
timing assumptions), not a newly discovered PG-incompatibility — the same
basis M0140-0001/-0002 gave for filing no ledger row. The one pre-existing
issue this task's gates surfaced (`internal/parser`'s golden-fixture drift)
was already filed as an M-NIGHTLY nightly-regression item before this task
began and is out of this milestone group's scope per the M-NIGHTLY carve-out
rule (it neither breaks the build nor breaks a gate this milestone group
depends on).

## Next

M0140-0004 (partial-Append producer, K43) and M0140-0005 (file the two
out-of-reach items as ledger rows) remain open. M0140-0002's Q2-decorrelation
concern that this task existed to resolve is now closed by the root-cause
fix in §1 — it will show up as *decorrelated* Q2 plans in any future TPC-H
capture rather than as a declined optimisation, which is a genuine
correctness/performance improvement carried by this task, not just a
test-pin exercise.
