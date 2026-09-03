# REVIEW — take3 agent review record (2026-09-03)

Three independent reviewers audited this bundle before it was recorded. Each
worked from the source rather than from the documents: the PostgreSQL reviewer
against `./postgres/` read-only, the goopg reviewer against the Go tree at HEAD
`b4e68c574` (docs re-verified at `d5f8a6ff9`), and the design reviewer against
the recorded history of the take2 round.

Their raw reports are kept as dotfiles beside this one — `.review-pg.md`,
`.review-goopg.md`, `.review-design.md` — because a summary of a review is not
a review, and the next round should be able to read what was actually said.
All three returned APPROVE-WITH-CHANGES; every fix below was re-verified
against the documents by grep/read in this pass (docs edits only, no code, no
servers, no tests). A fix listed here cites the line that carries it; anything
not found is listed under Unresolved instead of claimed.

## Coverage

| reviewer | scope | verdict | findings |
|---|---|---|---|
| PostgreSQL accuracy | 01, 02, 03 | APPROVE-WITH-CHANGES | 0 blocker · 1 major · 7 minor (8) |
| goopg accuracy | 04, 05, 06 | APPROVE-WITH-CHANGES | 9 major (M1–M9) · 5 minor (m10–m14) |
| design feasibility | 07, 08, 09, TODO, README | APPROVE-WITH-CHANGES | 3 blocker · 10 major · 5 minor (18) |

## PostgreSQL accuracy — all 8 applied

| # | Sev | File/section | Resolution | Evidence |
|---|---|---|---|---|
| PG-1 | MAJOR | 02 §3.5 + §9 item 24, bitmap `maxentries` | `work_mem*1024/64` replaced by `tbm_calculate_entries(work_mem×1024)` with the ~104 B divisor and `costsize.c:6555` / `tidbitmap.c:1545` cites | 02:256, 02:632 |
| PG-2 | minor | 01 §2 rows 2–3 + §13 item 6, CTE/MERGE order | rows swapped CTEs-first; item 6 now "Order: CTEs, MERGE→join, …" | 01:73–74, 01:639 |
| PG-3 | minor | 01 §4, missing `query_planner` steps | `fix_placeholder_input_needed_levels` and `distribute_row_identity_vars` present as steps 10b/17b | 01:223, 01:231 |
| PG-4 | minor | 03 §4.1, `isdefault` arms | "only in this arm" gone; all three default arms named | 03:286–288 |
| PG-5 | minor | 02 §9 item 21 vs §3.3 | item 21 now selectivity-scaled, SA-divided, clamped, guarded | 02:629 (cf. 02:215–221) |
| PG-6 | minor | 02 §9 item 26 vs §3.6 | item 26 now `random + seq×(ceil(sel×pages)−1), min 1 page` | 02:634 |
| PG-7 | minor | 01 §13 item 24 vs §5 | item 24 now cites `plancat.c: estimate_rel_size` + heap impl in `tableam.c` | 01:663 (cf. 01:324) |
| PG-8 | minor | 02 §2.1 `clamp_width_est` | now "caps at MaxAllocSize; negativity is Assert-only, not clamped" | 02:102 |

## goopg accuracy — applied in docs; code side deferred

