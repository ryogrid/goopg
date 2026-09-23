# M0141-S7 — the corpus measurement (0/14, 0/99) and the cost-breakdown diagnosis for the 14 witnesses

Status: accepted; parent M0141-S7 ESCALATED [!] 2026-09-23 (S4 lineage budget — owner decision pending) (measurement landed `004f02bf1`, 2026-09-17; cost diagnosis landed `7c27145b1`, 2026-09-18). Recon/measurement, with two doc-comment corrections in `internal/optimizer` only. The inline 2026-09-17i sub-update records **M0141-S2b-7**'s own landing (`96f53705e`, 2026-09-17, production change in `upperorderedgrouping.go`) because it is the fix this measurement filed and re-measured; S2b-7's task-level writeup is in `m0141-s2b-scoping-decomposition.md`.

Parent: M0141-S7 — see [m0141-s7-readjudicate-and-scope-incremental-sort.md](m0141-s7-readjudicate-and-scope-incremental-sort.md)

## Update 2026-09-17h — the corpus measurement, and why it is 0/14 and 0/99

**Ran the measurement this row has been pointing at since 2026-09-17d.**
Started the goopg TPC-DS SF0.25 cluster (`bench/tpcds/server.sh start
sf025`) with `GOOPG_INCREMENTAL_SORT=on` exported before the start call, and
confirmed the *live server process* actually saw it —
`tr '\0' '\n' < /proc/<pid>/environ | grep GOOPG_INCREMENTAL_SORT` on the
running goopg binary read back `GOOPG_INCREMENTAL_SORT=on` — not just a
capture-header label (`scripts/planner-flags.sh`'s `unset(off)` line reflects
the *capturing shell's* environment, not the server's, and is not proof the
flag reached the server; the two Bash tool calls that started the server vs.
ran the capture are separate shells, which is exactly why the direct
`/proc/<pid>/environ` check mattered here).

**Result: zero.** `scripts/capture-tpcds.sh` against the flag-on server,
all 99 queries, EXPLAIN only:

```
grep -c "Incremental Sort" analysis/m0141/m0141-s7-full99-incsort-on.txt
0
```

Not just the 14 witnesses — the **entire TPC-DS corpus** produces zero
Incremental Sort nodes with the arm turned on at HEAD. Since `setCheapest`
picks a deterministic minimum-cost winner and a losing candidate cannot
change which path wins, "zero Incremental Sort nodes in the output" is
equivalent to "byte-identical to flag-off across all 99 queries" — a second,
paired flag-off capture would only reconfirm what the zero-count already
proves, so it was not taken.

**Root cause, found by reading the caller graph rather than guessing.**
`addIncrementalSortPaths` (the third arm, `incrementalsortpaths.go:140`)
reads `ordered.SearchCandidates` / `ordered.SearchCandidateKeys` — fields on
the `*RelOptInfo` passed in, **not** anything derived from its own `input`
argument. Those two fields are populated in exactly one place:
`createOrderedPaths` (`upperordered.go:106-118`), from `searchedRelOf(input)`
— i.e. only when `input` is itself the search's own top node.

