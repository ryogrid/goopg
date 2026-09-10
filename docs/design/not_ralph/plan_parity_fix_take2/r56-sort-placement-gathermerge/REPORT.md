# R56 REPORT — §2 worker-sort-under-GatherMerge arm (2026-09-11)

Date: 2026-09-11. Scope: `SCOPE.md` §2 ONLY (third no-split upper
arm; F3 procost, AGG_MIXED, join-rows N-lead, §3 tie-break, Q8's
+25k gap all ledgered and ordered after, unchanged).
Tree @ `31c7ed3ca` + uncommitted cut: `partialaggupper.go` third
arm (+59) + `upperorderedgrouping.go` GatherMerge translation
(+17/−3) + `parallel.go` Sort-through arms + P7 guard (+84/−11) +
`cte_inline_pushdown.go` GatherMerge passthrough (+34/−1) + 4
touched tests (`partialaggupper_test.go` +145 traced-decomposition
pins, `upperordered_test.go` +37 elect coverage,
`cte_inline_pushdown_test.go` +70 GatherMerge-crossing pin,
`q8_subquery_scope_posmap_test.go` +10 finder arms) + this report.
Binaries:
`/tmp/pp2/bin/goopg-r56` (R56 cut pre-Q78-fix) and
`/tmp/pp2/bin/goopg-r56q78fix` (plus the CTE-pushdown arm —
same tree otherwise). Comparator: R55 q7fix baselines in
`/tmp/pp2/r54redesign/` + live PG 18.3 :65432 (read-only).
Same capped clones and GUCs throughout (`/tmp/pp2/clone-tpch` +
`/tmp/pp2/clone-ds05` :5533/:5534, `work_mem=64MB`, `mpwg=4`,
`GOOPG_ANALYZE_SEED=20260905`, fresh server per phase, every
query ×2 run-stable). Evidence (tmp-only, NOT committed):
`/tmp/pp2/r56/` (`pp-r56.txt`, `pp-pg-live.txt`, noq15/meas8
derivatives, values digests, `start-*.log` DPPATH traces,
`q78fix-*` logs, `ds-unfixed.txt`/`ds-fixed.txt` DS channel
A/B) + `/tmp/q78-clone-{base,r56}.txt`, `/tmp/q47-clone-*.txt`.

## 1. Exit table (SCOPE §3 prediction vs measurement)

| item | R56 | verdict |
|---|---|---|
| pk=3 rival (traced `producer=upper.groupagg.gathermerge … rows=2 … total=149461.93`) | **149461.93, inside [149350,149550]** | **✓ LANDS (reassembly ≈149460; margin 382→~193 DOWN, no flip — the scope-allowed outcome)** |
| Q7 winner | gathered 149268.72 stays | **✓ no flip at unfaithful N; N-lead promoted with a number (residual ~193)** |
| Q7 root plan | byte-identical to R55 (GM offered as ordered input, dominated — elect restored, see §2) | **✓** |
| Q7/Q5/Q1/Q8/Q9/Q10/Q19 pins | top byte-identical to R55; Q5 split still wins; spot1213 Q12=2/Q13=34 | **✓** |
| Q3 | Sort→Gather (233350.75) replaced by GatherMerge→Sort (216851.72); join subtree byte-identical; values md5 MATCH | **✓ ACCEPTED with C-19e-q16 precedent (dossier §3; review decides)** |
| TPC-H values 8/8 | md5 MATCH | **✓** |
| parity meas8 (identical live-PG capture) | verdict lines identical category-for-category except Q3's goopg estimates line; unparsed=0 both pairs | **✓** |
| DS SF0.5 sweep (unfixed binary) | **PASS=95 (57 ck) MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0 SKIP=4**; verdict-changes=none | **✓ §4 must-hold** |
| DS plan channel (R55→R56) | changed (9): Q4 Q11 Q31 Q47 Q51 Q57 Q59 Q78 Q97 | **✓ toward-PG (§4; Q78 contained a defect — §5)** |
| Q78 pushdown fix | `Filter: (d_year = 1998)` restored on all three date_dim scans; GroupAgg rows 269574→549 / 684176→1395 / 1425140→2906; Limit rows 100→7; shape GM retained | **✓ single root cause, §5** |
| DS plan channel (unfixed→fixed) | changed (1): Q78 only | **✓ blast radius exactly the defect** |
| DS SF0.5 sweep (fixed binary, foreground re-run) | **PASS=95 (57 ck) all-zero**; verdict-changes=none; plan channel vs unfixed changed (1): Q78 | **✓ gate re-greened on the shipped tree** |
| units | optimizer + executor green; `go vet` clean | **✓ (gate runs without `-count=1`)** |

