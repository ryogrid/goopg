# M0140-0002 — adjudicate the `GOOPG_GATHER_PATHS` flip's failing tests against PG 18.3

Status: accepted
Milestone: M0140 — TPC-DS parallelism
Type: adjudication + one test-harness fix (production planner code untouched)

## Goal

M0140-0001 re-measured the R43-era four-test failing set at HEAD and confirmed
all four still fail under `GOOPG_GATHER_PATHS=all`. This task adjudicates each
failure per R14's precedent — *"ask the oracle, the test can be wrong"* — and
does **not** change planner behaviour to satisfy a pin the oracle contradicts.

## Scope correction to M0140-0001 (found this loop)

M0140-0001 ran only the four **named** R43-era tests. A full package sweep
under the flip (`GOOPG_GATHER_PATHS=all go test -count=1 ./internal/optimizer/...
./internal/executor/...`) finds a **larger** true failing set: five in
`internal/optimizer`, one in `internal/executor`. Three names were not in the
R43-era list at all:

| test | package | in R43/M0140-0001 list? |
|---|---|---|
| `TestSplitEqualityForHashMultiKey/searched_enumerator` | optimizer | yes — **fixed this loop, see below** |
| `TestSlice3LiveQ9ShapeDerivation` | optimizer | yes |
| `TestSlice3FilterColumnSurvivesNarrowing` | optimizer | yes |
| `TestOwnedBuildPoisonPrebuiltBoundary` | executor | yes |
| `TestPartialPathIsNeverTheFinalPath` | optimizer | **no — newly found** |
| `TestQ2DecorrelatedGroupKeyResolvesInAggregateInput` | optimizer | **no — newly found** |
| `TestSlice3CorrelatedBodyDeclinesParentAware` | optimizer | **no — newly found** |

Lesson for the milestone: named-test re-measurement is not equivalent to a
failing-set measurement. `M0140-0003` (landing the flip) must gate on a full
`go test ./internal/optimizer/... ./internal/executor/...` sweep under the
flip, not a named list, or it will under-count again the way R43→M0140-0001
did. Scope of this sweep: the two packages that hosted every known-affected
test; `GOOPG_GATHER_PATHS` is read only from
`internal/optimizer/gatherpaths.go`, so a repo-wide `go test ./...` sweep was
judged disproportionate to run every time this flag moves.

**Cache hazard hit while measuring (record for future rounds):** an early
`GOOPG_GATHER_PATHS=all go test -run TestOwnedBuildPoisonPrebuiltBoundary
./internal/executor/` (no `-count=1`) returned a **stale PASS from the
test-result cache** — Go's cache did not invalidate on the env-var flip for
this particular binary/run, even though the same command reliably
invalidates for other tests in this same investigation. `-count=1` is
reserved for probes per `AGENT.md`'s gate policy, and this is exactly the
kind of probe that policy exists for: every number in this document's
adjudication table was confirmed with `-count=1` after first observing it
without, specifically because of this one false cache hit.

## Adjudication, per test

### 1. `TestSplitEqualityForHashMultiKey/searched_enumerator` — FIXED, was a test-harness bug, not a planner regression

**Verdict: the test was wrong. Fixed.**

Instrumented with a throwaway probe (`zz_probe_multikey_test.go`, deleted
after use) that dumped the full plan tree by type, bypassing the test's own
truncated walker. Under the flip the chosen plan is:

```
Project
  Gather
    Join type=Inner algo=JoinAlgoHash left=SeqScan(partsupp) right=Project(...)
      leftKey=ps_partkey rightKey=l_partkey
```

The join **is** a proper hash join with both `LeftKey`/`RightKey` set —
exactly what the test wants. The failure was the shared `visit()` helper
(`q21_live_test.go:198`), which predates `Gather` and has no `case *Gather`:
once the flip wraps the searched tree in a `Gather` (which only happens under
the flip — the unflipped default never emits one), `visit()` silently stops
descending and every caller — including this test — sees an empty subtree and
reports a false negative ("fell back to Nested Loop", which never happened).

