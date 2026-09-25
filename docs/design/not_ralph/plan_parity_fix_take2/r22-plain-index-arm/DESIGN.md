# R22 — unconditional plain-index-scan arm

*Round of `docs/design/not_ralph/plan_parity_fix_take2/TODO.md` (the R22
row). Status: DESIGN + self-review (subagent delegation unavailable —
recorded honestly in §5, not elided). **OUTCOME: DECLINED, see
REPORT.md** — implemented, pinned, probed (145 offers / 0 survivals),
reverted: a full-fetch index scan is strictly dominated by the seq
scan on both cost axes whenever a seq path exists (always), so the
arm can never win. Follow-up: none filed (ordered-arm contest belongs
to K24/R21-slice-3, already owned).*

## 0. Problem (§7.3, evidenced — not inferred)

PG uses a condition-less full index scan where goopg seq-scans (measured
on live :65432 vs :5547, GUCs pinned):

- Q5: PG `Index Scan using nation_pk on nation` (25 rows) vs goopg
  `Seq Scan on nation`;
- Q8: PG `Index Scan using nation_pk on nation n1` vs goopg Seq Scan
  (same for n2).

goopg CAN build that shape (`createIndexScanPlan` rebuilds a full index
scan from an empty `IndexClauses`, M0127-P5.5-b) but never OFFERS it
without an ordering: `addOrderedIndexPaths` declines the rel at the
`hasUsefulPathkeys` gate (`pathindexordered.go:114`), and
`addOneOrderedIndexPath` declines when the useful-column set is empty.
`Kind: PathIndexScan` is constructed at only three non-test sites —
the narrow structural gap root-causes §2 names.

## 1. Oracle (`build_index_paths`, indxpath.c:750-800)

PG builds, per index, a PLAIN path (no pathkeys) PLUS pathkey variants,
and `add_path` prunes. The plain path is a full scan when no
restriction clause matches (`pathnodes.h:1817` — the exact object
goopg's ordered arm already builds, minus the ordering). This round
transcribes the plain arm ONLY:

- pathkeys NIL, `IndexClauses` EMPTY, `RequiredOuter` 0,
  selectivity 1.0 (full heap fetch);
- costed by `costIndexScan` with `numQualOps` = all local conjuncts
  (R1 currency — a full scan filters everything on the heap);
- partial twin via the existing `addPartialIndexPath` (PG builds
  partials for plain paths too, indxpath.c:1039-1062).

Explicitly NOT this round: matching restriction clauses as index quals
(PG's `match_clauses_to_index` for non-parameterized quals — a WHERE
`col = const` becoming an indexqual). goopg's search has no such
producer (selectivity is always 1.0 outside parameterised probes), and
that is a separate, larger item — filed as follow-up, not bundled.
The gate being dropped is admission, not matching.

## 2. The change (one producer, same loop)

In `addOrderedIndexPaths`' per-rel loop, for each index (after the
existing `tbl`-nil and `scanLeafFor` eligibility gates, which stay):

1. `addOnePlainIndexPath(rel, tbl, idx, relPages, relTuples,
   totalPages)`: orderability NOT required for a plain path — but
   declined for `USING hash` (follows the partial twin's btree-only
   gate; goopg's executor IndexScan has no backward/hash mode to
   select, and PG's hash-AM plain paths are a filed follow-up, not
   this round) and for `idx.HasPredicate` (same unproven-partial
   decline as the ordered arm — no predicate-implication prover).
   Files `PathIndexScan` with `Pathkeys: nil`, empty clauses,
   `RequiredOuter: 0`, `Rows: rel.Rows`, costed serial + partial twin
   (`"index.plain"` / `"index.plain.partial"` producer tags).
2. The existing ordered arm runs ONLY if `hasUsefulPathkeys(rel)` —
   byte-identical behaviour on that path (same inputs, same outputs).

`setCheapest(rel)` runs if either arm added. No other caller changes;
no producer deleted; the gate function itself is untouched (still
governs the ordered arm).

Why the plain path cannot corrupt the search: a nil-pathkeys path is
incomparable with keyed paths in `addPath` (same rule that keeps two
merge orderings — established by the OnePathPerSortKey test), so it
competes only against the seq scan and other unkeyed paths. Worst case
it loses everywhere (then the round is a measured no-op with the
candidate set complete); it cannot evict an ordering anyone consumes.

## 3. Gates

- Unit: (a) plain path offered for a rel failing `hasUsefulPathkeys`
  (no join clause, no query pathkeys) — the gate's falsification;
  (b) `Pathkeys` nil, clauses empty, `RequiredOuter` 0;
  (c) cost identity with an equivalent full-scan pricing (same
  `indexScanInputs` as the ordered arm minus keys → same total);
  (d) partial twin filed with worker/divisor behaviour identical to
  the ordered twin's; (e) `HasPredicate` declined; (f) hash-AM
  declined; (g) ordered arm byte-identical with gate passing
  (existing tests cover — must stay green).
- Suites: optimizer + executor green (pre-commit bar).
- Values: TPC-H digest 24/24 MATCH vs pre-round arm; TPC-DS SF0.5 sweep
  PASS=95 all-zero.
- Parity (subject): shape verdicts both corpora, judged by CATEGORY
  movement (roadmap). Candidates for movement: Q5/Q8 nation sites
  (scan-type) and any tiny-dimension full-index win. Direction
  recorded; a zero-move round with the candidate set complete is an
  allowed outcome (stated in advance, not discovered after).
- Timing table reported, not adjudicated; timing move on unmoved plan
  fails the round.

## 4. Known remainders (filed, not bundled)

- Restriction-clause→indexqual matching (the real PG plain-path
  power): separate item.
- Hash-AM plain paths: filed (executor has no hash/backward scan
  mode).
- If Q5/Q8 do not move: suspect is cost (R1 qpqual × full fetch makes
  full index scans dear — correctly per PG) or width model (goopg
  nation width 490 vs PG 34 — a DIFFERENT gap, not this round's).

## 5. Review record

Subagent delegation unavailable (Task cancelled at R0; TODO.md log).
Review as adversarial second pass by the author:

- **Source pass** (at HEAD): the gate (`pathindexordered.go:114`),
  the keys-empty decline, and the three non-test `PathIndexScan`
  construction sites re-verified by grep; `addPath` pathkey-
  incomparability confirmed via the OnePathPerSortKey test (not
  assumed); `addPartialIndexPath` signature confirmed reusable
  (takes serial path + inputs — the plain arm supplies both).
- **Oracle pass**: `build_index_paths` plain-vs-pathkey structure
  re-read; `pathnodes.h:1817` confirmed as the already-transcribed
  object; partial-for-plain confirmed at indxpath.c:1039-1062.
- **Correction applied by review:** first draft dropped the gate for
  BOTH arms (one loop, no condition). Falsified: the ordered arm's
  gate is load-bearing documentation of WHEN ordering paths are
  worth building, and its removal would rebuild the per-index loop
  for rels whose ordered paths can never be consumed — churn with no
  parity effect. The gate stays for the ordered arm; only the plain
  arm is unconditional. Second correction: first draft included
  hash-AM indexes ("PG builds plain paths on them too") — removed:
  an unordered full scan over a hash-organised index offers no shape
  the btree plain path does not already offer (same node shape, same
  heap economics), so including it only doubles candidates for zero
  new shapes. Filed, not built.