The prediction lands 149461.93 against the [149350,149550] bar:
inside, near the reassembly, direction DOWN. A landing above
149651 would have FAILED the tournament model; it did not
happen. No flip past gathered — at N=5874-vs-2520 a flip would
have triggered re-audit, not celebration.

## 2. What the cut does

- `partialaggupper.go` third no-split arm: `GroupAgg →
  GatherMerge → Sort → pseed`. Worker Sort priced at
  per-worker rows through `costSortRun` (via
  `sortPathForBounded` over the per-worker seed — the whole
  saving: N sorts of R/N rows vs one of R log R; `ParallelWorkers`
  set explicitly since the sorter prices but never plans
  workers), boundary through `gatherMergeCost`, upper through
  the SORTED arm of `costAgg` — all existing functions, no new
  constant. Filed `Rows: finalGroups`, identical to the
  hashed/sorted siblings. Manual `PathGatherMerge` (copies
  subpath Pathkeys; `ParallelSafe: false`) because
  `makeGatherMergePath` only wraps already-sorted subpaths
  while this arm builds the sort itself (C-19e's
  `createPartialSortPaths` pattern, mirrored field for
  field). Producer `upper.groupagg.gathermerge`; `setCheapest`
  adjudicates.
- `upperorderedgrouping.go` companion (found pre-compaction by
  the Q7 root-cost delta): `groupingEmissionPathkeys` accepts a
  `PathGatherMerge` child as well as `PathSort` — a merge
  emits its inputs' order, the same order-delivery contract a
  Sort gives (PG iterates Gather Merge inputs in
  `create_ordered_paths` the same way). Without it the new
  candidate evicts the leader-sort candidate under identical
  pathkeys and `electOrderedGrouping` loses its only
  translatable candidate, declining to a legacy-priced ORDER
  BY seed. Positional group-key coverage check unchanged.
- `parallel.go` Sort-through arms (the four walks agree):
  `drivingScan` sees through `*Sort` (per-worker sorts, P7);
  `stampParallelScan`/`unstampParallelScan` mirror it
  (copy-on-write; the stamp arm is REQUIRED — `gatherChildPlan`
  refuses a worker subtree with no driving scan, so without it
  the R56 upper arm could never build); `findPartialSubtree`'s
  bottom-out rule gains `drivingScanCrossesSort` so a Sort on
  the spine resolves to a GatherMerge boundary, never a
  concatenating Gather over per-worker sorts
  (`TestNoPlainGatherOverWorkerSort` pins the forbidden shape).
- Sibling-path audit (§4 in-loop): the arm's three terms ARE
  the three call sites (`costSortRun`, `gatherMergeCost`,
  `costAgg` SORTED) — no forked arithmetic to drift.

## 3. Q3 flip dossier (for review)

Join subtree byte-identical; the move is Sort→Gather at
233350.75 replaced by GatherMerge→Sort at 216851.72,
seq-driven (Parallel Seq Scan on lineitem). Values md5 MATCH.
Precedent: C-19e's q16 (same asymmetric resolution, accepted).
Mechanism (clone trace): fractional/startup election under the
top LIMIT — the same startup-vs-total shape that elects the
PG-oracle moves in §4. No new constant participates; the
rival is priced end-to-end by existing PG-faithful terms.

## 4. The 9 DS moves are toward PG

PG oracle `bench/tpcds/plans-pg/Q47.txt` is GroupAggregate →
Gather Merge → Sort (Workers Planned: 3) — EXACTLY the new
shape; Q51/Q59/Q97 oracles also show Gather Merge over Sort.
Q4/Q11/Q31/Q59 roots move DOWN (GroupAgg→GM→Sort over a newly
parallel join); Q47/Q51/Q57/Q97 trade HashAgg→Gather for
GroupAggregate→GatherMerge→Sort at slightly HIGHER displayed
total (+0.5–0.8%) — the fractional/startup election under top
LIMITs (Q47 Limit rows=1; clone trace: GM startup=14388.85 <
gathered startup=14721.34 while totals 14860.96 > 14760.76).
Direction adjudicated: toward the oracle, parity wins.

