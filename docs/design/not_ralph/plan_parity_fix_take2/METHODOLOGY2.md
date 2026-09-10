# Plan parity, recounted at R50 Slice A — methodology note 2

*Date: 2026-09-10. Tree: `dcd960c` (R50 Slice A) in `/tmp/wt-r49b`,
branch `plan-parity-with-pg-take2`. This note supersedes the numeric
tables in `METHODOLOGY.md` §2 and `ROADMAP-to-all-match.md` §1–2; it does
not replace them — the method (§4 pipeline) still stands, with the
corrections in §7 below. Round status stays authoritative in `TODO.md`.*

## 1. Fresh verdicts (measured, not carried forward)

Captured 2026-09-10 with the §4 pipeline on private clones (`:5533` DS05,
`:5534` TPC-H, pinned `work_mem='64MB'`,
`max_parallel_workers_per_gather=4`, `GOOPG_ANALYZE_SEED=20260905`),
goopg binary built at `dcd960c`, PG 18.3 references live on `:65432`
(db `tpch`, user `tpch`) and `:65438` (db `tpcds05`, user `ryo`).
Diff tool `scripts/pg-plan-parity-diff.py` is byte-identical to the R43
baseline run (`git log --since=2026-09-09 --` on the tool returns empty —
verified 2026-09-10), so every delta below is a tree movement, not
metric drift. Tree verified at `dcd960c` with a clean `git status`
save for this file. Evidence: `/tmp/pp2/rec-{tpch,ds}-diff.txt`
with normalized captures alongside.

| corpus | match | shape-diff | unparsed | missing-node | error |
|---|---|---|---|---|---|
| TPC-H (22) | 2 (Q6, Q13) | 20 | 0 | 0 | 0 |
| TPC-DS (99) | 1 (Q9) | 71 | 0 | 24 | 3 |

The 3 TPC-DS errors are Q36/Q70/Q86 — unplannable on both engines, out of
scope per METHODOLOGY §9. TPC-DS Q9 (the R46 legacy-funnel win) still
matches; nothing regressed to ERROR or UNPARSED anywhere.

## 2. Blocked-per-category, recounted

Same metric as before (tool `CATEGORIES` line = queries whose headline
carries the category). Two baselines, named because they differ: TPC-DS
deltas are vs `ROADMAP-to-all-match.md` §2 (2026-09-09, post-slice-2b:
95/89/81/79/72/72/42/35/13); TPC-H deltas are vs the R43 HEAD recount in
`TODO.md` §R43 (post-R41/R42: 18/12/13/6/10/13/18/7/7 — note this
already differs from ROADMAP's H row 17/12/14/6/10/13/16/6/7, the
R41/R42 eligibility admissions adding shapediff queries).

| category | TPC-DS (of 99) | Δ | TPC-H (of 22) | Δ |
|---|---|---|---|---|
| **join-order** | **95** | 0 | **18** | 0 |
| parallelism | 89 | 0 | 17 | −1 |
| aggregation-strategy | 70 | **−11** | 8 | −2 |
| sort-strategy | 80 | +1 | 12 | −1 |
| join-method | 65 | **−7** | 11 | −1 |
| scan-type | 72 | 0 | 14 | +1 |
| parameterisation | 35 | **−7** | 6 | 0 |
| rendering | 35 | 0 | 9 | +2 |
| qual-placement | 9 | **−4** | 2 | **−5** |

Net: 29 TPC-DS query-category blocks and 10 TPC-H blocks closed since R43,
with 4 sideways moves (DS sort +1; H scan +1, rendering +2). join-order did
not move on either corpus — still the dominant, still untouched.

Per-round attribution (read 2026-09-10 from the `TODO.md` ledger and
round reports — `TODO.md` records category deltas only for R44 and R46;
R47/R48/R49/R50 reports list flips and line censuses, never category
counts, so everything below marked "consistent with" is inference from
flip/census content and direction, not a ledger quote; per-query
archaeology between the R43-era and current captures would promote
each to a quote):

- **R44 (const-fold before selectivity, estimate round):** DS
  join-method 71→66, parameterisation 39→35, aggregation 84→82
  (−10 DS categories total); H parallelism 18→17, qual 6→5,
  sort 13→12 (−3 H). Ledger-quoted; carries the bulk of the DS
  join-method and parameterisation movement.
- **R45:** REJECTED on review (architecturally impossible — Partial
  emits zero rows; K97). No landing, no movement. Recorded so the
  zero is explained, not missing.
- **R46 (legacy funnel index-vs-seq):** DS Q9 → MATCH (first TPC-DS
  match of the programme; ROADMAP 0/72 → now 1/71 via the Q9 win —
  R46's own baseline read 0/70, drift per below — missing-node 24
  and error 3 unchanged throughout); scan-type −1 on its own
  baseline. TPC-H categories identical.