This exact class of bug was already fixed once in **production** code for the
identical reason: `createplanroot.go:451-459`'s `boundaryWalkChildren` added
`case *Gather`/`*GatherMerge` under R11 with the comment *"without these the
walk ENDS at the Gather and the boundary below it is never reached."* The
narrowing-pass tests that use `boundaryWalkChildren` (the two `Slice3` tests
below) and the executor's own `obpWalk` (`owned_build_poison_test.go:83,711`)
were already Gather-aware for the same reason — `visit()` was the one walker
that had not been updated.

**Fix landed this loop:** `internal/optimizer/q21_live_test.go` — added
`case *Gather` and `case *GatherMerge` to `visit()`, both single-child
pass-throughs mirroring `boundaryWalkChildren`. Confirmed:
- `GOOPG_GATHER_PATHS=all go test -count=1 -run TestSplitEqualityForHashMultiKey
  -v ./internal/optimizer/` → PASS (both subtests).
- Unflipped default unaffected (`go test -count=1 -run
  TestSplitEqualityForHashMultiKey -v ./internal/optimizer/` → PASS, as
  before).
- Full `go test ./internal/optimizer/...` (default) → all pass, no other
  test depends on `visit()` stopping at `Gather` (`rtable_identity_cut2_test.go`
  already documents that other tests needing full-tree traversal bring their
  own walker for exactly this reason, rather than relying on `visit()`).

This is real progress toward M0140-0003's prerequisite: one of the four
R43-era names is now a true PASS under the flip, not a false FAIL.

### 2–4. Narrow-build shape tests — real shape/optimization movement, not adjudicable against PG, not a correctness bug

`TestSlice3LiveQ9ShapeDerivation`, `TestSlice3FilterColumnSurvivesNarrowing`
(optimizer) and `TestOwnedBuildPoisonPrebuiltBoundary` (executor) all pin an
exact narrow-build column-set/count for the same TPC-H Q9-shaped synthetic
query. All three use Gather-aware walkers already (`boundaryWalkChildren` /
`obpWalk`), so — unlike test 1 — their failures are **not** a walker-blindness
artifact; the underlying plan shape genuinely changed.

