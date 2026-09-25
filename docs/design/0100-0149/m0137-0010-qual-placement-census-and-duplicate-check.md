# M0137-0010 — qual-placement census + duplicate-sensitive values check

Status: accepted
Milestone: M0137 — Parity measurement harness and instrument repair
Harness: `AGENT.md` §"Plan-parity harness — applies ONLY to M0137–M0143"
Source: `docs/design/not_ralph/plan_parity_fix_take2/METHODOLOGY3/04-forward-plan.md`
§2.5 (M5), open problem C1/C2 in `METHODOLOGY3/02-open-problems.md`

## Why

Two bugs shipped past every existing correctness channel because neither one
looks at qual *placement*, only at final row counts or PG-vs-goopg plan
*shape*:

- R56's Q78 lost three `Filter:` lines (a `pushConjunctTraced` regression)
  while every values-green sweep (row-count + checksum digests) kept
  passing — the query still returned the right rows on that corpus's data,
  it just filtered later in the tree than it should have. C2 in
  `02-open-problems.md` names this class as still open.
- R83's Limit-below-Unique truncation bug was masked on the corpus's actual
  data only because the duplicate count stayed under the LIMIT (`78 < 100`).
  The fix (`internal/optimizer/tuplefraction.go`'s `limitBoundMovable`
  guard, `internal/optimizer/planner.go`'s `planSelect` LIMIT stage) only
  ever covered the `LIMIT 100`-style literal shape (`*optimizer.IntegerConst`).
  C1 in `02-open-problems.md` records that the guard was "deliberately not
  extended" to `LIMIT $1` (`*optimizer.ParamRef`), so *"the values bug
  persists for that case"* — "fail-closed and zero-corpus-impact today —
  which is exactly why it will stay invisible."

`pg-plan-parity-diff.py` (M0137's structural-diff sibling) WOULD flag a
qual-placement move, but only across a (goopg, PG) pair — it has no mode for
diffing two same-engine captures, which is exactly the shape every M0139
slice produces (goopg pre-change vs goopg post-change, same corpus, same
stats epoch). This task closes both gaps named by the fix_plan line: a
cheap literal-line census usable on that same-engine shape, and closing the
one still-open half of C1 the docs call out by name.

## What landed

### 1. `scripts/qual-placement-census.py` (+ `-test.py`)

A new, deliberately tiny sibling to `pg-plan-parity-diff.py`: given two
`=== KEY`-section capture files (the same convention every M0137 tool uses),
it counts `Filter:` and `Index Cond:` property lines per query and diffs the
counts between the two arms. It does **no** tree parsing, no qual-signature
normalisation, no PG oracle — that is the point: it is cheap enough to run
on every M0139 slice as a fast gate, where `pg-plan-parity-diff.py`'s full
structural diff is a heavier, PG-oracle-requiring instrument reserved for
parity measurement proper.

Unlike `pg-plan-parity-diff.py` (report-only, always exits 0 — the pinned
budget lives in that tool's own test), this tool is designed to be the gate
itself, per K91 (AGENT.md §"Plan-parity harness": *"a harness must fail
loudly"*):

- exit 0 — every query key present in both arms has identical `Filter:`/
  `Index Cond:` counts
- exit 1 — a query's counts differ between arms, or a query key exists in
  only one arm (this is the R56 Q78 regression shape)
- exit 2 — an input file could not be read

A changed count is never silently assumed to be a regression — a
genuinely different plan shape can legitimately gain or lose a qual line —
but it is always something a human must look at before the arm pair is
accepted, which is what the nonzero exit forces.

`scripts/qual-placement-census-test.py` pins this contract: identical arms
(exit 0), the R56 Q78 shape reproduced synthetically (three `Filter:` lines
vanish → exit 1, message names `filter 3->0`), an `Index Cond:`↔`Filter:`
shape swap (exit 1), a query present in only one arm (exit 1, both
directions), and — the K50 complement — a pure `cost=`/`rows=`/`width=`
change with the qual lines held constant does **not** trip the gate (exit
0): the tool counts lines, not their contents or the node's estimate
fields, so it stays blind to exactly the class `pg-plan-parity-diff.py`'s
N1 normalisation already declines to judge.

Live-validated (not just the hermetic self-test) against a real arm pair
already on disk from prior optimizer work —
`analysis/planner-refactor-take3/c06-flip-remeasure-20260907/c06-{off,on}.plans.txt`,
a genuine flag-flip A/B over the full TPC-H corpus — which reports
`queries=22 ok=22 mismatch=0`: that flag class did not move qual placement,
and the tool parses real capture files (not just synthetic fixtures)
without a false positive.

### 2. C1 closed: `LIMIT $1` now moves above `DISTINCT` exactly like `LIMIT 100`

Added `TestDistinctLimitAppliesAboveDistinct_ParamRef`
(`internal/executor/distinct_limit_paramref_test.go`), the duplicate-sensitive
synthetic case `04-forward-plan.md` §2.5 item 2 asked for: the same R83 probe
shape (150×`'a'` + 50×`'b'`, `SELECT DISTINCT v ... ORDER BY v LIMIT $1`
bound to 100) but through a `ParamRef` instead of a literal. Confirmed it
fails at HEAD exactly as C1 predicted — the plan root came back
`*optimizer.Distinct` (Limit planned *below* Unique), and once forced open
via `Run` it returned `[]` instead of `[a b]` (100 pre-distinct physical rows,
all `'a'`, truncated before dedup).

Root cause matches `02-open-problems.md` C1 precisely:
`limitBoundMovable` (`internal/optimizer/tuplefraction.go:101-112`) type-
asserted only `*IntegerConst`; `planSelect`'s LIMIT stage
(`internal/optimizer/planner.go:2067-2080`) gates the R83 "defer LIMIT past
DISTINCT" rewrite on it, so any other expression shape — including
`*ParamRef` — fell through to the `else` branch that wraps `Limit` directly
under the not-yet-built `Distinct`, reproducing the pre-R83 bug.

**Why this was safe to actually fix, not just document as still-open:** a
`ParamRef` is bound once per statement execution via the extended protocol
and is exactly as position-independent as an `IntegerConst` for the
question this guard answers — it never varies per row or by outer scope.
The guard's own doc comment says "position-independent integer constants
move… anything row- or scope-dependent… keeps today's order" — a `ParamRef`
is not row- or scope-dependent, so the IntegerConst-only allowlist was
never a correctness requirement, only an unextended one (its own comment:
*"every corpus LIMIT is a plain integer literal"*). Fix: `limitBoundMovable`
now also accepts `*ParamRef` (two `Expr` type assertions, matching the
original code's style — **not** a `switch e.(type)`, deliberately, because
`TestExprSwitchInventoryIsPinned` (`internal/optimizer/exprwalk_inventory_test.go`)
censuses every hand-written `Expr` type switch in the package and a bare
2-arm switch here would register as a new unpinned site for no reason; two
assertions carries the identical semantics without touching that census).

This is a real engine correctness fix, not an instrument change — but it is
exactly the shape 04's M5 item 2 asked this task to produce (*"add a
synthetic case per known-fragile shape (the ParamRef LIMIT + DISTINCT case…
is still live)"*), and the fix was small, well-understood, and directly
falls out of writing the failing case, so it is landed in the same commit
rather than left failing or skipped.

## Verification

- `python3 scripts/qual-placement-census.py --self-test` — 5/5 passed.
- `python3 scripts/qual-placement-census-test.py -v` — 8/8 passed.
- Live run against a real arm pair (see above) — `mismatch=0`, as expected.
- `go build ./...` clean.
- `go test ./internal/executor/ -run TestDistinctLimitAppliesAboveDistinct` —
  both the pre-existing IntegerConst pin and the new ParamRef case PASS.
- `go test ./internal/optimizer/... ./internal/executor/...` — full package
  suites green, including `TestExprSwitchInventoryIsPinned` (confirms the
  two-assertion form did not create a new census site).

## Deliberately out of scope

- **C2** (the `*Gather`-crossing exclusion in `pushConjunctTraced`, the
  general form of the R56 Q78 defect) is not fixed here — it already has a
  ledger row and a named resume point from M0137-0012
  (`m0137-0012-o15-gather-crossing-excluded-pushconjuncttraced`), which this
  task's census now unblocks (that row explicitly "sequences the fix after
  M0137-0010's census"). No new row needed.
- The census is not wired into any Makefile target or CI stage — matches
  every sibling instrument in this milestone (M0137-0001/-0002/-0006), all
  run manually; M0139 slices are expected to invoke it directly per the
  fix_plan gating note ("gated on M0137-0010").
- No corpus-wide qual-placement re-measurement was run — this task lands the
  instrument and the one named-fragile-shape fix; using the census to sweep
  the existing corpus for other undetected qual-placement drift is a
  separate exercise for whichever M0139/M0141 slice needs it.

No `.ralph/deferral_ledger.md` row: both deliverables landed in full (the
census tool, and C1's fix); nothing here is a discovered-but-unfixed PG
incompatibility.
