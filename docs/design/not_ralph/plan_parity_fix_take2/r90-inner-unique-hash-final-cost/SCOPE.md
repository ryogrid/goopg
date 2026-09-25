# R90 SCOPE — transport inner-unique hash final-cost inputs

R90 follows R89 report commit `cf168c747`. It authorizes one bounded
implementation: carry sound inner-unique evidence and its matched/unmatched
probe factors into serial and partial Hash Join costing. It does not authorize a
Q96-specific preference, a Gather policy change, a selectivity change, or a
general rewrite of PG's target/qual cost framework.

## 1. Problem and boundary

R89 proves that all four Q96 candidates agree with PG's
`initial_cost_hashjoin` component on the same Goopg inputs. The unpriced
difference is PG's `final_cost_hashjoin` branch at
`costsize.c:4434-4481`: the live selected PG paths state `Inner Unique:
true`, use a partial outer and a complete inner, and charge separate matched
and unmatched bucket walks. Goopg never carries `inner_unique` and always
uses its non-unique bucket-walk term.

The R89 observed tournament is the acceptance witness, not a promise of a
flip: Goopg retains `ss -> store` by 184.7875 at L2 and 50.80625 at L3, while
PG selects `ss -> hd -> store`. A cost change is admissible only after it
uses a sound input at both serial and partial call sites.

## 2. Authorized design

Introduce a small immutable hash-final-cost input owned by the join path,
rather than deriving uniqueness in `hashJoinCost` from an unrelated global:

* `innerUnique` is true only for `parser.JoinInner` when the inner is a
  complete, non-parameterized sole base relation and the inner-side columns
  of the hash clauses collectively cover one complete non-partial *bare*
  unique index. Extra hash clauses may coexist. Reuse the existing
  `provableKeys` / `provableJoinKeys` coverage discipline, but reject
  `fromFK` evidence and do not consume clauses or alter
  cardinality/selectivity. Decline expression keys, partial unique indexes,
  incomplete composite keys, joined inners, and uncertain provenance. LEFT
  joins retain the old cost: their emitted `joinRows / outerRows` includes
  unmatched outer rows and is not an outer-match fraction.
* For a sound inner-unique INNER path, derive semifactors once in total
  relation coordinates: `outerMatchFrac = clamp(joinrel.Rows / outerRel.Rows)`
  and `matchCount = 1`. Pass the same fraction to serial and partial siblings;
  only `outerMatched = math.RoundToEven(candidateOuterPath.Rows *
  outerMatchFrac)` uses a candidate's per-worker outer rows. This is the
  `compute_semi_anti_join_factors` identity when at most one inner row can
  match an outer row. Preserve current behavior for LEFT, SEMI, and ANTI;
  they need separately scoped join-semantics work.
* Replace only the existing non-unique bucket-walk CPU charge for a proved
  inner-unique path with PG's matched and unmatched charge shapes:
  matched probes use `outerMatched = rint(outerRows * outerMatchFrac)` and
  `innerScanFrac = 2 / (matchCount + 1)` and Goopg's existing
  `innerBucketSize`. The unmatched tuple-walk charge is zero in this
  executor-coordinate adaptation: an unmatched canonical key has no candidate
  `map` slice to walk. Do not substitute `hashsize.Choose`'s `NBuckets` or
  `NBatch` for PG's `virtualbuckets`; they describe map capacity and memory
  batching, not Goopg candidate-chain space.
* This is deliberately a bounded model adaptation, not a claim that Goopg
  now has PG's unmatched virtual-bucket term, packed-tuple geometry,
  MCV-frequency suppression, general `QualCost`, or pathtarget costs. The
  source/comment must name those remaining inputs. R89's 1,024-bucket,
  one-batch Q96 observation is sizing evidence only and must not be used as
  a cost denominator.
* Thread the same final-cost input into `addHashJoinPath` and
  `addPartialHashJoinPath`. Partial paths retain a per-worker outer and
  complete inner build; no extra worker divisor or build multiplier is allowed.
  Update every direct unit-test constructor and make the zero-value input
  preserve the old non-unique result byte-for-byte.

## 3. Required proof and tests

Before implementation, locate the exact sole-base/key-side proof at both
estimator coordinate boundaries and add focused negative tests for partial,
expression, incomplete-composite, joined-inner, and parameterized-inner
evidence. Add positive serial and partial tests that prove:

1. a unique inner produces `outerMatched`, scan fraction, matched, and
   zero unmatched term from the stated executor-coordinate rule, including a
   `.5` rounding tie that pins `math.RoundToEven`;
2. a non-unique input is numerically unchanged;
3. serial and partial siblings receive the same complete-inner proof, while
   only the partial outer rows are divided and both use the same total-rel
   match fraction;
4. a two-sided unique pair evaluates each orientation independently; and
5. LEFT, SEMI, and ANTI preserve the old cost; and
6. a nonzero residual remains separately charged exactly once.

After code, run focused optimizer tests, then
`go test ./internal/optimizer ./internal/executor
./internal/testutil/estimateaudit` and matching `go vet`. Capture Q96
natural A/A and `GOOPG_GATHER_PATHS=top`, Q96 values against PG, and Q9/Q41/
Q91/Q96 trace-on/off byte controls. Because R90 changes production cost, run
the full TPC-H digest, SF0.25 values sweep, and fresh live-PG plan census
before its implementation report.

## 4. Non-goals and process

Do not add a forced order, tune a GUC, change R88 selectivity, or fill an
unrepresented PG MCV/QualCost/pathtarget value with a constant. If the sound
unique proof is not available at a call site, retain the old cost and record
the decline.

This scope requires agent review, correction, `git commit -n`, and push
before implementation. The implementation and its evidence report require
separate reviews and commits/pushes.