**Tried to adjudicate against real PG 18.3** (per R14's precedent): ran
`EXPLAIN` for the actual TPC-H Q9 query
(`select nation, o_year, sum(amount) ... from (select ... part, supplier,
lineitem, partsupp, orders, nation where ...) profit group by nation, o_year
order by ...`) against the live PG oracle (`bench/tpch`, SF=1, port 65432):

```
Sort
  Finalize HashAggregate
    Gather (Workers Planned: 4)
      Partial HashAggregate
        Parallel Hash Join (orders.o_orderkey = lineitem.l_orderkey)
          Parallel Seq Scan orders
          Parallel Hash
            Nested Loop  -- join filter: supplier.s_suppkey = lineitem.l_suppkey
              Hash Join (supplier.s_nationkey = nation.n_nationkey)
                Hash Join (partsupp.ps_suppkey = supplier.s_suppkey)
                  Parallel Hash Join (partsupp.ps_partkey = part.p_partkey)
                    Parallel Seq Scan partsupp
                    Parallel Hash -> Parallel Seq Scan part (p_name LIKE filter)
                  Hash -> Seq Scan supplier
                Hash -> Seq Scan nation
              Index Scan lineitem USING lineitem_part_supp_fkidx
                Index Cond: l_partkey = ps.ps_partkey AND l_suppkey = ps.ps_suppkey
```

This matches **neither** goopg shape (default or flipped): PG joins orders
**last**, via a *Parallel* Hash Join under a `Gather`; it joins lineitem via a
**Nested Loop + Index Scan**, never a hash build; and its join order for
part/partsupp/supplier/nation is staged (four separate joins), not the
5-build/4-build witness structure either goopg arm produces. TPC-H's parity
scoreboard already excludes Q9 from the matched set (6/22, not including Q9)
under **both** configurations (`METHODOLOGY3/README.md`), consistent with
this: Q9's join-order divergence from PG predates this flip and is a
separately tracked, larger blocker — the join-order-costing gap (`K26`,
`R51`–`R53`, `R68`, `r130-q9-joinorder-remeasure`), gated to **M0142**, not
something `GOOPG_GATHER_PATHS` resolves either way. "Closer to PG" cannot
adjudicate between the two goopg shapes here because neither is close.

**What the failures actually show, mechanically** (verified via each test's
own diagnostic output, `-count=1`):
- `TestSlice3LiveQ9ShapeDerivation`: build count drops 5→4 (two previously
  separate witness builds — `{s_suppkey,n_name}` and
  `{ps_partkey,ps_suppkey,ps_supplycost}` plus the nation piece — merge into
  one `{ps_partkey,ps_suppkey,ps_supplycost,s_suppkey,s_nationkey,
  n_nationkey,n_name}` build). No `wantSet`'s columns are missing from the
  union of what the flipped plan actually builds; every column the old shape
  needed is still present somewhere in the new one. This is join-tree
  re-shaping under partial-path competition, not data loss.
- `TestSlice3FilterColumnSurvivesNarrowing` / `TestOwnedBuildPoisonPrebuiltBoundary`
  (same underlying mechanism, two packages, same Q9-inner shape): the part
  leaf's narrow build now keeps `p_name` (`[p_partkey p_name]`, was
  `[p_partkey]`). The LIKE filter still runs correctly below the (now wider)
  build — verified via `obpScanBelowCarries`/`slice3FiltersMentioning`, both
  still passing inside the failing test body. This is the prebuilt-boundary
  filter-column-drop optimization **declining to fire** once its leaf sits
  beneath a Gather-admitted ancestor — a missed optimization (one extra
  column carried through one hash build), **not** a correctness bug: nothing
  reads a dropped column that should have survived, and nothing drops a
  column that a reader above still needs.

**Verdict: not a regression requiring a planner fix under this task's
adjudication rule** (no correctness break, and the join-order-costing
question these shapes actually turn on is out of scope for M0140, gated to
M0142). **These are stale test pins** — R14's precedent applies in the
direction of "the test can be wrong": they assumed the unflipped shape as
ground truth without ever having been PG-adjudicated themselves. **Action for
M0140-0003**: when the flip lands default-on, re-pin these three to the
post-flip shape (the actual builds observed above), not before — re-pinning
now, ahead of the flip actually landing, would let a reverted or delayed flip
silently go unnoticed.

### 5. `TestPartialPathIsNeverTheFinalPath` — safety property holds; only a timing/staging assertion is stale

**Verdict: the critical invariant this test exists to guard is intact.**

The test asserts two things: (a) no partial path ever reaches the *chosen
final* plan tree (the safety property — a partial path selected as final
without a `Gather` wrapper would be an executor-crashing bug), and (b) join
rels carry **zero** partial paths before a specific later pass, `C-19d`. Under
the flip, **(a) still passes** — `walk(final)` completes without finding any
partial path or nonzero-worker path in the chosen tree. Only **(b)** fails,
with `join rel 0x3 has partial paths before C-19d`.

