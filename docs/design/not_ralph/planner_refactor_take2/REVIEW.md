# REVIEW — agent review record

Three independent reviewers audited this bundle before it was committed. Each
worked from the source rather than from the documents: the PostgreSQL reviewer
against `./postgres/` with GNU GLOBAL, the goopg reviewer against the Go tree
with Serena, and the design reviewer against the recorded history of the four
previous planner rounds.

Their raw reports are kept as dotfiles beside this one — `.review-pg.md`,
`.review-goopg.md`, `.review-design.md` — because a summary of a review is not
a review, and the next round should be able to read what was actually said.

## Coverage

| reviewer | scope | claims checked | wrong | blockers |
|---|---|---:|---:|---:|
| PostgreSQL accuracy | 01, 02, 03 | ~241 | 39 | 3 |
| goopg accuracy | 04, 05, 06, 07 | ~90 | 11 | 1 |
| design feasibility | 08, 09, TODO, README | 33 findings | — | 7 |

Two results worth recording before the corrections, because they are evidence
about the method rather than about the documents:

- **Zero invented symbols.** All 82 `file.go:Symbol` citations across the
  goopg-side documents resolve to real declarations in the file named. This
  project has a recorded history of design documents citing symbols that do not
  exist; that failure mode did not recur here.
- **Every cost formula in doc 02 §2–§6 that the reviewer re-derived from source
  matched term for term and in the same order of operations**, including
  subtleties such as `cost_tuplesort` computing `input_bytes` before the
  `tuples < 2` clamp. All five worked examples in §8 recompute correctly.

## Blockers, and what changed

### PostgreSQL accuracy

| # | Finding | Resolution |
|---|---|---|
| 1 | Doc 02 §1.2 dated `disabled_nodes` to "PG 17 (kept in 18)". It is a **PG 18** feature (commit `e2225346794`, 2024-08-21); PG 17's `costsize.c` still has 23 `disable_cost` references. | Corrected, with the commit cited. |
| 2 | Doc 01 claimed `disable_cost` is never added any more. It survives in `final_cost_hashjoin` (`costsize.c:4421`), and doc 02 said so — the bundle contradicted itself. | Both sites in doc 01 corrected to name the surviving use. |
| 3 | Doc 01 described `create_unique_path` as adding `disabled_nodes`. It is a **hard veto**: it clears `semi_can_hash` (`pathnode.c:2023-2038`) and can return NULL. The `+1` rule lives in `create_setop_path`. | Corrected in both places. |
| 4 | Doc 01 §10 cited **`add_paths_to_partial_grouping_rel`, which does not exist** — the hallucinated-symbol failure mode, caught. | Replaced with the real names, `create_partial_grouping_paths` (planner.c:7351) and `add_paths_to_grouping_rel` (planner.c:7109). |
| 5 | Doc 02 checklist #77 generalised a worked-example constant into a formula: "220+width" per skew MCV entry. The true size is `MAXALIGN(width) + 116`; 220 is its value at width = 100. | Corrected. |
| 6 | Doc 03 §12.1 reproduced the manual's `rows=1007`. PG 18.3 itself returns **1006**, because `<` subtracts `eq_selec` for the bound. Confirmed against the live oracle. | Corrected, with a note to prefer the measured value over the manual's. |
| 7 | Doc 03 dated the removal of the 1.25× MCV rule to "PostgreSQL ≤ 11". It was removed by `b5db1d93d2a` and first absent in PG 11, so the last version carrying it was 10. | Corrected. |

**The MCV 1.25× question, resolved.** The review brief asked the reviewer to
adjudicate an apparent contradiction between doc 03 and docs 06/07/08. There was
none: doc 03 says PostgreSQL 18.3 has no 1.25× rule, and the goopg-side
documents say *goopg* uses one (`mcvFreqMargin = 1.25`,
`internal/executor/operators_analyze.go`) whose comment wrongly describes it as
upstream's. Both are correct and they are consistent. PG 18.3's
`analyze_mcv_list` prunes from the least common entry upward, keeping an entry
only when its sample count exceeds `selec·samplerows + 2·stddev + 0.5` with a
hypergeometric variance; `grep '1\.25' analyze.c` returns nothing. The stale
thing is goopg's in-code comment, which P1-08 now covers.

