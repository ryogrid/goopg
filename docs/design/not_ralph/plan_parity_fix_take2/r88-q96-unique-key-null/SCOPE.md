# R88 SCOPE — bare-unique join cardinality must retain null-aware equality

R88 is the implementation successor to R87 (`115de19a3`).  It corrects one
specific selectivity-model divergence: a bare unique index may prove a
no-fan-out upper bound, but it must not be treated as a declared foreign key
whose equality clause is removed and replaced by `1/raw-key-rows`.

## 0. Evidence and selected boundary

R87 established the earliest Q96 F-branch mismatch under the pinned SF0.25
environment:

* the partial `ss ⋈ hd` and `ss ⋈ store` paths use goopg's bare-unique
  shortcut, yielding 23222 and 19352 rows;
* PG has no relevant declared FK and no two-sided MCV branch, so its ordinary
  `eqjoinsel_inner` applies the fact-side null fractions, yielding the visible
  `ss ⋈ hd` estimate 22198 and a source-rule `ss ⋈ store` estimate 18504;
* the 186.54 L2 cost gap is mostly a faithful 147.8375 filtered-inner/hash
  startup difference.  The null-aware correction changes only about 1.76 of
  that narrow L2 gap when all other terms are held fixed, so R88 must not
  promise a Q96 winner or force any tree.

The source boundary is deliberately narrow:

| evidence kind | declared FK | bare unique index |
| --- | --- | --- |
| selectivity treatment | PG `get_foreign_key_join_selectivity`: consume matching clause(s), multiply `1/ref_tuples` | retain matching clause(s) for ordinary equality selectivity; apply both null complements in the no-MCV arm and the existing pairwise MCV model only when both slots exist |
| structural implication | referenced side is unique; apply the sound row bound | key side is unique; apply the same sound row bound only |

The second row is a goopg structural safeguard, not a claim that PG's FK
routine fires for a bare unique index.  A full source-faithful model of PG's
single-column `isunique` and multi-column statistics remains outside this
round; R88 only removes the demonstrable error of replacing a bare-unique
equality's selectivity with FK selectivity.

Not selected:

* changing a hash-cost term, width, work_mem, an enable GUC, an ordering
  preference, Gather admission, the partial-path comparator, or NLI execution;
* loading data, changing statistics, introducing a Q96-only exception, or
  forcing a PG alternative plan;
* altering declared-FK INNER/SEMI/ANTI behaviour, which has its own PG source
  contract;
* closing the later R86 Gather/NLI issue or claiming all corpus parity.

## 1. Design

### 1.1 Separate two proofs that the current code conflates

`superkeyJoinSelectivity` in the PG-shaped DP and `superkeyJoinEstimate` in
the completed-plan estimator currently use one "covered" result for both
facts: (a) whether an equality may be removed from selectivity accounting and
(b) whether an output cardinality ceiling exists.  R88 will represent those
facts separately.

For a proven **declared FK**, preserve the existing PG-shaped consuming path:
remove exactly its fully covered clauses; multiply `1/ref_tuples` for INNER
joins; retain the existing SEMI/ANTI FK guard; and carry the existing row
bound.  A partial key cover must still prove neither treatment.

For a proven **bare unique index**, retain all of its equality clauses in the
residual/ordinary equijoin loop.  It may update only `rowsBound`.  In the DP,
this reaches the existing null-aware code.  In the completed-plan estimator,
R88 also adds the missing no-MCV `(1-nullfrac-left) *
(1-nullfrac-right)` factor exactly once; a pair with MCV lists on both sides
continues through the existing pairwise MCV model and must not receive a second
null correction.  A single-column unique key retains the existing ordinary
`isunique` NDV behaviour (including its separately ledgered fidelity limits);
R88 must not use raw-key rows as a **replacement selectivity** or synthesize a
composite NDV.  Composite bare-unique keys retain each ordinary equality
rather than being replaced by a composite FK estimate.

Candidate state and precedence are independent.  Discovery first records
non-partial bare-unique bounds without consuming clauses, then evaluates
declared-FK candidates over the intact clause set and consumes only FK clauses.
A bound-only unique must neither mask an overlapping declared FK nor repeat
forever in a largest-divisor loop.  Both estimators therefore carry at least
separate `boundProven` and `fkConsumed`/selectivity-consumed state; the
completed-plan estimator must use `boundProven` when deciding whether to apply
`rowsBound`, rather than accidentally bypassing it because no FK fired.