Cause: `GOOPG_GATHER_PATHS` also unlocks `addPartialHashJoinPath` (the
producer K80 already identified as "dead solely because `GOOPG_GATHER_PATHS`
is default-OFF" — `docs/milestones/0140-tpcds-parallelism.md`), which is not
gated behind pass `C-19d`/`C-19e` the way this fixture assumed was the only
partial-path source. The flip makes a second, earlier producer active; the
test's ordering assumption ("nothing produces partial paths before C-19d")
was true only because the flag kept that second producer off.

**Verdict: stale timing pin, not a safety regression.** Action for
M0140-0003: relax or re-scope the pre-C-19d assertion to account for the
hash-join partial-path producer, once the flip is landing for real.

### 6–7. `TestQ2DecorrelatedGroupKeyResolvesInAggregateInput` / `TestSlice3CorrelatedBodyDeclinesParentAware` — one real missed-optimization regression, most consequential of the six

**Verdict: real, and the strongest candidate for an actual fix before
M0140-0003, though still not a correctness bug.**

Both tests independently hit the same mechanism on TPC-H Q2's correlated
scalar-aggregate subquery: under `pgShapedDP=true` (the searched enumerator),
`jsgDecorrelatedAgg(plan)` returns `nil` — the decorrelation that turns the
correlated subquery into a joined, grouped aggregate **does not fire at all**
under the flip; the plan falls back to the `SubPlan` form (re-evaluated once
per outer row). `TestQ2DecorrelatedGroupKeyResolvesInAggregateInput`'s own
comment marks this exact case as intentionally non-fatal when the flag is off
("Flag OFF keeps the SubPlan on this shape. Nothing to check, and nothing
wrong") but `t.Fatalf`s when the flag is on and decorrelation still declines
— i.e. the test's authors already anticipated and explicitly rejected this
outcome for the `pgshaped=true` arm.

Unlike tests 2–5, this is **not** merely a wider build or a stale ordering
assumption: it is a full decline of an established optimization, and a
`SubPlan`-per-outer-row fallback is the same shape class the
`TestSplitEqualityForHashMultiKey` test's own doc comment calls "catastrophic
at any real scale" (M-NIGHTLY tpch/Q20-timeout precedent) when it happens to
a table-sized relation. Root cause not yet isolated in this task (out of
scope — this is adjudication, not a fix, and the "no planner-behaviour change
to satisfy a pin" rule cuts the other way here: this pin is not being
"satisfied", it is flagging a real optimization loss). Still not provably a
*correctness* bug (`SubPlan` results are still right, only slower), so it is
recorded here rather than blocking this task.

**Action for M0140-0003 (or an earlier M0140 slice if the cost is judged too
high to defer): before landing the flip default-on, isolate why the
decorrelation check declines once a Gather can appear in the candidate set**
— the leading hypothesis, by analogy with tests 1 and 2–4 above, is another
instance of code that walks a path/plan tree and was never updated to expect
a `Gather` in the middle of it, but this was not verified by instrumentation
in this task and must not be assumed without checking.

## Summary table

| test | verdict | action |
|---|---|---|
| `TestSplitEqualityForHashMultiKey/searched_enumerator` | test-harness bug | **fixed this loop** |
| `TestSlice3LiveQ9ShapeDerivation` | stale shape pin; PG matches neither arm | re-pin at M0140-0003 |
| `TestSlice3FilterColumnSurvivesNarrowing` | stale shape pin; missed optim., no correctness break | re-pin at M0140-0003 |
| `TestOwnedBuildPoisonPrebuiltBoundary` | same mechanism as above, executor pkg | re-pin at M0140-0003 |
| `TestPartialPathIsNeverTheFinalPath` | safety property holds; stale timing pin | re-pin at M0140-0003 |
| `TestQ2DecorrelatedGroupKeyResolvesInAggregateInput` | **real missed-optimization regression** | isolate root cause before/at M0140-0003 |
| `TestSlice3CorrelatedBodyDeclinesParentAware` | same mechanism as above | isolate root cause before/at M0140-0003 |

Net: of the seven test failures now known under the flip (four R43-era names
plus three newly found by this loop's full-package sweep), **one is fixed**,
**four are stale pins to update when the flip lands** (no correctness impact),
and **two share one real optimization regression** that should be isolated
before the flip lands default-on, though it has not been shown to produce
wrong results.

## No ledger row filed

As with M0140-0001: every divergence found here is an interaction between
goopg-internal planner/optimizer mechanisms (partial-path admission vs.
narrowing, vs. decorrelation, vs. a test walker), not a newly discovered
PG-incompatibility. The Q9 join-order gap that made "ask the oracle" a dead
end for tests 2–4 is already tracked (K26/R51-53/R68/M0142); nothing here is
new to that ledger.