### goopg accuracy

| # | Finding | Resolution |
|---|---|---|
| 1 | **Doc 07 §3.6 fabricated an absence**: "`drivingScan` recognises only SeqScan and BitmapHeapScan, so Parallel Index Only Scan is structurally unreachable." It has an `*IndexOnlyScan` arm (`internal/optimizer/parallel.go:447`, M0134-0189), and doc 07's own q16 row contradicted the claim. The sentence had already been copied into doc 08, scoping Phase 5 against a gap that does not exist. | Corrected in both. The real gap is narrower and is now stated as such: plain `*IndexScan` has no arm, and eligibility is a hand-maintained arm list rather than a `consider_parallel` property. |
| 2 | **The Q72 figures were wrong.** Doc 07 described "a seven-table comma list plus two LEFT OUTER JOINs" producing "six search problems". The query is a **nine-relation explicit-`JOIN` chain**, and instrumenting the production predicates gives **eight** `searchOneProblem(nitems=2)` calls. Doc 04 had it right. | Corrected. |
| 3 | Two gap rows in doc 07 §3.10 described pre-fix code: outer-join row floors (`outerJoinRowFloor` exists on the legacy arm) and nested-loop rescan ("priced at zero" — `nestloopCost` takes an explicit rescan total). Docs 05 and 06 stated both correctly. | Both rows rewritten to say what is actually missing: the floors exist only on the legacy arm, and what is absent is a Material path and CTE rescan. |
| 4 | Doc 07 never mentioned the **third live cost model**. `joincost.go:chooseInnerJoinAlgo` has two production callers; `nliCostGateAccepts` and `estimateSubplanCostPerCall` are also in non-PostgreSQL units. A Phase 6 scoped from doc 07 alone would have missed them. | Added to doc 07 §3.5. |
| 5 | Doc 05 claimed `parallel_leader_participation` is "never read from a GUC in production"; it is (`dispatch.go:1614`). Doc 04 marked it correctly. | Left to doc 05's own errata; noted here. |

**Bonus code finding from the reviewer:** `numericValue` lists
`"float"`/`"real"`/`"double precision"` but **not** goopg's canonical `float4`
and `float8`, so float columns fall to the flat-0.5 `bucketFraction` alongside
the date and text types the documents enumerate.

### The over-stated claim, corrected

Both the goopg and design reviewers independently caught the same
over-statement, and it was the most consequential correction in the round.

Doc 07 §3.1 asserted that because `GOOPG_PGSHAPED_COLLAPSE` is off, every
explicit `JOIN` is pinned and therefore "no join order is searched at all for a
whole dialect of SQL" — with TODO P3-00 budgeted accordingly. The mechanism is
real and was verified link by link. **Its breadth was wrong by two orders of
magnitude**, and the repository already pins the true number in two git-tracked
tests: `TestCollapseIsAControlOnTheTPCHCorpus` asserts **0 of 22** TPC-H queries
are collapse-eligible, and `TestCollapseEligibilityOfTheTPCDSCorpus` pins TPC-DS
to exactly **{Q72, Q75} of 99**. The reason is compositional — a flat comma list
is one problem in either regime, and an outer join that has already pinned and
folded its two sides leaves an adjacent inner `JOIN` two-member in both regimes.

Consequences applied:

- Doc 07 §3.1 rewritten. The dominant mechanism is the **unconditional
  outer-join pin**, which no flag controls; the collapse flag owns two queries.
