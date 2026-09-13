# R98 SCOPE — Q96 inner-unique final-cost input attribution

R98 follows R97 STEP-1 (`254ab5cc7`). That measurement falsified the
bucket-walk skip hypothesis and showed that Q96's relevant Hash Join calls use
the R90/R91 `innerUnique` branch. The remaining order difference is not an
authorization to tune its cost. This scope first identifies whether Goopg's
two inputs to PG's final-cost branch are faithfully represented.

## Evidence and question

For an inner-unique Hash Join PG18 calls `compute_semi_anti_join_factors`
before `final_cost_hashjoin`. Its final cost uses
`rint(outer_path_rows * semifactors.outer_match_frac)` and
`2/(semifactors.match_count + 1)` for the matched bucket walk, followed by the
unmatched virtual-bucket walk. Goopg R90 supplies only a proof of bare unique
inner and the shortcut
`outerMatchFrac = clamp(joinrel.Rows / outer.Rows)` with an implicit
`match_count = 1`; R91 independently reconstructs
`numbuckets * numbatches` from a path's rows, emitted width, and work memory.

On Q96, the two first-level inner-unique alternatives use the same
store_sales outer with distinct 720-row hdem and 1-row store inners. R97
observed positive bucket statistics and small final walks, but did not prove
that Goopg's match fraction, match count, or PG virtual geometry equals the
corresponding PG values. It also did not identify which of those inputs, if
any, explains their level-two run-cost gap.

## Authorized work: measurement only

Before any production behavior change, add temporary, default-off,
env-gated diagnostics at the existing final-cost input/construction boundary.
They may report, per Q96 hash orientation:

1. joinrel/outer/inner relation identities; total and candidate path rows;
   unique-proof verdict; `joinrel.Rows / outer.Rows`; the effective matched
   row count; the fixed effective match count and inner scan fraction; the
   inner bucket size; hash-clause count; and each matched/unmatched term;
2. every `pgHashGeometry` input and result (`innerRows`, emitted width,
   work memory, buckets, batches, virtual buckets) plus a decline reason; and
3. outer and inner child startup/total, output rows, residual-qual delta, the
   non-unique baseline, and pre/post-final-branch startup/run/total,
   sufficient to reconcile each filed candidate with `DPPATH` exactly.

Run the opt-in Q96 EXPLAIN at least twice against a private SF0.25 clone,
capture stdout and stderr separately, and require byte-identical EXPLAIN.
Use only the existing live PG18.3 capture/source as an anchor; do not change
the read-only `postgres/` tree. Remove all diagnostics before any commit.
The report must state one of:

- **ATTRIBUTED:** a specific, representable PG input is missing or
  mis-transcribed, with exact figures and an explicitly bounded follow-up
  implementation scope; or
- **UNOBSERVABLE:** PG's required input cannot be recovered from evidence
  available to Goopg, so decline rather than invent a constant.

PG's source provides the formulas but neither EXPLAIN nor the existing live
capture exposes Q96's `outer_match_frac`, `match_count`, or
`numbuckets * numbatches`. The report therefore **must** use UNOBSERVABLE for
those PG values unless it names and records a lawful, reproducible derivation
from captured PG relation/path/statistics inputs. A matching final cost alone
is not evidence that those hidden inputs agree.

## Hard boundaries

R98 changes no cost formula, selectivity, path filing, worker sizing, Gather
policy, executor hash table, or plan election. It does not force Q96's join
order. It does not broaden R90's sole-base bare-unique proof, infer MCV data,
or treat executor `hashsize.Choose` as PostgreSQL geometry. It neither changes
defaults nor commits diagnostic code.

## Required proof and handoff

Pin all diagnostic values to the correct serial or partial candidate
coordinate: total-relation semifactors are shared, while `rint` uses the
candidate outer rows; a partial inner remains complete. Include both Q96
first-level orientations and their selected level-three continuation. Prove
the no-diagnostic binary yields the original natural and opt-in EXPLAIN and
the same Q96 value. Keep Q9/Q41/Q91 as read-only plan/value controls.

Before temporary instrumentation: agent review, correction if required,
`git commit -n`, and push this scope. After the measurement: remove it, run
focused optimizer tests plus `go test ./internal/optimizer`, `go vet
./internal/optimizer`, and `git diff --check`; commit and push the English
report. A subsequent production-cost change requires a separate reviewed
scope and the full TPC-H/SF0.25/census gate suite.