| # | Sev | File/section | Resolution | Evidence |
|---|---|---|---|---|
| M1 | MAJOR | 05 §10 fidelity column | re-keyed to take3 02 §9 (53 items); take2-numbering header fixed | 05:548–549 |
| M2 | MAJOR | 05 header mirror claim | narrowed to §§1,5,6,7,8 + explicit mapping table for §2/§3/§4/§9/§10 | 05:3–20 |
| M3 | MAJOR | 04 §4.4 `makeSpecialJoinInfo` cite | now `specialjoin.go:54` (def) / `collapse.go:416` (call), old `:398` identified | 04:287–288; residual U1 |
| M4 | MAJOR | 06 §1.5 `columnStatsForChildBase` cite | now `selectivity.go:451` wrapper → `cardinality.go:899` Base | 06:157–158 |
| M5 | MAJOR | 04 §12.2 "not a second copy" | restricted to the cost-number fields; globals/literals listed as the P2-02c/P3-10 hazard | 04:520–531 |
| M6 | MAJOR | 06 §13 `#` column | cites take3 03 §11; preamble says so | 06:6, 06:551–557 |
| M7 | MAJOR | line-citation drift | re-pinned at `b4e68c574`; `pathNCols`/`pathAvgVarBytes` at `path.go:348`, `:360` | 04:242–243; sweep below; residual U2 |
| M8 | MAJOR | 04 §1 bypass predicate | now 24 planner-input GUCs with the stale `dispatch.go:1975` comment called out | 04:168 |
| M9 | MAJOR | stale code comments | docs mark each stale site inline; comment edits need code commits | 04:168, 04:514–515; deferred U3 |
| m10 | minor | 04 §10 `drivingScan` | now SeqScan/BitmapHeapScan/IndexOnlyScan + Filter/Project/join-probe wrappers | 04:447–449; sweep below |
| m11–m12 | minor | 04 §12.3 / §2.1 enumerations | §12.3 now enumerates every defaulting `planSelect` site (CTE/copy/top-level/set-op/scalar + `planSelectWithParent` callers); §2.1 step 9 cites the `unnestPreDPOn` flag + `predp.go:73`, `:424` scoped to the post-pass | 04:554–557; 04:200, 04:202 |
| m13–m14 | minor | mirror/measurement labels | 06 §12 header notes no 03 counterpart; timings marked as-measured | 06:6, 05:11–12 |

## Design and sequencing — all 3 blockers + 13 majors applied

| # | Sev | File/section | Resolution | Evidence |
|---|---|---|---|---|
| F1 | BLOCKER | TODO P6-06 flag bundle | split into P6-06a…e, one flag per commit (5 not 6: COLLAPSE retires once in P3-05 per F6) | TODO:625–649 |
| F2 | BLOCKER | P2-02b unnamed blocker | P2-02d/e/f propagation slices added, each after P4-01 | TODO:349–363; 08:294–295, 08:500–502 |
| F3 | BLOCKER | P4-01 phase order | ordering bar: P4-01 executes before Phase 3 (§10.7, §2.3 safety rule) | TODO:504–506, 679–680; 08:514–515 |
| F4/F5 | MAJOR | TODO P2-09 / P2-16 bundles | split to P2-09a/b and P2-16a…e with per-item gates | TODO:365–370, 381–394; 08:299–327 |
| F6 | MAJOR | COLLAPSE double retirement | single owner P3-05; P6-06e says so explicitly | TODO:463, 644–648 |
| F7 | MAJOR | 08 §4 missing P1-18/P1-21 | both designed in §4 (P1-18 executes with Phase 3 after P3-04) | 08:253–263; TODO:231–232, 458–460 |
| F8 | MAJOR | unowned ranks | P1-30/P1-31 filed in Phase 1, P6-08 in Phase 6, ranks point at them | TODO:261–266, 653–656; 07:486–487 |
| F9 | MAJOR | 09 A4 stale target | numberless until the P0-04 re-measurement, `baseline + N` like A1/A2 | 09:131–136 |
| F10 | MAJOR | "clause-6" adjudication | replaced by DPPATH per-producer OFFERED/ACCEPTED + `--enum-trace`, Q72-class defined | 09:189; 08:392–393 |
| F11 | MAJOR | P4 exit instruments | witness + `Batches:` + DPPATH totals + threshold correction + values gate all named | 09:48, 09:190 |
| F12 | MAJOR | wrong pointers | 07 §3.3 → 08 §10.1 + §5.1; 08 §5.1 → 07 §3.3 + 04 §12.3; 08 §10.4 cites §3.2+§3.3 | 07:247–248; 08:285, 508–509 |
| F13 | MAJOR | unconsumed paths | P5-06 gates on its `parallel_hash_build.go` consumer; P4-05 BLOCKED on executor support | TODO:531–534, 580–583 |
| F14–F17 | minor | §4 order, gates, batching | §§4.1–4.4 monotone; P1-16 → 09 §2.2; P1-02 then P1-10 as two commits | 09:150–159; TODO:230; 08:248–251 |