- Doc 08 §6.2 rewritten to match.
- **TODO P3-00 became P0-13**, reframed as the parity instrument's *positive
  control*: a change whose blast radius is pre-registered and tiny is exactly
  what proves the instrument measures what it claims. Expected result is TPC-H
  `changed=0` and exactly two TPC-DS plans moved; anything else means the
  instrument is wrong.

### A missing negative result

The design reviewer found that doc 07 §4.2 omitted a recorded NO-GO, violating
the bundle's own ground rule that negative results are preserved.
`analysis/leftdeep-joins/2026-08-06-p59m-README.md` recorded the collapse flip
as a NO-GO with a full arm pair. `internal/optimizer/joinsearchseam.go` then
records that verdict as **void** — it was "a no-go about a flag that could not
move a plan", decidable only after P5.9-r/s. Both facts are now in doc 07 §4.2,
because a reader who finds the p59m report without the seam's note will conclude
the opposite of the truth.

### Design and sequencing

| # | Finding | Resolution |
|---|---|---|
| 1 | **P0-02 breaks the only existing goopg-vs-PG instrument.** `cmd/estimate-audit` parses EXPLAIN text and its committed fixtures contain the literal `cost=0.00..0.00`, so surfacing real costs invalidates its reference capture — and nothing sequenced it. | P0-02 now carries the hazard, requires the parser update and reference re-capture in the same commit, and is blocked until P0-05/06 provide an independent signal. |
| 2 | **The parity instrument could not produce a meaningful first number**: goopg emits no bare `Hash` node, so a naive tree diff flags all 44 TPC-H hash joins, and A3 demanded `MISSING-NODE = 0` regardless. | Doc 09 §3.1 now declares a normalisation policy up front (PostgreSQL's `Hash` nodes are stripped; every rule is written down and printed in the report header), and A3 explicitly excludes `Hash`. |
| 3 | **A1/A2 named targets for a metric that has never been measured**, while P0-07 said P0 would set them. | A1/A2 now carry no number until P0-07 commits the baseline. The enforceable bars in the meantime are the monotone per-category ones in §5, which attribute better anyway. |
| 4 | **B1–B4 were stated as bars while §7.2 declared them unreachable** and P7-01 excluded them, leaving acceptance undefined. | B1–B4 are now explicitly directional targets for the engine and not acceptance criteria for this bundle; acceptance is A1–A5, B5, C1–C4. |
| 5 | **A5's slack of 3.0 was contradicted by the bundle's own evidence** — q18's joinrel is 42,837× over where PostgreSQL is 5,387× over, a ratio near 8. | A5 now ratchets from the measured post-P1 value instead of naming a constant. |
| 6 | **Missing scope**: grouping sets (12 TPC-DS queries, two of them among the slowest), `remove_useless_joins`, the existing `reduce_outer_joins.go` that the design never mentioned, FROM-clause subquery pull-up as a possible third search-shrinking mechanism, InitPlan/SubPlan/CTE alignment in the differ, and generic-vs-custom plans. | Added as doc 08 §11.1, "in scope but not yet scheduled", each requiring a TODO item before its phase starts. |
| 7 | **Eight TODO items are milestones wearing checkboxes** (P0-05/06, P1-22/23, P3-01…04, P4-01, P6-02). | Acknowledged; splitting them is the first act of each phase rather than a guess made now. Recorded here so the omission is deliberate and visible. |

### An unrecorded measurement confound, found during review

Not a reviewer finding but a check made while responding to one. The two TPC-H
benchmark clusters are **not configured alike**: PostgreSQL's `postgresql.conf`
sets `work_mem = 64MB` and `effective_cache_size = 2GB` explicitly, while
goopg's leaves both at boot defaults — 512MB and 4GB. The headline 9.9× is
therefore measured with goopg holding an **8× `work_mem` advantage**, so the
real gap is wider than reported, and any `work_mem`-sensitive cost comparison
between the engines is currently meaningless. This is now doc 09 §6.4, doc 07
§7 item 7b, and TODO P0-12, sequenced before the `work_mem` boot-value fix so
that a configuration change is not read as a catastrophic regression.

## What the reviewers judged sound

Discriminating praise only, since the rest is noise:

- The thesis — make the Path search the only planner, fix its inputs, delete
  what plans around it — was not challenged by any reviewer.
- No recorded no-go is re-proposed.
- Doc 09 §1's rules are derived from incidents rather than from principle, and
  each closes a specific one.
- Doc 07 retires three of its own deferral-ledger rows as stale rather than
  inheriting them, and the goopg reviewer verified all three independently,
  including the commit hash.
- Converting `sortPartialRootPays` from a hard-coded preference into a cost
  comparison (doc 08 §8) was called out as the right treatment of a divergence
  backed by real measurement.
- The per-category monotone exit criteria in doc 09 §5 were judged better bars
  than the corpus-wide percentages, and now carry the weight accordingly.

## Not done

- The design reviewer's recommendation to move Phase 2's inert half (the planner
  context and session-GUC threading) into Phase 0 was **not** applied. It is a
  reasonable argument — that work changes no plan by itself — but it enlarges
  Phase 0 past "instruments only", which is the property that makes Phase 0's
  `changed=0` exit criterion meaningful. Recorded here as a live disagreement
  rather than silently dropped.
- Doc 05's `parallel_leader_participation` error and doc 04's minor errata are
  noted in the raw reports and not yet folded into those documents.
- The remaining MINOR findings in `.review-pg.md` are not individually applied.
  They are imprecisions rather than errors of fact; the file is the work list.

## Addendum — five MAJOR findings applied after the first commit

A reviewer sub-agent auditing doc 03 §5–§13 finished after the bundle was first
committed (`ec220754b`). Its findings were already folded into `.review-pg.md`,
but only the first had been applied to the document. All five are now fixed and
re-verified against the source:

| # | Doc 03 | Was | Is | Citation |
|---|---|---|---|---|
| 1 | §12.1 | worked example gives `rows=1007`, "matches the manual" | **1006** — `ineq_histogram_selectivity` subtracts `eq_selec` for a strict `<`, which §5.3 of the same document already states. Mechanism confirmed on the live PG 18.3 oracle: `a < 1000` → 1003, `a <= 1000` → 1004, the difference being exactly the `eq_selec` term | `selfuncs.c:1198-1200,1315-1322` |
| 2 | §7.5 | `mergejoinscansel`'s `nulls_first` shift scales by `(1−start)` | plain addition, no scaling, applied to **both** the start and the end fraction | `selfuncs.c:3230-3249` |
| 3 | §8.3 | `estimate_hashagg_tablesize` in `planner.c`, formula adds `MAXALIGN(width) + MAXALIGN(SizeofMinimalTupleHeader)` to `hash_agg_entry_size` | it is in **`selfuncs.c:4179`**, and the body is `hash_agg_entry_size(...) * dNumGroups`. `hash_agg_entry_size` already includes those terms, so the old formula was the pre-PG16 shape and **double-counted** | `selfuncs.c:4178-4195`, `nodeAgg.c:1701-1731` |
| 4 | §9.3 | `dependency_is_compatible_clause` accepts `Var IS NULL` | `dependencies.c` has **no `NullTest` branch** (0 occurrences). `IS NULL` is compatible with **MCV** extended statistics only | `dependencies.c:741-870`, `extended_stats.c:1385-1390` |
| 5 | §11 | `pg_restore_relation_stats` writes `pg_class` "in place" | it calls `CatalogTupleUpdate` (`relation_stats.c:183`) — an ordinary **transactional** catalog update, not `vac_update_relstats`' in-place write | `relation_stats.c:137-192` |

Findings 3 and 5 are the kind that would have propagated silently: an
implementer porting `estimate_hashagg_tablesize` from the old formula would
size every hash-aggregate table too large, and one porting the statistics
import path would have reached for the wrong write mechanism.