Eligible bare-unique evidence is a non-partial unique index only.  An index
with `HasPredicate` is not a relation-wide uniqueness proof unless predicate
implication is proved; R88 does not implement that proof and must decline it
in both `uniqueKeyColumnSets` (plan stamp) and the DP key discovery path.

The two coordinate-space implementations must remain semantically aligned:

1. `joinrelsize.go`: `superkeyEstimate`, `superkeyJoinSelectivity`, and its
   `provenKey`/key-discovery flow; and
2. `joinkeyproof.go` plus `cardinality.go`: `joinSuperkeyEstimate`,
   `superkeyJoinEstimate`, `provenJoinKey`, `bestProvableJoinKey`, and the
   residual equality loop in `estimateJoin`.

The latter currently lacks an evidence-kind bit on `provenJoinKey`; R88 adds
one rather than inferring evidence from a key's shape.  It must identify the
same declared-FK arm that the DP's existing `fromFK` bit identifies.

### 1.2 Required tests

Add focused tests for both coordinate spaces, not a Q96-only fixture:

1. a nullable outer key joined to a bare unique inner key retains the equality
   clause and applies the null complement; its output remains bounded by the
   opposite side's post-filter rows;
2. a non-null bare-unique case retains ordinary equality selectivity rather
   than `1/raw-key-rows` substitution;
3. overlapping parent bare-UNIQUE plus a fully covered declared FK still
   selects the FK consuming `1/ref_tuples` path; the existing SEMI/ANTI guard
   is pinned in the DP only, because completed `estimateJoin` exits through
   `semiJoinMatchFraction` before `superkeyJoinEstimate`;
4. fully covered composite bare UNIQUE retains every equality, terminates, and
   applies its bound even when its per-column NDVs are unavailable/default,
   proving `boundProven` rather than residual `measured` state makes the bound
   effective; partial composite coverage proves neither consumption nor a key
   bound;
5. a nullable pair with MCV slots on both sides remains on the pairwise MCV
   model and does not receive the new no-MCV null complement twice; and
6. a partial unique index is declined fail-closed in both coordinate spaces.

The existing two-estimator parity tests must be extended or complemented so a
future edit cannot fix only DP search cost while leaving printed/legacy rows
on the old shortcut.  Tests must use explicit null fractions/NDVs and assert
the formulas, not merely the winner of a hand-built path list.

## 2. Verification protocol

All program execution is foreground.  Before any benchmark, build a clean
binary from the committed R88 scope, use a fresh private SF0.25 clone on an
unused 55xx port, and pin the R87 session GUCs (`work_mem=64MB`, four maximum
parallel workers, leader participation on).  Retain artifacts under
`/tmp/pp2/r88/`.

Required gates after implementation:

1. focused optimizer tests plus `go test ./internal/optimizer
   ./internal/executor ./internal/testutil/estimateaudit` and same-package
   `go vet`;
2. natural Q96 EXPLAIN trace A/A, live PG Q96 capture, and a diagnostic
   `GOOPG_GATHER_PATHS=top` trace only to inspect candidates;
3. an exact table showing the post-change L2 rows, each applicable null/NDV
   input, L2/L3 costs, path properties, and partial-list survivor.  Do not
   declare success if Q96 does not move; report the measured result;
4. Q96 values against PG and trace-on/off byte controls for Q9/Q41/Q91/Q96;
5. the full corpus gates are mandatory because this is a global estimator
   change: TPC-H digest, SF0.25 sweep, and live-PG shape diff/census.  A values
   break stops the round; identical plans are not a timing regression.

The result report must compare Q9 as an explicit blast-radius witness because
the old unique-index extension was introduced for composite-key estimates.
It must state whether the source-faithful selectivity reaches Q96, whether it
changes the L3 survivor, and which remaining divergence is next if it does
not.  Temporary trace code, if needed, must be default-off and removed before
the report commit.

## 3. Acceptance and process

This scope authorizes no code until it has been agent-reviewed, reflected,
committed with `git commit -n`, and pushed.  The implementation and its
results report require separate commits/pushes.  Success for R88 is a
source-faithful distinction between declared-FK selectivity and bare-unique
row bounds in both estimators, backed by the listed evidence; it is not an
authorized forced Q96 shape.