## 5. In-loop defect: Q78 lost its pushed qual (FOUND, FIXED, PROVED)

The R56 plan put `GroupAggregate → Gather Merge → Sort` over
Q78's three single-reference grouping CTEs — and all three
`date_dim` scans lost `Filter: (d_year = 1998)` (rows
149→73049, GroupAgg rows 549→269574), a qual-placement
divergence from PG on an otherwise PG-shaped plan. Sweep
checksums still passed (copy semantics: the residual outer
`ss_sold_year/ws_sold_year/cs_sold_year = 1998` filters select
the same rows — the defect cost estimates, not results).

Root cause (single, proved): `pushConjunctIntoCTEBody`
(`cte_inline_pushdown.go`) had no `*GatherMerge` arm, and
`pushConjunctTraced` has none either — the merge boundary
fell through to fail-closed decline. The estimators were
EXONERATED by inspection AND by the fix's numbers: every
walker (`EstimateRows`, `relFilteredRowsWalk`,
`resolveBaseColumn`) delegates GatherMerge; the filed PathAgg
carries `Rows: finalGroups` like its siblings; the group-rows
inflation was the skipped Yao/Dell'Era term (`filtered ==
tuples` when the pushed filter is absent → product saturates
to the closing input-rows clamp). A/A stability
(date_dim 149/149/149 on two fresh :5534 servers, same
binary) proved the 73049 non-noise before the mechanism was
known.

Fix: transparent `*GatherMerge` passthrough in
`pushConjunctIntoCTEBody` (mirrors `*Sort` exactly — a merge
republishes its child's schema unchanged and merges without
reordering; copy semantics only, residual kept, never a move)
plus the `*Filter`-over-`GatherMerge` HAVING-position entry.
`*Gather` deliberately excluded, as scope containment rather
than a soundness verdict: a Gather republishes its child's
schema unchanged too, so crossing it would be equally sound —
but below the top Aggregate only no-split shapes arrive in this
round (the `*Aggregate` arm declines Partial/Finalize splits),
and a Gather deeper in the join tree is
`pushConjunctIntoSubtree`'s territory — the same boundary the
`*Sort` precedent draws. General-path Gather/GatherMerge
crossing there is a separate round with its own proof (§6).

Proof, three legs: (1) new `TestCTEBodyPushCrossesGatherMerge`
(constructed Aggregate→GatherMerge→Sort→SeqScan body; pushed
Filter lands on the leaf with LeafLocal, residual duplicated,
idempotent); (2) live clone A/B — fixed binary restores all
three `Filter: (d_year = 1998)` lines, GroupAgg rows
549/1395/2906, CTE-scan rows 2/6/14, Limit rows 7, with the GM
shape retained and GroupAgg costs DOWN vs base (worker-sort
saving now visible against filtered input); (3) DS plan
channel unfixed→fixed: changed (1), Q78 only — blast radius
exactly the defect, the other 8 moves untouched.

TPC-H is unaffected by the fix — scope: the fix, not the whole
R56 cut (§3's Q3 move is the cut's, and measured). By mechanism
AND by corpus check: the pass only fires on Filter-over-CTEScan
pairs, and the measured TPC-H corpus holds no CTEs at all
(`grep -ci cte` over the identical live-PG capture pair:
`pp-r56.txt` 0, `pp-pg-live.txt` 0 — checked 2026-09-11). So
the fix no-ops on TPC-H by construction; units green, no TPC-H
re-run needed.

## 6. Residuals (ledgered, ordered after — unchanged by this round)

Join-rows N-lead (5874 vs 2520, now with a residual ~193
number); §3 tie-break calibration; F3 procost; AGG_MIXED;
Q8 +25k gap (winner immobile — split still wins, must-held).
New: general-path Gather/GatherMerge crossing in
`pushConjunctTraced` (join-clause pushdown into parallel
subtrees — separate round, wider blast radius).
