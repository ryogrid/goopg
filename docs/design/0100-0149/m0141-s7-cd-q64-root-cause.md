# M0141-S7-cd-q64 — Q64 was miscategorized, not under-served

Status: accepted (landed `073ab2748`, 2026-09-18). Landed env-gated traces in non-test `internal/optimizer` files (`traceOrderedCandidatePopulation`, `traceIncrementalSortCandidate` in `pathtrace.go`, both behind the pre-existing `GOOPG_PGSHAPED_DP_TRACE`), for which the owner reclassified the task `Kind: impl` on 2026-09-18.

Parent: M0141-S7 — see [m0141-s7-readjudicate-and-scope-incremental-sort.md](m0141-s7-readjudicate-and-scope-incremental-sort.md)

## Update 2026-09-18b — M0141-S7-cd-q64 root-caused: Q64 was miscategorized,
not under-served

The prior update left Q64 undecided between two hypotheses ("SearchCandidates
empty" vs. "every candidate's keys `len==0`"), both reachable only by
instrumenting `createOrderedPaths`'s population step — an `internal/`-file
change that update's own diagnosis-only mandate declined. This update lands
that instrumentation (two new DPPATH producer strings, both gated on the
existing `pathTraceEnabled`/`GOOPG_PGSHAPED_DP_TRACE=1` flag so the change is
inert with the flag at its default-off setting — same posture as every other
groundwork step in this file):

- `traceOrderedCandidatePopulation` (`pathtrace.go`), called from
  `createOrderedPaths` (`upperordered.go:109-131`) right where
  `RelOptInfo.SearchCandidates`/`SearchCandidateKeys` are populated: emits
  `producer=upper.ordered.candidates searchedrel=<bool> candidates=<n>
  nonemptykeys=<n>` — settling exactly the ambiguity the prior update left
  open.
- `traceIncrementalSortCandidate` (`pathtrace.go`), called from
  `addIncrementalSortPaths`'s per-candidate loop
  (`incrementalsortpaths.go:160-163`) right after
  `pathkeysCountContainedIn`: emits `producer=upper.ordered.incrementalsort.candidate
  index=<i> kind=<PathKind> keys=<len> contained=<bool> ncommon=<n>` for
  every candidate considered, whether or not it survives to an offer — the
  finer sibling `M0141-S7-cd-candidatepool` (below) also benefits from,
  since it answers "which candidates were even in the running" per query
  without a debugger attach.

### How Q64 was traced without touching `:65437`

The RALPH_LOOP guard (added since the 2026-09-17h/2026-09-18 traces in this
file, which DID restart `:65437` directly) now blocks restarting any shared
TPC-DS server from this loop. Traced instead on a fully private, throwaway
cluster: `go build -o tmp/m0141s7cdq64/goopg ./cmd/goopg`, `goopg init -D
tmp/m0141s7cdq64/data`, started via `scripts/goopg-test-run.sh` on port 5533
(cgroup-capped, own `GOOPG_CG_UNIT`) with `GOOPG_PGSHAPED_DP_TRACE=1
GOOPG_INCREMENTAL_SORT=on` confirmed via `/proc/<pid>/environ` before
capturing — same discipline as every prior trace in this doc, just on a
private port instead of `:65437`. Schema + the already-sampled SF0.25 TSVs
(`bench/tpcds/runtime_goopg/tpcds-data-sf025/*.tsv`, git-untracked scratch
already on disk from a prior `tpcds-sf025-regression.sh build-data` run) were
loaded fresh into this private cluster's own `postgres` database via the same
`tpcds.sql` + per-table `COPY ... FROM` + `ANALYZE` sequence
`cmd_load_goopg` (`scripts/tpcds-sf025-regression.sh:412`) uses against
`:65437` — no read from, or write to, the live `:65437` data directory at
any point. `bench/tpcds/runtime_goopg/tpcds-data/queries/query64.sql` (the
SF1 query text; TPC-DS query bodies are scale-independent, only the tables'
row counts differ) was then run against it directly.

### The trace result

```
DPPATH candidates producer=upper.ordered.candidates searchedrel=true candidates=7 nonemptykeys=6
DPPATH path producer=upper.ordered.sort ... pathkeys=5 verdict=accepted ...
DPPATH candidate producer=upper.ordered.incrementalsort.candidate index=0 kind=4 keys=3 contained=false ncommon=0
DPPATH candidate producer=upper.ordered.incrementalsort.candidate index=1 kind=4 keys=3 contained=false ncommon=0
DPPATH candidate producer=upper.ordered.incrementalsort.candidate index=2 kind=4 keys=3 contained=false ncommon=0
DPPATH candidate producer=upper.ordered.incrementalsort.candidate index=4 kind=4 keys=3 contained=false ncommon=0
DPPATH candidate producer=upper.ordered.incrementalsort.candidate index=5 kind=4 keys=3 contained=false ncommon=0
DPPATH candidate producer=upper.ordered.incrementalsort.candidate index=6 kind=4 keys=3 contained=false ncommon=0
```

**Neither of the prior update's two hypotheses holds.** `searchedRelOf(input)`
returns a real rel (`searchedrel=true`); `SearchCandidates` has 7 entries, 6
with a non-empty validated ordering claim (`nonemptykeys=6`, matching the 6
traced candidates — index 3 is the seventh, the one with `len(keys)==0`,
which the loop's own `continue` skips before reaching
`traceIncrementalSortCandidate`). Every one of those 6 is `kind=4`
(`PathMergeJoin`) carrying a 3-key ordering claim, and every one scores
`ncommon=0` against the outer query's 5 sort keys — **zero shared columns,
not a partial-prefix miss**. So `addIncrementalSortPaths`'s `nCommon == 0`
branch is exactly what fires, on every candidate, every time.

### Why zero is the CORRECT answer here — Q64 was never an outer-ORDER-BY
witness at all

Re-reading `query64.sql` (`bench/tpcds/runtime_goopg/tpcds-data/queries/query64.sql`)
side by side with PG's plan (`bench/tpcds/plans-pg/Q64.txt`) resolves why:
the query has **two separate, unrelated sort-shaped points**, and the
`Incremental Sort` node PG's plan actually contains belongs to the one this
whole investigation (M0141-S7-cd-q64/candidatepool, and — going back further
— the original 2026-09-16/17 witness classification's own "Nested Loop /
`item.i_item_sk`" row for Q64, quoted near the top of this file) was never
looking at:

1. The **outer** query (`select ... from cross_sales cs1, cross_sales cs2
   where ... order by cs1.product_name, cs1.store_name, cs2.cnt, cs1.s1,
   cs2.s1`) is what reaches `createOrderedPaths` — its `ORDER BY` is over
   **aggregate OUTPUT columns of the CTE** (`product_name`, `store_name`,
   `cnt`, `s1` are all `SELECT`-list items of `cross_sales`'s own
   `GROUP BY`/aggregate clause, not raw join columns). In PG's own plan this
   level is satisfied by a **plain `Sort`** — not an Incremental Sort at
   all; PG's own planner reaches the identical `nCommon == 0` conclusion,
   it just has no separate DPPATH-style trace to say so explicitly.
2. The `Incremental Sort` node PG's plan DOES contain
   (`Presorted Key: item.i_item_sk`) sits **inside the CTE's own
   definition**, feeding `cross_sales`'s `GroupAggregate` — a completely
   different planning stage (`cross_sales`'s inner `GROUP BY
   i_product_name, i_item_sk, s_store_name, ...` needs its input sorted by
   the group key, and PG's chosen Nested-Loop-shaped input happens to
   already deliver a leading prefix of that group key, `item.i_item_sk`,
   for free). This is a **sorted-GroupAgg input-sort** decision, made by
   whichever machinery elects the CTE's own aggregate strategy — nothing
   `createOrderedPaths`/`addOrderedPaths` (a statement's outer `ORDER BY`
   only) is ever called for.

So the original witness-classification table's "Q64 → Nested Loop →
`item.i_item_sk`" row was reading the RIGHT plan node but filing it under the
WRONG mechanism family: it is CTE-internal-aggregation-shaped (the same
family as the 5 `GroupAggregate` witnesses already tracked under
**M0141-S2b-0**/**M0141-S2b-7**), not outer-ORDER-BY-shaped (the
**M0141-S2b-2**/**2a**/**2b**/**2c** `Nested Loop`/`Merge Join` family Q64 had
been grouped with since the 2026-09-16 NARROWED update). The `createOrderedPaths`
call this update traced is doing exactly the right thing for the outer
`ORDER BY` it is actually responsible for — the outer ORDER BY's columns are
aggregate outputs no join-shaped candidate could ever carry a prefix of, so
`nCommon == 0` on every candidate is the CORRECT verdict, not a gap. **There is
no outer-ORDER-BY fix for Q64 to make** — M0141-S7-cd-q64's own premise (that
Q64 has an unexplained miss at this call site) does not survive contact with
the query text.

**Consequence for the corpus tally**: the "Nested Loop x4 (Q4, Q11, Q35, Q64)"
row in this file's producer-shape table (and the "6 of 13" no-incrementalsort
tally in the prior update) should read Nested Loop x3 (Q4, Q11, Q35) once Q64
is moved to the CTE-internal-aggregation bucket — **not implemented in this
recon-only loop** (re-tallying and deciding whether Q64 is a genuinely new
6th member of the GroupAggregate family, or already covered by
M0141-S2b-0/S2b-7's own scope, needs its own pass over `electOrderedGrouping`'s
CTE-materialization callers, which is out of this update's one-task budget).
Filed as **M0141-S7-cd-q64-reclassify** below. `M0141-S7-cd-candidatepool`'s
own 7-witness table is unaffected: Q64 was never one of the 7 "reach the arm"
witnesses it covers.

`GOOPG_INCREMENTAL_SORT` stays default-off (unchanged verdict; this update
only adds trace lines, both gated on the pre-existing `GOOPG_PGSHAPED_DP_TRACE`
flag, so the ORDERED tournament is byte-identical to before at every default
setting). No ledger row: this closes an open recon question with a definite
answer using only already-filed mechanism names, and does not itself leave
anything newly unimplemented — the follow-up reclassification task is filed
directly in `fix_plan.md`, same as every other recon task in this
programme.

Gates run: `go build ./...` clean; `go vet ./internal/optimizer/`; `go test
./internal/optimizer/...` PASS (pre-existing suite, unchanged — the two new
trace functions have no test of their own by design, same as
`tracePath`/`formatPathLine`'s sibling coverage via the existing DPPATH
consumers, since the property under test is "identical output with the flag
off," not the trace TEXT itself). `RALPH_PRECOMMIT_SCOPE=units
scripts/ralph-precommit-test.sh` PASS. No TPC-H dependency. The private
`tmp/m0141s7cdq64/` cluster was stopped (not left running) before this
commit; `:65437`/`:65438` were never started, stopped, or restarted by this
loop — confirmed via `bench/tpcds/server.sh status` before and after.