- **R47 slice 2 (ordered-level loop, K101):** 16 flips toward PG, ZERO
  EXTRA — H Q7+Q8 (`Sort → HashAggregate` to `GroupAggregate →
  Sort(input)`) plus 14 TPC-DS stations. Consistent with the H
  aggregation-strategy −2 and the DS −11 remainder after R44's
  ledger-quoted −2 — prime suspect, not quote.
- **R48 (semi JoinQual placement + `Filter: (true)` drop):** 40 stray
  `Filter: (true)` lines → 0 (6 H / 34 DS, EXPLAIN-only) + 9 TPC-H
  semi-residual lines moved join→inner-probe (PG placement). Prime
  suspect for the H qual-placement movement; DS byte-identical on
  half 2.
- **R49 Slice B (bitmap-heap NLI probe parameterisation):** probe
  clauses join→probe, moves-only — TPC-H 14 / TPC-DS 40 lines.
  Prime suspect for the DS parameterisation −7 remainder after
  R44's ledger-quoted −4 (H parameterisation net 0).
- **R50 Slice A (bpchar hash-safe admission):** 28 in-scope lines
  touched (net census 64→42, delta 22 — the remainder shrunk rather
  than vanished: Q4 5→2, Q11 3→1, Q74 3→1, Q58 drops `item_id`
  only), ZERO other text diffs, HC-extra held at zero. Prime
  suspect for the DS qual-placement −4 remainder alongside R48.

**Honesty ledger — what does NOT attribute cleanly:**

- The ledger's inter-round numbers are not mutually commensurable.
  R44-start DS (71/39/84) already differs from ROADMAP DS (72/42/81),
  and in-window scope changed: the R41/R42 eligibility admissions
  (3 queries) landed between them, so the DS drift may be admission
  scope as much as harness drift. A second unflagged drift: R44's H
  qual start (6) vs the R43 HEAD recount (7). Further cf. R46's
  baseline `match=1/19` vs R43's `match=2/20` (recorded as
  discrepancy) and K91's stub-capture correction. That is why this
  note anchors on ONE fresh recount instead of chaining round
  deltas; chaining would compound the drift.
- The four sideways moves are unassigned: DS sort-strategy +1, H
  scan-type +1, H rendering +2 (of which +1 is the matched Q13's
  absorbed `[rendering]` tag — §3 — leaving +1 genuine). Each needs
  a named query via per-query archaeology between the R43-era and
  current captures before this table is final; the prime suspects
  are headline-tag side effects of the R47/R49/R50 moves (a move
  toward PG can add a tag elsewhere in the same headline), not
  regressions — nothing regressed to ERROR or UNPARSED anywhere.
- H join-method −1 has no ledger owner (R44's H total lists only
  parallelism/qual/sort; the gather-paths flip R43 measured was
  never landed). Same treatment: name the query before claiming
  the cause.

## 3. What the recount clarified about the metric itself

- **A MATCH can carry a category tag.** TPC-H Q13 matches with
  `[rendering]` (a `strip-pg-Hash` normalisation nit the verdict absorbs).
  So `rendering=9` overstates "blocked" by exactly one: 8 shapediff queries
  plus the matched Q13. Blocked-counts are verdict-headline counts, and a
  headline is not quite a block. Future recounts should report
  `blocked-excluding-matches` alongside the raw tool line.
- **The "4–7 at once" conjunction now has low-end exceptions — that is
  progress, not refutation.** Category-count distribution over shapediff
  queries at HEAD: TPC-H `{1:1, 2:1, 3:1, 4:5, 5:5, 6:4, 7:3}`,
  TPC-DS `{3:5, 4:7, 5:16, 6:21, 7:15, 8:7}`. Zero queries are blocked by
  join-order alone (unchanged), and no single fix flips any query — but
  TPC-H now has genuine nearest-misses where R43's ranking method applies:
  Q14 `[parallelism]` alone, Q1 `[sort-strategy, parallelism]`. TPC-DS has
  none below 3 (narrowest: Q26/Q28/Q41/Q84/Q96 at 3).
- **Q14 is still the milestone, with a narrowed verdict.** One category
  from matching, unchanged since R43 — but K92 closed the cheap route:
  neither the gather-paths flip (orthogonal, K82) nor relabelling the
  leader-prebuild as `Parallel Hash` (misdescription) converts it. Its
  conversion remains the event that proves the conjunction breaking,
  and it now prices as an executor programme, not a planner flip.

## 4. New categories: considered, none added

Three candidates were proposed since R43; all three stay subsumed, for
stated reasons. The bar for a tenth category is: a divergence class that
(a) appears in ≥3 queries, (b) is not a mechanism under an existing
category, and (c) would change round selection if tracked. None clears (b).