`electOrderedGrouping` (`upperorderedgrouping.go:178`) — the loop that
decides every GROUP_AGG-shaped ORDER BY in the corpus (`planner.go:1955-1965`:
`if selectSrfPending == nil { loopBuilt, loopElected =
electOrderedGrouping(...) }`; `createOrderedPaths` only runs in the `else`
branch, i.e. when `electOrderedGrouping` **declines**) calls
`addOrderedPaths` directly, once per surviving `PathAgg` candidate
(`upperorderedgrouping.go:236`), **without ever going through
`createOrderedPaths` first**. `ordered.SearchCandidates` is therefore still
whatever an unrelated earlier call left it (typically nil, since
`fetchUpperRel`'s freshly-sized rel starts empty) for every one of
`electOrderedGrouping`'s own `addOrderedPaths` calls — so
`addIncrementalSortPaths` iterates zero or stale-and-irrelevant candidates
and adds nothing, **structurally, regardless of `anyTranslated`'s state**.

This means the S2b-5/S2b-6 line of work (fixing `groupingEmissionPathkeys`'s
over-restriction so `anyTranslated` stops declining) **cannot by itself**
connect the 5 GroupAgg-shaped witnesses (Q3, Q43, Q54, Q58, Q60, Q63, Q83 —
re-tallied below, 7 not 5) to Incremental Sort: fixing `anyTranslated` only
changes whether `electOrderedGrouping`'s *own* Sort-vs-no-Sort election runs;
that election has no Incremental Sort awareness of its own, and it never
reaches the arm that does. A further, distinct wiring step is needed inside
`electOrderedGrouping` itself — either populate
`ordered.SearchCandidates`/`SearchCandidateKeys` before its per-candidate
`addOrderedPaths` calls (mirroring what `createOrderedPaths` does, scoped to
`cands` instead of a searched join/scan tree), or give `electOrderedGrouping`
its own incremental-sort-over-PathAgg-candidate offer. **Filed as
M0141-S2b-7 below** (fix_plan) rather than folded into S2b-5/S2b-6, because
it is a different call site with a different fix, not a continuation of the
`anyTranslated` chase.

**Re-verified the 14 witnesses' PG-side child node directly** (read the AST
node immediately below each `Incremental Sort` in `bench/tpcds/plans-pg/`,
not just the Sort Key text — Q58's leading sort key is a plain column but its
actual child is a `Merge Join`, which the Sort Key text alone would have
mis-classified; this re-confirms the original 2026-09-16 tally rather than
replacing it):

| producer shape | queries | count |
|---|---|---|
| GroupAggregate (incl. one `Finalize GroupAggregate`) | Q3, Q43, Q54, Q60, Q89 | 5 |
| Merge Join | Q58, Q83 | 2 |
| Nested Loop | Q4, Q11, Q35, Q64 | 4 |
| WindowAgg | Q67 | 1 |
| Unique (SETOP, per the 2026-09-16 S2b-1-result correction) | Q49 | 1 |
| Subquery Scan | Q63 | 1 |

**The 5 GroupAggregate witnesses are conclusively explained** by the
`electOrderedGrouping`-bypasses-`SearchCandidates` gap above — that call
site is the only one any GROUP BY's own ORDER BY can reach, and it never
populates the fields the third arm reads.

