# M0141-S7-cd-candidatepool — the seed's own unexploited partial-prefix claim (Q4), and why the electOrderedGrouping side has no gap

Status: accepted (landed `c7e231ae1`, 2026-09-18). Landed an env-gated trace in a non-test `internal/optimizer` file (`traceOrderedSeedCandidate` in `pathtrace.go`, plus a `totalcost=` field on `traceIncrementalSortCandidate`), all behind the pre-existing `GOOPG_PGSHAPED_DP_TRACE`; the owner reclassified the task `Kind: impl` on 2026-09-18 for that reason. Files **M0141-S2b-9**.

Parent: M0141-S7 — see [m0141-s7-readjudicate-and-scope-incremental-sort.md](m0141-s7-readjudicate-and-scope-incremental-sort.md)

## Update 2026-09-18d — M0141-S7-cd-candidatepool: the seed itself carries an
unexploited partial-prefix claim on the createOrderedPaths-mechanism side
(Q4); the electOrderedGrouping-mechanism side (Q43/Q54/Q60) has no gap

The task asked two things: (a) does the cheap seed always structurally fail
`addIncrementalSortPaths`'s partial-prefix test (nothing to fix), or (b) does
it sometimes carry a genuine usable partial key the loop is simply never
offered because of how `ordered.SearchCandidates` is populated? Reading
`createOrderedPaths`/`electOrderedGrouping` side by side first surfaced a
fact the prior updates hadn't separated: **the 7-witness corpus splits across
two entirely different `addOrderedPaths` callers**, each populating
`SearchCandidates` a different way:

- `createOrderedPaths` (Q11/Q58/Q83, and Q4) populates `SearchCandidates` from
  `searchedRelOf(input).Pathlist` — the join search's OWN candidate pool for
  the relset that produced `input`.
- `electOrderedGrouping` (Q43/Q54/Q60 — the Hashed-vs-Sorted GroupAgg tie
  family, M0141-S2b-0/S2b-7) populates `SearchCandidates` **directly from its
  own `cands` slice** (the Hashed/Sorted `PathAgg` candidates), explicitly
  because — its own comment says — `searchedRelOf(input)` "does not exist
  here (the input is a GROUP_AGG rel's PathAgg, never a searched join/scan
  root)".

Verifying which of (a)/(b) applies needed more than the existing
`traceIncrementalSortCandidate`/`traceOrderedCandidatePopulation` lines:
neither one reports what the SEED's *own* claim is, so there was no way to
compare the seed against the SearchCandidates entries side by side. Landed
`traceOrderedSeedCandidate` (`pathtrace.go`), called from `addOrderedPaths`
(`upperordered.go`) right before the arm-1 containment check — same
`pathTraceEnabled`-gated, inert-by-default shape as the two existing trace
functions — emitting the seed's own `kind`/`keys`/`contained`/`ncommon`
**plus `totalcost`** on both this new function and
`traceIncrementalSortCandidate` (a small additive change to the latter's
signature): cost is what lets a same-shaped seed/candidate pair be told apart
from a coincidence, since every entry sharing one `ordered.SearchCandidates`
list also shares the same relset and therefore the same `Rows` — cost is the
only field that discriminates "this really is the same Path" from "this just
happens to have the same key count".

### Trace setup

Same private-cluster discipline as M0141-S7-cd-q64 (the RALPH_LOOP guard
still blocks restarting `:65437` directly): `go build -o
tmp/m0141s7candpool/goopg ./cmd/goopg`, `goopg init -D
tmp/m0141s7candpool/data`, started via `scripts/goopg-test-run.sh` on port
5533 (cgroup-capped, own `GOOPG_CG_UNIT`) with `GOOPG_PGSHAPED_DP_TRACE=1
GOOPG_INCREMENTAL_SORT=on` (confirmed via `/proc/<pid>/environ`). Schema +
the same already-sampled SF0.25 TSVs
(`bench/tpcds/runtime_goopg/tpcds-data-sf025/*.tsv`) loaded into this
cluster's own `postgres` database via the standard `tpcds.sql` + per-table
`COPY` + `ANALYZE` sequence. All seven witnesses
(`bench/tpcds/runtime_goopg/tpcds-data/queries/query{4,11,43,54,58,60,83}.sql`)
were `EXPLAIN`'d directly against it; no read from or write to `:65437`/
`:65438` at any point.

### Trace result (seed vs. SearchCandidates, cost-annotated)

```
Q4:  seed  kind=0 keys=1 contained=false ncommon=1 totalcost=13.7925
     cand2 kind=4 keys=1 contained=false ncommon=1 totalcost=13.7825   <- closest, NOT equal
Q11: seed  kind=0 keys=0 contained=false ncommon=0 totalcost=6.7775    <- no claim at all
Q43: seed  kind=6 keys=2 contained=false ncommon=2 totalcost=47682.78310407106
     cand1 kind=6 keys=2 contained=false ncommon=2 totalcost=47682.78310407106  <- EXACT match
Q54: seed  kind=6 keys=1 contained=false ncommon=1 totalcost=1.150537478050103
     cand1 kind=6 keys=1 contained=false ncommon=1 totalcost=1.150537478050103  <- EXACT match
Q58: seed  kind=0 keys=0 contained=false ncommon=0 totalcost=4.78              <- no claim at all
Q60: seed  kind=6 keys=1 contained=false ncommon=1 totalcost=2.9476245279653694
     cand1 kind=6 keys=1 contained=false ncommon=1 totalcost=2.9476245279653694 <- EXACT match
Q83: seed  kind=0 keys=0 contained=false ncommon=0 totalcost=3.5725            <- no claim at all
```