- **`Parallel Hash` (K79).** PG uses `Parallel Hash Join` in 69/99 TPC-DS
  and 7/22 TPC-H; goopg emits it zero times. But R43 measured the
  `GOOPG_GATHER_PATHS` flip moving TPC-H `parallelism` 18→15 with no
  parallel-hash work at all — the absence already scores inside
  `parallelism`. A standalone node-kind category would double-count it.
  Keep under `parallelism`; the lever is the gather-paths flip
  (R10 measured TPC-H `parallelism` 18→15 with no parallel-hash work
  at all; R43 rev 3 re-measured 18→16 at HEAD), now unblocked.
- **Join-implied equalities (K26).** The measured join-order cause
  (goopg's seam withholds transitive `a = c` from the DP search; PG
  synthesises them via EquivalenceClasses) is candidate generation, i.e.
  squarely `join-order`. The next step is even bounded: one helper,
  `nliIn` vs the `*NestedLoopIndexJoin` inner shape implied equalities
  produce (K26 §8 refuted the "broad reshaping" fear — 3 test failures,
  not a corpus). No new category; the obstacle is named and small.
- **Hash-`Join Filter:` duplication (R50).** Same-node HC+identical-JF is
  scored where the tool puts Filter-text divergence:
  `qual-placement`/`rendering`. Slice A moved exactly those two cells and
  nothing else (HC-extra held at zero by the gate), confirming the fit.

## 5. Challenges toward the goal, re-organised

Ordered by queries unblocked, with the current named obstacle each:

1. **join-order (95/18, untouched).** Cause measured (K26); next step
   bounded (`nliIn` helper). This is the critical path — every other
   category can fall to zero and no query matches without it, since zero
   queries are blocked by anything alone.
2. **parallelism (89/17).** Two independent tracks, kept separate per
   K82/K92: (a) the gather-paths flip (R43 rev 3 measured TPC-H
   parallelism 18→16 with no new match — worth landing on the category
   metric, still unlanded behind the R13 adjudication scope); (b) Q14,
   the one-category nearest miss, whose last gap is NOT closed by the
   flip (byte-identical under it — K82) and NOT by relabelling
   (K92: goopg's leader-prebuild is not PG's partial-inner model, so
   stamping `Parallel Hash` would be misdescription). Q14 needs the
   real executor programme (workers building a shared hash from a
   partial inner), comparable in size to R45's rejected item. Beneath
   both tracks sit two non-planner caps: K14 (`character(N)` padding /
   heap fill — storage, not planner) and K15 heap-density
   worker-count effects.
3. **aggregation-strategy (70/8) + sort-strategy (80/12).** R47 proved the
   mechanism moves (16 flips, zero extra); remaining work is the firing
   rule and coverage, not discovery.
4. **join-method (65/11) / scan-type (72/14).** Partly cost-currency (the
   C-19 index-vs-seq asymmetry, root-causes §3) and partly candidate
   generation (plain-index-scan arm, `hasUsefulPathkeys` gate). Estimates
   are 3–5 orders out both ways and the verdict is structurally blind to
   them (METHODOLOGY §"BLIND") — so cost work must be judged by
   shape-delta + categories, never by estimate movement alone.
5. **Eligibility floor.** TPC-DS seam declines remaining: 5
   (`outer-over-derived` 3 on CTE-stats work, `outer-spine` 2). Declined
   queries cannot converge by costing work (K27) — track declines as the
   second axis per METHODOLOGY §3, or category wins will be read as
   broken promises.
6. **Metric hygiene.** Two corrections applied in this recount and now
   mandatory: PG arms need their documented credentials (not `postgres`),
   and Q15a needs an explicit EXPLAIN (`/tmp/parity-r0/q15a.sql` lost its
   prefix — spliced here with provenance, but the working copy should be
   fixed). Report `shape-delta.sh` alongside categories every round; a
   shape change with no category movement is sideways until named.

## 6. Provenance of this recount

- Captures: `/tmp/pp2/rec-goopg-{tpch,ds}.txt` (`.norm` = section-
  normalised), `/tmp/pp2/rec-pg-{tpch,ds}.txt`; diffs
  `/tmp/pp2/rec-{tpch,ds}-diff.txt`; Q15a splice sources
  `/tmp/pp2/q15a-{goopg,pg}.plan` via `/tmp/pp2/q15a-explain.sql`.
- GUCs pinned in-session per METHODOLOGY §4.1; serving binaries verified
  by inode (`launch-verified.sh`); PG references verified live, never
  restarted.
- Values gates were NOT re-run for this note (no code changed since
  `dcd960c`, whose digest 24/24 and sweep 95/0 stand); a recount after a
  code change must re-run them per §5.
