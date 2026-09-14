# M0140-0001 — re-measure the `GOOPG_GATHER_PATHS` flip's failing test set at HEAD

Status: accepted
Milestone: M0140 — TPC-DS parallelism
Type: recon (measurement only, no production change)

## Goal

`METHODOLOGY3/02-open-problems.md` §B3 and `04-forward-plan.md` §Phase 1
Campaign A step 1 both name a "5 failing tests, 4 needing adjudication" list
from `TODO.md:2318-2326` (R43-era, ~87 rounds stale) as the prerequisite the
`GOOPG_GATHER_PATHS` flip needs cleared before it can be landed default-on.
Both documents explicitly forbid inheriting that list without re-measurement:
*"re-measure before relying on any prior round's figures"* — because at least
one member (`TestSlice3LiveQ9ShapeDerivation`) is already known to have been
re-baselined once, by R51, nine rounds after R43.

This task re-runs the named tests against HEAD with the flip on and records
which still fail. No production code is touched — a code diff in this task's
commit would be a scope violation per `AGENT.md`'s recon-task rule.

## Named set

The R43-era list carries four distinct test names (`TODO.md`'s "5 failing"
count includes a subtest):

| test | package |
|---|---|
| `TestSplitEqualityForHashMultiKey/searched_enumerator` | `internal/optimizer` |
| `TestSlice3LiveQ9ShapeDerivation` | `internal/optimizer` |
| `TestSlice3FilterColumnSurvivesNarrowing` | `internal/optimizer` |
| `TestOwnedBuildPoisonPrebuiltBoundary` | `internal/executor` |

## Method

None of the four tests set `GOOPG_GATHER_PATHS` internally (`grep -n
"GOOPG_GATHER_PATHS\|Setenv"` over all three test files returns nothing), so
the flip is applied purely via the process environment at `go test` invocation
time — exactly how a default-on flip would change their behaviour with no
other code change.

```
# baseline (current default: GOOPG_GATHER_PATHS unset == off)
go test ./internal/optimizer/... -run 'TestSplitEqualityForHashMultiKey|TestSlice3LiveQ9ShapeDerivation|TestSlice3FilterColumnSurvivesNarrowing' -v
go test ./internal/executor/...  -run 'TestOwnedBuildPoisonPrebuiltBoundary' -v

# under the flip
GOOPG_GATHER_PATHS=all go test ./internal/optimizer/... -run 'TestSplitEqualityForHashMultiKey|TestSlice3LiveQ9ShapeDerivation|TestSlice3FilterColumnSurvivesNarrowing' -v
GOOPG_GATHER_PATHS=all go test ./internal/executor/...  -run 'TestOwnedBuildPoisonPrebuiltBoundary' -v
```

## Result (measured 2026-09-15, HEAD = `d2dbb7863`)

| test | baseline (flip off) | under `GOOPG_GATHER_PATHS=all` |
|---|---|---|
| `TestSplitEqualityForHashMultiKey/searched_enumerator` | PASS | **FAIL** — falls back to Nested Loop instead of hash-joining any of the AND'd equalities |
| `TestSlice3LiveQ9ShapeDerivation` | PASS | **FAIL** — none of the 5 expected narrow-build column sets match; the actual builds carry different (wider) column sets under parallel paths |
| `TestSlice3FilterColumnSurvivesNarrowing` | PASS | **FAIL** — the part build keeps `p_name` instead of narrowing to `[p_partkey]` only |
| `TestOwnedBuildPoisonPrebuiltBoundary` | PASS | **FAIL** — same symptom as the sibling above: `p_name` survives the narrow build over the prebuilt leaf |

All four pass at HEAD under the current default (`GOOPG_GATHER_PATHS` unset).
All four fail under the flip. **The R43-era list is stale in dating but not
in content: none of the four regressions it named have been independently
fixed by the intervening ~87 rounds of unrelated work.** This refutes the
possibility (raised only as a risk, not a claim, by 04's phrasing) that the
list might already be half-clear; it is not — the prerequisite for landing
the flip is exactly as large today as R43 measured it, four real failures,
zero already resolved.

This is consistent with — and narrows — the one already-known correction to
the list: `01-what-we-learned.md` records that `TestSlice3LiveQ9ShapeDerivation`
was *re-baselined* by R51 (its expected narrow-build sets were rewritten to
match a join-order change unrelated to parallelism). That re-baseline changed
what the test's PASS state means, not whether it still fails under the flip —
it does, against the *current*, post-R51 pin.

## Why all four fail the same way

Inspecting the failure text: the optimizer/executor pair of tests
(`TestSlice3LiveQ9ShapeDerivation`, `TestSlice3FilterColumnSurvivesNarrowing`,
`TestOwnedBuildPoisonPrebuiltBoundary`) all assert exact narrow-build column
sets or exact narrow-vs-keep column membership on TPC-H Q9's join tree. Under
`GOOPG_GATHER_PATHS=all` the search additionally emits partial (parallel-
capable) paths at points these tests do not expect, which changes which path
wins at each join level and therefore which columns the eventual narrow-build
witness carries — the assertions are about a specific serial plan shape, and
the flip changes the shape being asserted against. `TestSplitEqualityForHash
MultiKey/searched_enumerator` fails for a related but distinct reason: under
the flip the multi-key equi-join no longer chooses Hash Join at all for that
shape (falls back to Nested Loop), which the M-NIGHTLY regression the test
guards (`tpch/Q20-timeout`) exists specifically to prevent.

None of these are adjudicated against PG in this task — per the harness's
recon-task discipline, adjudication (M0140-0002: "ask the oracle, the test
can be wrong") is deliberately a separate, later task so this one stays a
clean measurement with no production diff.

## Disposition

- No code changed this task.
- `M0140-0001` is complete: the failing set is re-measured at HEAD and is the
  same four tests R43 named, still failing, for the reasons described above.
- Hands off to `M0140-0002` (adjudicate each of the four failures against PG
  18.3) with a head start: the two Q9-narrowing tests and the poison-boundary
  test look like the same underlying mechanism (parallel-path availability
  changing which join-tree shape wins the search, before any narrowing logic
  runs), so M0140-0002 may be able to adjudicate them as one finding rather
  than three independent ones. The multi-key hash-join fallback is a
  distinct, unrelated mechanism and should be adjudicated separately.

## Ledger

No new deferral-ledger row: this task's own list format already carries the
resume point (M0140-0002) and there is no PG behavior newly discovered to be
unimplemented — the divergence is a goopg planner-search interaction between
two of its own features (parallel-path admission and the narrowing pass), not
a PG-compat gap.