(Full stderr captures: `/tmp/candpool2-q{4,11,43,54,58,60,83}.explain.log`
against `/tmp/candpool-start2.log`, this loop's scratch — not committed.)

### Reading the result: two different verdicts for two different mechanisms

**`electOrderedGrouping` side (Q43/Q54/Q60) — answer (a), no gap.** Every
time the loop iterates to the Sorted-agg candidate (`cands[1]`, the one with
a non-empty translated key), the `offer` it builds (`offer := *c; offer.Pathkeys
= translated[i]`) IS a shallow copy of that same candidate — so the "seed"
`addOrderedPaths` receives on that iteration and the `SearchCandidates[1]`
entry the arm-3 loop later re-examines are cost-identical, not just
shape-identical (47682.78310407106 / 1.150537478050103 / 2.9476245279653694,
exact bit-for-bit matches across all three witnesses). `electOrderedGrouping`
was already engineered (M0141-S2b-7) specifically to make its own candidate
pool double as `SearchCandidates`, and this trace confirms that engineering
does what it says: whenever the seed itself carries a partial claim, an
Incremental Sort candidate over it is already built and already costed
(and already loses to the plain Sort in these three cases per the
2026-09-18 update's cost table — a cost-model finding, not a coverage gap).
**Nothing to fix for this family.**

**`createOrderedPaths` side, no-claim witnesses (Q11/Q58/Q83) — answer (a),
no gap.** The seed's own `keys=0`: the cheapest-by-fraction join order
literally has no natural ordering to offer a partial prefix from (this is
the "hash-shaped" case the task's own (a) branch anticipated, generalized to
any Kind with an empty pathkeys claim, not just hash joins specifically —
here it is `PathPrebuilt`-wrapped output of a plan that happens to carry no
propagated order). Other, pricier `SearchCandidates` entries DO carry
partial claims (e.g. Q83's `index=0`, `ncommon=1`) and are correctly offered
— and correctly lose on cost, the same "input-candidate divergence"
already tracked. **Nothing to fix for these three.**

**`createOrderedPaths` side, Q4 — answer (b), confirmed gap.** Q4's seed has
`keys=1 ncommon=1`, a genuine, non-empty, non-full partial-prefix claim — the
exact shape `addIncrementalSortPaths` exists to exploit. But no
`SearchCandidates` entry matches its cost exactly: the closest,
`candidate[2]`, is 0.01 cheaper (13.7825 vs. 13.7925). This is not noise —
Q4 carries a `LIMIT 100` (`query4.sql:113`), so the seed was chosen by
`getCheapestFractionalPath`'s **fractional** (startup-weighted) cost metric,
not raw `Total`; `candidate[2]` is a genuinely different Path (better on raw
Total, presumably worse on Startup, which is why the fractional metric didn't
pick it) that merely happens to share the same 1-key ordering claim. The
seed itself — the one `sortPathForBounded` actually builds arm-2's full Sort
over — is confirmed **absent** from `ordered.SearchCandidates`: that list is
literally `sr.Pathlist`, populated independently of whichever entry
`getCheapestFractionalPath` picked to become `input`, so
`addIncrementalSortPaths`'s loop can only ever attach a `PathIncrementalSort`
to one of `sr.Pathlist`'s OTHER members — never to the seed itself, even when
the seed's own claim would qualify. **Real, structural, confirmed gap.**

### Disposition

`GOOPG_INCREMENTAL_SORT` stays default-off; no cost-model or plan-shape
change lands this loop (banner item 4 restricts M0141-S7 to cost diagnosis).
The confirmed Q4-side gap is filed as **M0141-S2b-9** below: teach
`addIncrementalSortPaths` (or its caller) to also score `input` itself
against `sortPathkeys` and offer a `PathIncrementalSort` built over the seed
when `0 < nCommon < len(sortPathkeys)` — using the SAME fractional-cost-aware
comparison `getCheapestFractionalPath` already applies, since Q4's own LIMIT
is exactly why a naive Total-cost seed-candidate would be the wrong thing to
build here. No ledger row (same posture as 18b/18c: a filed follow-up task
carries the deferral, not an undocumented gap).

Gates: `go build ./...` clean, `go vet ./internal/optimizer/` clean, `go test
./internal/optimizer/...` PASS (existing `captureTrace`-based tests in
`upperordered_test.go`/`incrementalsortpaths_test.go` still compile against
the two trace functions' extended signatures — no assertion on the new
`totalcost=` field was needed since none of those tests parse trace output
by field count). `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh`
PASS. No TPC-H dependency; the private port-5533 cluster and its throwaway
`tmp/m0141s7candpool/` tree are scratch, stopped and left for the next loop
to reap or reuse (not committed, `tmp/` is git-ignored).