## What the reviewers judged sound

Discriminating praise only, since the rest is noise:

- No invented landings on the goopg side: every "landed" claim spot-checked
  (seam, constant propagation, `uint32`/`maxSearchRels=32`, settings carrier,
  `DisabledNodes`, GEQO wiring, `joinorder.go` deletion, all cited commits
  resolving with matching subjects) is present in code.
- No finding of 07 claiming OPEN what 04/05/06 mark landed with a commit;
  the landed census is consistent across 04 §0, 05 §1, 06 preamble, 07 §5,
  and the TODO landed records.
- Every cost formula in 02 §§2–6 that the PG reviewer re-derived matched
  term for term in the same order of operations; worked examples 8.1–8.3
  recompute correctly.
- The 09 §1 rules are derived from incidents rather than principle, each
  closing a specific one; the per-category monotone exits in 09 §5 were
  judged better bars than corpus-wide percentages.
- The F3 safety rule ("wider search is not safe until the numbers ranking
  it are right") and the one-variable-per-commit discipline are now
  enforced by sequencing constraints, not just stated in prose.

## Stale-consumer sweep (this pass)

The goopg fix moved `pathNCols`/`pathAvgVarBytes` to `path.go:348`, `:360`
and widened `drivingScan` (m10/M7). Doc 04 carried both corrections; two
downstream consumers still quoted the old values and were updated here:

- 07 §3.2 (G2): `path.go:347,359` → `pathNCols`/`pathAvgVarBytes`,
  `path.go:348`, `:360` (07:198).
- TODO P4-01a: same citation fix (TODO:492).
- 07 §3.6 (G6): "recognises only SeqScan/BitmapHeapScan" → the 04 §10
  wording (IndexOnlyScan + Filter/Project/join-probe wrappers; plain
  IndexScan still missing) (07:302–305).
- TODO P5-03: same parenthetical fix; title narrowed to plain index scans
  (TODO:568–570).

## Unresolved / deferred (not claimed above)

- U1 — M3 downstream: FIXED post-review — 07:176, 08:341, TODO:437 now cite
  `specialjoin.go:54` (def) / `collapse.go:416` (call).
- U2 — M7 residual: only spot-fixed here. Either re-pin every `path:line`
  cite mechanically at the merge HEAD or drop line numbers for symbols.
- U3 — M9 code side: stale comments (`dispatch.go:1975`,
  `plannersettings.go:8-15`, `cost_funcs.go:77-79`, `path.go`, `joinsearch.go`,
  `relsize.go`, `joinkeyproof.go`) need code commits; docs already flag them.
- U4 — FIXED post-review — 08:441 now scopes P5-03 to plain `IndexScan`/bitmap
  (index-only already admitted).
- U5 — FIXED post-review — 03 §11 item 6 now cites `plancat.c` +
  `table_block_relation_estimate_size`, aligned with 01:663.
- U6 — FIXED this pass — m11: Grep confirms `with.go:205/:264/:334/:395`,
  `copy.go:41`, `planner.go:58/:915/:943/:1047/:10933` all call defaulting
  `planSelect`, as 04:554–557 enumerates. m12: `unnest.go:424` is the post-pass
  `unnestSubqueriesInPlan`, the pre-DP flag is `unnestPreDPOn` (`:57`, set at
  `:45`, read at `:64`), `predp.go:73` the search def — as 04:200 scopes them
  (post-pass at 04:202). F18: P1-29 wording in 07 §2.1 (07:89) + §5 (07:383),
  census exclusion rule (07:385–386), README map row (README:47), Status
  qualification (README:78–79), different-binary caveat (README:15–21) all
  present.

## Final verdict

**ACCEPT take3 as the planning baseline.** All three reviewers'
APPROVE-WITH-CHANGES conditions are met in the documents: PG 8/8, goopg
M1–M9 + m10–m14 in docs, design F1–F18 in docs. U1–U5 are follow-ups,
not blockers — none misstates a mechanism, and each names its file and line.
Next docs pass should clear U1, U2, U4, U5; code passes own U3.