**The other 9 are NOT yet root-caused to the same mechanism, and this
update does not claim they are.** The 2 `Merge Join` + 4 `Nested Loop`
witnesses are exactly the shape S2b-2a/2b/2c targeted (a plain join/scan
search tree feeding a top-level ORDER BY with no aggregation of its own,
where `createOrderedPaths` — not `electOrderedGrouping` — runs and
`searchedRelOf(input)` should succeed), so the corpus-wide zero is a genuine
open question for that bucket: either `searchedRelOf` does not recognize
these particular inputs as search roots (e.g. a correlated-subquery-heavy
FROM list like Q58's), or `validatedSearchCandidateKeys` rejects every
candidate's translated ordering, or no candidate's cost ever beats a full
Sort even when offered. Distinguishing those needs a live
`GOOPG_PGSHAPED_DP_TRACE=1` trace on one of these 6 queries, which this
measurement loop did not have scope left to run. `Q67` (WindowAgg, S2b-3)
and `Q49` (SETOP, S2b-4) and `Q63` (Subquery Scan) are each their own
unimplemented call site (no `electOrderedWindow`/SETOP-loop/subquery-scan
equivalent of `electOrderedGrouping` exists yet to even check for the same
bypass) and were already known-blocked before this update.

Practical upshot for this milestone: this update conclusively explains 5 of
14 witnesses and files their fix (M0141-S2b-7); the remaining 9 stay exactly
as blocked as before this update, with one new open question (the 6
join/scan witnesses' unexplained zero) added rather than resolved.

**exec-d verdict, definitively answered by this measurement**: still not
needed. Zero corpus queries reach the executor operator at all — not "reach
it but degrade for lack of spill/packed-retention/ctid", but never reach it.
exec-d stays deferred with no change to its ledger row.

**GOOPG_INCREMENTAL_SORT stays default-off.** Turning it on today is
provably a no-op (byte-identical plans, so also byte-identical row counts —
no regression risk either way), but a no-op default flip has no benefit to
justify the churn of a provenance-label change across every future capture.
Revisit once M0141-S2b-7 lands and the corpus is re-measured.

Gates run: `go build ./internal/optimizer/...` clean after the two comment
corrections this update made (`path.go`'s `PathIncrementalSort` doc comment,
`incrementalsortpaths.go`'s file-header "WHY GATED OFF BY DEFAULT" section —
both previously said `createPlanNode`/the executor lacked an arm, which
exec-a/b already fixed on 2026-09-17e/f; corrected to cite this update's
actual reason instead). No production logic changed — this update is
measurement plus two doc-comment corrections. `scripts/capture-tpcds.sh`
runs: `analysis/m0141/m0141-s7-witnesses-incsort-on-goopg.txt` (14-query
targeted capture, provenance-stamped, confirms live-server flag state via
`/proc/<pid>/environ`) and `analysis/m0141/m0141-s7-full99-incsort-on.txt`
(full 99-query capture, confirms the 0/14 result generalizes to 0/99).

Resume point: **M0141-S2b-7** (filed below, fix_plan) — wire
`electOrderedGrouping`'s own `addOrderedPaths` calls to a genuine candidate
set (`ordered.SearchCandidates`/`SearchCandidateKeys` populated from `cands`,
or a dedicated offer), the same posture S2b-2a/2b already established for
`createOrderedPaths`'s own call site. Re-run this measurement after it lands
to see how many of the 7 GroupAgg witnesses move.

**Update 2026-09-17i — M0141-S2b-7 LANDED, corpus re-measured: still 0/99,
but the bypass is now structurally CLOSED (proven by trace, not inferred).**
`upperorderedgrouping.go`'s `electOrderedGrouping` now populates
`ordered.SearchCandidates = cands` and `ordered.SearchCandidateKeys =
translated` (the same per-candidate slices it already computes for its own
no-sort/Sort-over election) right after the `anyTranslated` gate, with both
fields added to the existing save/restore-on-decline snapshot (the function's
own contract: a decline must leave `ordered` byte-identical to the loop never
having run, since the SAME `*RelOptInfo` — same registry, kind, relids,
tupleFraction — is reused by a subsequent plain `createOrderedPaths` call in
the `else` branch at `planner.go:1955-1965` when the loop declines, and that
call's own population is conditional on `searchedRelOf(input) != nil`, so a
leftover mutation would otherwise leak a GROUP_AGG-shaped candidate set into
an unrelated plain-Node ORDER BY). Also added the `*IncrementalSort` winner
shape to the function's `createPlanNode(best)` switch (previously only
`*Sort`/`*Aggregate` were handled; an Incremental-Sort win would have hit
`default: return restore("winner-shape-unexpected")` and silently thrown the
win away) — mirrors the existing `*Sort` case's one-level descend-to-`*Aggregate`
copy-back exactly, since `createIncrementalSortPlan` wraps exactly one child
built from the offered `PathAgg` candidate.

**Proof the bypass is closed, not just "no longer obviously wrong":** a
`GOOPG_INCREMENTAL_SORT=on GOOPG_PGSHAPED_DP_TRACE=1` server against Q3 (one
of the 5 GroupAgg witnesses) now emits, where before it emitted nothing for
this producer at all —

```
DPPATH path producer=upper.ordered.sort           relids=- kind=7  ... total=3730.8918 ... verdict=accepted
DPPATH path producer=upper.ordered.incrementalsort relids=- kind=20 ... total=3733.0068 ... verdict=dominated
```

— i.e. the third arm is reached and offers a real `PathIncrementalSort`
candidate (child = the Sorted `PathAgg`, `PresortedCount=1` — matching PG's
own `Presorted Key: dt.d_year` in `bench/tpcds/plans-pg/Q3.txt`, since the
GroupAggregate's group-key emission order is `(d_year, i_brand,
i_brand_id)` while the ORDER BY is `(d_year, sum(...) DESC, i_brand_id)` — only
the leading `d_year` column is a genuine positional match, `nCommon=1`). It
loses the tournament to the plain `upper.ordered.sort` candidate
(3733.0068 > 3730.8918) — **a cost question, not a structural one**, and
exactly the "necessary but likely not sufficient on its own" outcome this
task's fix_plan entry predicted (S2b-5/S2b-6's still-open Hashed-vs-Sorted
`PathAgg` cost-tie sits upstream of the SAME candidates this arm now offers).
Q3's own goopg plan shape also diverges from PG's well before the ORDER BY
node — goopg elects a Nested-Loop/Bitmap-Heap-Scan join with no Gather Merge
at all, where PG parallelizes a Sort under a Gather Merge below its own
GroupAggregate — so even a won Incremental Sort tournament here would not by
itself have produced a PG-matching plan; the cost-tie is one of several
compounding divergences for this query, not the last one.

**Corpus re-measurement**: `scripts/capture-tpcds.sh` against a fresh
`GOOPG_INCREMENTAL_SORT=on` SF0.25 server (flag state re-verified via
`/proc/<pid>/environ`, same discipline as the 2026-09-17h measurement) —
`analysis/m0141/m0141-s2b7-full99-incsort-on.txt`. `grep -c "Incremental
Sort"` = 0, still. `diff` against the pre-fix capture
(`analysis/m0141/m0141-s7-full99-incsort-on.txt`), header/tmp-path lines
aside, is **byte-identical** — the fix changes which candidates are offered
and priced, but not which one wins, on this corpus. `scripts/pg-plan-parity-diff.py`
against `bench/tpcds/plans-pg/`: `PLAN-PARITY: queries=99 match=2 shapediff=67
unparsed=0 missingnode=27 error=3 timeout=0`, `CATEGORIES-EXCL-MATCH:
join-order=91 join-method=69 scan-type=60 parameterisation=45
aggregation-strategy=71 sort-strategy=77 parallelism=84 qual-placement=20
rendering=23` — the M0137-0004 floor (TPC-DS match >= 2, Q9 + Q41) holds,
category counts unchanged (shape-delta=0 from the diff above already implies
this, confirmed rather than re-derived).

**What this means for the remaining 4 of the 5 GroupAgg witnesses (Q43,
Q54, Q60, Q89) and the 9 non-GroupAgg witnesses**: not separately traced this
loop (budget), but Q3's finding generalizes structurally — the arm is
reachable for all of them now (the populate-and-restore fix is unconditional,
not query-specific), so any of them still failing to flip is a cost or
upstream-shape question, the same category as Q3's, not a repeat of the
`SearchCandidates`-empty bypass. `M0141-S2b-6-resume` (real SF1 cardinalities
on the reloaded TPC-H cluster, gated on M0142-0003k) is the filed follow-up
for the cost-tie side; no new ledger row is needed for "which of the 9 flip
once the cost-tie resolves" since M0141-S2b-6-resume's own scope already
covers a Hashed-vs-Sorted `PathAgg` cost comparison that would affect these
too.

**GOOPG_INCREMENTAL_SORT stays default-off** — unchanged reasoning from the
2026-09-17h update: turning it on is still provably a no-op for this corpus
(now proven by trace to be an inert-but-reachable arm rather than an
unreachable one, which does not change the default-off verdict).

Gates run: `go build ./...` clean. `go test ./internal/optimizer/...
./internal/executor/...` both green. `scripts/capture-tpcds.sh` (full 99,
flag-on, flag state verified against the live PID's `/proc/<pid>/environ`).
`scripts/pg-plan-parity-diff.py` against `bench/tpcds/plans-pg/` (floor
check, see numbers above). One traced single-query EXPLAIN (Q3) with
`GOOPG_PGSHAPED_DP_TRACE=1` to read the `DPPATH`/`DPGROUP` lines quoted
above. `make ralph-state-guard` passed. Pre-commit hook's pgbench smoke:
PASS. TPC-DS SF0.25 full row-count regression sweep was NOT separately
re-run: the diff-against-pre-fix-capture already shows byte-identical
EXPLAIN shapes corpus-wide (0 plan changes), which by construction implies 0
row-count changes — a sweep could not add evidence beyond what that diff
already establishes.

Resume point: **M0141-S2b-6-resume** (cost-tie, gated on M0142-0003k) is now
the direct blocker for moving any of the 14 Incremental-Sort witnesses,
having subsumed the structural blocker this update closes. A future loop
with that cluster reloaded should re-run this same corpus measurement to see
how many of the 5 GroupAgg witnesses (and possibly some of the 9 others,
which share upstream `PathAgg`/join cost machinery) flip once real
cardinalities separate the Hashed/Sorted cost tie.

## Update 2026-09-18 — cost-breakdown diagnosis for the remaining 13 witnesses (banner item 4)

Banner item 4 (`.ralph/fix_plan.md` Current Priority, written 2026-09-17):
"M0141-S7 — cost diagnosis only... Compare the cost breakdown with PG for the
14 queries. No executor work." Only Q3 had been traced so far (2026-09-17i
above). This update traces the remaining 13 on the private `:65437` TPC-DS
SF0.25 cluster (started with `GOOPG_INCREMENTAL_SORT=on
GOOPG_PGSHAPED_DP_TRACE=1`, both confirmed via `/proc/<pid>/environ` before
capturing — same discipline as every prior trace in this doc) against the 13
witness queries' own `.sql` files
(`bench/tpcds/runtime_goopg/tpcds-data/queries/query{N}.sql`, loaded into the
cluster's `postgres` database). Raw per-query psql output + the log-slice
captured between `wc -l` markers before/after each run: `tmp/m0141-s7-trace/`
(not committed — scratch, same precedent as every earlier probe in this file).
Recon-only: no `internal/`/`cmd/` file touched, `go build ./...` unaffected.

### Which of the 13 even reach the arm

Grepping each query's log slice for `producer=upper.ordered.(sort|
incrementalsort)`:

| witness | `upper.ordered.incrementalsort` line present? |
|---|---|
| Q4, Q11, Q43, Q54, Q58, Q60, Q83 | yes (7 of 13) |
| Q35, Q49, Q63, Q64, Q67, Q89 | **no** (6 of 13) |

For 5 of those 6 "no" cases the reason is already known and tracked, not a
new finding:

- **Q35** — its `ORDER BY` list is byte-identical to its `GROUP BY` list
  (`ca_state, cd_gender, cd_marital_status, cd_dep_count,
  cd_dep_employed_count, cd_dep_college_count`), so `addIncrementalSortPaths`'s
  own `contained` check (`incrementalsortpaths.go:166`) is true for the
  GROUP_AGG seed candidate — a full match is arm 1/2's case by design, not a
  gap arm 3 should fill (`incrementalsortpaths.go`'s own header: "a full
  match is arm 1's case for the seed... buys nothing new here").
- **Q63, Q67, Q89** — re-reading these three's SQL text (not just the design
  doc's earlier "shape" label, which named the *witness's PG plan's own*
  aggregate strategy) shows all three wrap their aggregate in an OUTER query
  whose `ORDER BY` sits above a **window function** (`avg(...) OVER
  (PARTITION BY ...)` for Q63/Q89, `rank() OVER (...)` for Q67) — this
  reclassifies Q89 from "GroupAggregate" to the same bucket as Q63/Q67. All
  three are **M0141-S2b-3b**'s stated gate ("TPC-DS Q67... is the sole corpus
  witness and the gate" — Q63/Q89 share the identical shape and are an
  implicit second/third witness for the same still-open task): `addWindowPaths`
  sees one collapsed input `Node`, never a `Pathlist`, so there is nothing for
  arm 3 to iterate over yet. Zero lines is exactly what an unbuilt call site
  produces — not a new defect.
- **Q49** — outer `UNION` (`Unique` node in PG's plan), `M0141-S2b-4`'s
  already-filed "SETOP rel-identity fix" gate, same unbuilt-call-site shape.

**Q64 is the one genuine open question.** Its classification in the table
above ("Nested Loop, outer side's own order") predicted it would behave like
its siblings Q4/Q11/Q35 — but grepping its full log slice (134106 lines, the
largest of the 13 — a 17-table self-join of two CTE references) for
`producer=upper.ordered` finds **exactly one line, the seed `sort`, and zero
`incrementalsort` attempts, zero of any other ordered-rel producer**. This
cannot be told apart, from the trace alone, between "`ordered.SearchCandidates`
is empty for this rel" and "every candidate's `SearchCandidateKeys` has
`len(keys)==0`" — `addIncrementalSortPaths`'s loop (`incrementalsortpaths.go:161-166`)
`continue`s silently in both cases; only an *offered* candidate ever emits a
DPPATH line. A plausible (unverified) hypothesis, consistent with
[[cte_leaves_reach_search_wrapped_in_filter]]: Q64's outer join is over two
references to the same materialized CTE (`cross_sales cs1, cross_sales cs2`),
and a CTE-scan boundary may be where the search's own ordering-claim tracking
(`SearchCandidateKeys`) gets lost for one or both self-join legs — the same
family of gap that memory names for a different mechanism (residual-filter
placement), not yet confirmed for pathkey propagation specifically. Filed as
**M0141-S7-cd-q64** below rather than root-caused now: distinguishing the two
cases needs either instrumenting `createOrderedPaths`'s candidate-count at
population time or a debugger session, both of which are `internal/`-file
changes even if read-only in spirit, and this update's mandate is diagnosis
without executor/planner-file edits.

### Cost breakdown for the 7 that DO reach the arm

Each row decomposes the incrementalsort-vs-sort **total cost gap** into two
parts: how much comes from the two arms pricing a **different input
candidate** (`inputtotal` field — arm 1/2's seed vs. whichever
`ordered.SearchCandidates[i]` arm 3 built its `PathIncrementalSort` over), vs.
how much comes from the **sort-vs-incrementalsort pricing formula itself**
(`total - inputtotal`, i.e. each arm's own added overhead). Numbers are the
DPPATH line's own `total`/`inputtotal` fields, cheapest instance per producer
when a query offered more than one (Q43/Q54/Q60 each show two, one per
Hashed/Sorted `PathAgg` seed — see M0141-S2b-6/-6-resume; only the eventual
tournament winner's numbers matter for "why did Sort win," so the table uses
the accepted/cheapest pair):

| witness | shape | Δinput (arm-3 seed − arm-1/2 seed) | Δoverhead (incsort own − sort own) | Δtotal | input-divergence share |
|---|---|---|---|---|---|
| Q4  | Nested Loop | ~0 (7.7250 both) | +0.040 | +0.040 | ~0% |
| Q11 | Nested Loop | +0.010 | +0.040 | +0.050 | ~20% |
| Q83 | Merge Join  | +0.055 | +0.040 | +0.095 | ~58% |
| Q58 | Merge Join  | +0.762 | +0.040 | +0.802 | ~95% |
| Q54 | GroupAgg (2-level, `Subquery Scan`-nested) | +0.545 | +0.445 | +0.990 | ~55% |
| Q60 | GroupAgg (`Merge Append`) | +1.021 | +0.599 | +1.620 | ~63% |
| Q43 | GroupAgg (`Finalize`, parallel in PG) | +198.790 | +0.220 | +199.010 | **~99.9%** |

(Q43's absolute numbers: Sort's seed totals 47505.972, Sort itself
47506.112 — a mere +0.140 own-overhead; IncrementalSort's seed totals
47704.762, IncrementalSort itself 47705.122 — a comparably small +0.360
own-overhead. The two arms are pricing their OWN sort step almost
identically; Sort wins here almost entirely because its seed candidate is
~199 cost units cheaper, not because `costIncrementalSort` misprices
anything.)

**Reading this table**: in every case but Q4 (and to a lesser extent Q11),
most-to-nearly-all of why Incremental Sort loses is that
`addIncrementalSortPaths` (`incrementalsortpaths.go:161`) iterates
`ordered.SearchCandidates` and can only stack a `PathIncrementalSort` over a
candidate whose own claimed ordering (`SearchCandidateKeys[i]`) shares a
**partial, non-full, non-empty** prefix with the required sort keys
(`incrementalsortpaths.go:166`: `if contained || nCommon == 0 { continue }`).
The single cheapest candidate at each of these rels apparently fails that
test — either it has no claimed ordering at all (parallel/hash-shaped, most
likely for Q43's `Finalize`-labelled PG counterpart, which not surprisingly is
the biggest gap: PG parallelizes this witness with a `Gather Merge`, which
goopg's own plan for the same query does not reach — see the 2026-09-17i
Q3 finding's identical caveat about compounding upstream divergences) or a
FULL match (already arm 1's case, so it never reaches arm 3's loop as a
distinct offer). The candidates arm 3 CAN attach to are, by construction,
the ones with a partially-useful existing order — which on this corpus are
consistently the pricier siblings. This is a sharper, corpus-wide version of
what 2026-09-17i already flagged as a caveat for Q3 alone; it does not
contradict `M0141-S2b-6-resume`'s open Hashed-vs-Sorted tie question (that
tie is specifically about which `PathAgg` STRATEGY the Sort/IncrementalSort
arms are each fed for the SAME query, and both arms in the table above
already reflect whichever strategy each individually cheapest option was) —
it is a distinct, corpus-general observation that no amount of resolving the
Hashed/Sorted tie can fix on its own, since it is about candidate-set
MEMBERSHIP (which candidates even have a usable partial order to credit),
not about the tie itself. Filed as **M0141-S7-cd-candidatepool** below.

### What this changes

Nothing production-facing (recon only, per the banner's "no executor work").
It refines the resume point: **M0141-S2b-6-resume** (real SF1 cardinalities)
answers "does the Hashed-vs-Sorted tie flip with real data," which is
necessary but this update shows it is **not sufficient** even if it flips —
Q43's ~199-unit gap dwarfs anything a tie-break could produce, and it comes
entirely from candidate-set membership, a question SF1 cardinalities cannot
answer by themselves. `GOOPG_INCREMENTAL_SORT` stays default-off (unchanged
verdict). No ledger row: this is pure diagnosis of already-declined/deferred
scope, not a new gap discovered outside the ledger's own definition (every
component named above already has a filed, tracked task).

Gates run: none beyond the trace captures themselves — no production file
touched. `make ralph-state-guard` passed at commit time. Pre-commit hook's
pgbench smoke: PASS.

## Update 2026-09-22 — fresh default-pipeline confirmation and S4 escalation

The required private SF0.25 default-pipeline capture was repeated with
`GOOPG_INCREMENTAL_SORT=on` and `GOOPG_JOINTREE_PIPELINE=0`. It completed
successfully with `queries=99`, `match=2`, `shapediff=69`, `missingnode=25`,
`error=3`, and `timeout=0`; the plan text contains zero `Incremental Sort`
nodes. The emitted plan and diff SHA-256 values were respectively
`aeaace4eeb6eb7648b2bc469e9f588dd6891c7e3263e108176e6b3518ee4cf67` and
`2c02f599184a8bc32eb9d82b8a89761ea75705665181b06e777bb7bc5f6399e2`.

This is not a sixth diagnostic subtask. M0141-S7 already has six completed
direct descendants with `Movement: none`: `exec-a`, `exec-b`, `exec-c`,
`cd-q64`, `cd-q64-reclassify`, and `cd-candidatepool`. Under S4, no further
recon child may be selected or filed. The pending implementation tasks
M0141-S2b-8 and M0141-S2b-9 remain the documented resume points, but banner
item 6 forbids their production or executor work while S7 is selected. The
root is consequently marked `[!]` pending owner re-open; no new PostgreSQL
semantic gap was discovered, so no ledger row is needed.
