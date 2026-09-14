# INDEX-by-mechanism.md — round corpus cross-reference (built by M0137-0008)

*Generated 2026-09-15 from the same 126-round corpus as `INDEX-by-query.md` (see that file's header for scope notes). One section per mechanism/category tag (the vocabulary used by `METHODOLOGY3`'s per-query category census: join-order, join-method, aggregation-strategy, sort-strategy, scan-type, parameterisation, qual-placement, rendering, parallelism, plus the cross-cutting costing/width, statistics/ANALYZE, correctness-bug and instrument/harness tags used by this index). Rounds within a section are oldest-first. A round can and often does appear in multiple sections.*

## join-order (19 rounds)

| round | title | verdict |
|---|---|---|
| r14-adjudicate-against-pg | adjudicating the flip against PG, on evidence (Q9) | adjudicated: flip is PG-faithful direction (join-order separate, still wrong) |
| r22-plain-index-arm | the plain arm is provably unwinnable (dominance proof + production probe) | declined — reverted, provably unwinnable |
| r26-seam-decline-audit | the seam-decline audit: TPC-DS queries that can never match PG | recon only, no code — audit + diagnosis; fix landed in a separate round (R27) |
| r28-cte-inlining | goopg materialises CTEs where PG inlines them (111 vs 68 CTE Scan nodes) | recon only, no code |
| r39-seam-decline-triage | The 8 remaining seam declines triaged to their blockers (B-06, outer-spine, outer-link-no-sjinfo) | recon only, no code |
| r51-implied-equalities-seam | Flip seam to full transitive-equalities closure (K26 §§7-9) | landed, MATCH-neutral (opens candidate set; costing half still open) |
| r52-r51-shape-adjudication | Adjudication of all R51 shape/tag movements vs PG (no code) | recon only, no code (mostly TOWARD/MIXED verdicts named) |
| r67-materialize-retriage | Re-triage of Materialize producer: still not worth building | withdrawn (re-queued behind join-order/join-method costing) |
| r68-joinorder-costing-step0 | Q9 join-order DPPATH re-measured: NLI wins everywhere, pricing scoped both sides | recon only, no code |
| r71-q4-semi-selectivity | Q4 rows-vs-election crossover probe: rows theory dead, election program owns Q4 | withdrawn (rows theory DEAD; re-scoped to election program) |
| r74-semi-admission | Q4 fork trace: unnestExistsExpr is where EXISTS bypasses search admission | recon only, no code (recommend admit SEMI to search) |
| r75-semi-admission-slice | Harness-only PASS: SEMI NLI admission needs no new machinery | landed (test-only proof), no code change yet |
| r82-ds-head-baseline | TPC-DS HEAD baseline (SF0.25) + nearest-miss ranking to pick next round | recon only, no code (selected Q41 as next target) |
| r86-q96-step0 | Q96 partial-row provenance: PG-shaped L3 prefix loses within partial-path tournament (outcome F) | recon only, no code (seeds R87 on L2 selectivity/cost) |
| r96-q96-join-order | Q96 join-order Step-0/P0/P1: bucket-walk hypothesis ATTRIBUTED then FALSIFIED, reframed to inner-unique inputs | recon/attribution chain, no code — superseded (reframed slice, continued in r98) |
| r101-q96-forced-order-costs | PG's forced hdem-first vs store-first Q96 loser price observed; Goopg cannot bind the equivalent LATERAL SQL form | recon only — blocked by Goopg LATERAL binding gap (STOP) |
| r105-q96-goopg-forced-order-capture | Repeatable Goopg forced-order Q96 cost capture post-r104 (hdem-first vs store-first) | recon only, no code (measurement capture) |
| r127-semijoin-selectivity | proposed fixing Q4 aggregation-strategy via semi-join cardinality; premise already refuted by R71/R78/R81 | withdrawn (9 findings, 3 fatal, all refuted by prior evidence) |
| r41-anti-leaf-coordinates | Leaf numbering re-synchronised with binding order (K74/K75), reland after R42 unblocked it | landed, eligibility gain (leaf-count 3→0, declines 8→5), no MATCH change |

## join-method (25 rounds)

| round | title | verdict |
|---|---|---|
| r8-partial-path-admission | why no partial hash-join path ever wins (GOOPG_GATHER_PATHS off by default) | recon only, no code; blocker found (crash risk), filed to R9 |
| r9-partial-jointype-filter | jointype filter on the partial hash-join producer | landed, no parity movement at default; unblocks R10 |
| r10-gather-paths-default | flip GOOPG_GATHER_PATHS default | withdrawn/reverted — 15 failing tests need adjudication |
| r14-adjudicate-against-pg | adjudicating the flip against PG, on evidence (Q9) | adjudicated: flip is PG-faithful direction (join-order separate, still wrong) |
| r15-multikey-adjudication | the multi-key fallback is a cost question, not a capability one | recon only, no code — narrowed, not resolved |
| r16-pg-multikey-verdict | PG hash-joins the multi-key shape, so the flip loses a join PG keeps | adjudicated DIFFER: confirmed regression under flip (reversed by r17) |
| r17-multikey-on-real-data | correction: goopg does NOT lose the hash join on real data | recon only, no code — corrects r16; confirms flip near-PG on real data |
| r25-nli-decompose | decompose the NLI node into PG-native operators | landed (slice 1 + CTEScan seam-decline fix) |
| r27-outer-join-reduction | Row-dropping outer-join demotion fixed, oracle-verified (applyDemotion reads upperNN not accumulatedNN) | correctness bug fixed (not parity) |
| r40-left-anti-transplant | LEFT→ANTI transplant landed, uncovers latent column-vs-relation-granularity wrong-rows bug (K71) | landed, correctness bug fixed; decline class converted not reduced, no MATCH change |
| r43-parallel-hash | Parallel-hash parity re-scoped (K79/K80); TRUTHFUL-but-narrower verdict on cooperative build labelling | design only (rev 4), no code landed; corrects match count to 2/22 |
| r60-partial-nestloop-producer | Implement partial (parallel) nested-loop path producer | landed, CONDITIONAL PASS (Q5/Q10 shape moved, accepted as scope change) |
| r64-nli-right-decline | Decline NLI when probe is the preserved (RIGHT-join) side; fixes Memoize wrong-results bug | landed, correctness bug fixed (Q13 33→34 rows) |
| r67-materialize-retriage | Re-triage of Materialize producer: still not worth building | withdrawn (re-queued behind join-order/join-method costing) |
| r68-joinorder-costing-step0 | Q9 join-order DPPATH re-measured: NLI wins everywhere, pricing scoped both sides | recon only, no code |
| r69-nli-probe-audit | NLI rescan-startup term landed (missing PG term added to nestloopCost) | landed, MATCH gained (categories reduced on several queries) |
| r72-election-step0 | Q4 election census: ruling is price-defect, not election-rule defect | adjudicated (c): no election-rule slice, defect is upstream pricing |
| r82-ds-head-baseline | TPC-DS HEAD baseline (SF0.25) + nearest-miss ranking to pick next round | recon only, no code (selected Q41 as next target) |
| r93-isolated-partial-prebuilt | PathNestLoop path-kind does not prove executable partial NLI node for Q96 | recon only, no code |
| r94-partial-plain-nestloop | Ordinary INNER nested-loop partial capability lands; Q96's shape is lateral, not ordinary | landed (ordinary-NL partial), Q96 out of scope — superseded by r95 |
| r95-lateral-probe-workers | Lateral-probe worker semantics land; Q96 now goes parallel by cost | landed, MATCH gained (shape-diff narrowed to join-order/scan-type/qual-placement) |
| r115-q96-nonspill-forced-order | Q96 forced-order joins bypass the HashJoin path-cost seam; attribution falsified at that seam | recon only, no code (negative attribution) |
| r116-q96-legacy-join-attribution | Q96 margin not explained by legacy join-method/build-side choice; traced to lower-join cardinality | recon only, no code |
| r122-narrow-join-propagation | Slice B: narrowing propagated past first join; first parity gain — TPC-H join-method/scan-type −1 (Q3) | landed default-off, categories improved (no MATCH flip) |
| r128-parity-over-throughput | promotes GOOPG_NARROW_COST_INPUTS to default-ON; TPC-H join-method/scan-type gain confirmed, throughput cost ~nil | landed, GOOPG_NARROW_COST_INPUTS shipped default-ON |

## aggregation-strategy (22 rounds)

| round | title | verdict |
|---|---|---|
| r3-hashagg-spill | the memory-blind HashAggregate | landed, no MATCH change (minor contributor; dominant cause elsewhere) |
| r6-window-sort | the window's Sort is now in the plan (slice A) | landed, no MATCH change (slice A only; slice B filed) |
| r19-aggregation-strategy | the aggregation-strategy move explained: flip loses the Partial/Finalize split | adjudicated: regression explained (favorable); fix recommended before landing flip |
| r20-partial-agg-over-gather | the wiring gap is one guard, and it unifies with K12 | recon only, no code — root cause unified with K12(B); TODO restructured |
| r21-upper-planner-seam | give the upper planner the join rel's paths, not a finished Node | landed through slice 2b (splice fix); slice 3 + flip default still pending |
| r33-subquery-parallel-pass | Post-cache parallel pass recurses into uncorrelated sublinks (K44) | landed, plan shape moved toward PG, no MATCH change |
| r45-gather-merge-ordered-finalize | goopg cannot combine a split aggregate with an ordered Gather Merge — Finalize GroupAggregate unreachable (K94/K96/K97) | design rejected on review (architecturally impossible: partial agg is row-less); diagnosis stands, filed as multi-round executor item (K97) |
| r47-q4-upper-rel | Slice-2: no-sort sorted-election measurement for upper ORDER BY/grouping | landed, 16 flips toward PG (Q4 itself did not flip; firing micro-rule still unidentified) |
| r54-parallel-admission-step0 | Parallel-admission measurement, fix, regression, and redesign relanding | fix round FAILED BACK TO DESIGN (Q7/Q8 regressed away from PG); redesign re-land + Q7 selectivity fix LANDED |
| r61-groupcount-search-input | Size the grouping rel's row count from the join search input, not blind 1 | landed, all gates PASS |
| r71-q4-semi-selectivity | Q4 rows-vs-election crossover probe: rows theory dead, election program owns Q4 | withdrawn (rows theory DEAD; re-scoped to election program) |
| r72-election-step0 | Q4 election census: ruling is price-defect, not election-rule defect | adjudicated (c): no election-rule slice, defect is upstream pricing |
| r76-nli-price-splice | In-situ stamp splice: Q4 election priced on a real base (temp binary) | landed (temp splice proof); deferred full gates to R77 |
| r77-production-splice | Production NLI price splice landed (dual call-site stamping) | landed, no MATCH change (categories same, costs now PG-scale) |
| r81-q4-ordered-remeasure | Q4 ordered-election DPPATH re-harvest post-R77: comparator correct as designed | adjudicated DIFFER (structural, blocked on selectivity+width inputs) |
| r82-ds-head-baseline | TPC-DS HEAD baseline (SF0.25) + nearest-miss ranking to pick next round | recon only, no code (selected Q41 as next target) |
| r92-q96-partial-aggregate-seed | Q96 upper partial-aggregate source barrier (PathPrebuilt representation barrier, not executable) | recon only, no code |
| r93-isolated-partial-prebuilt | PathNestLoop path-kind does not prove executable partial NLI node for Q96 | recon only, no code |
| r95-lateral-probe-workers | Lateral-probe worker semantics land; Q96 now goes parallel by cost | landed, MATCH gained (shape-diff narrowed to join-order/scan-type/qual-placement) |
| r120-hashagg-width-currency | GOOPG_HASHAGG_WIDTH_CURRENCY corrects HashAgg entry-size currency but is net-negative alone; needs ncols narrowing pairing | landed default-off; pairing hypothesis later refuted by r124 |
| r124-nontable-leaf-widths | non-table leaves get narrowed too (mixed bucket → 0), but zero plan/cost movement; refutes r120's pairing hypothesis | landed default-off, no MATCH change; supersedes/refutes r120 pairing rationale |
| r127-semijoin-selectivity | proposed fixing Q4 aggregation-strategy via semi-join cardinality; premise already refuted by R71/R78/R81 | withdrawn (9 findings, 3 fatal, all refuted by prior evidence) |

## sort-strategy (14 rounds)

| round | title | verdict |
|---|---|---|
| r6-window-sort | the window's Sort is now in the plan (slice A) | landed, no MATCH change (slice A only; slice B filed) |
| r21-upper-planner-seam | give the upper planner the join rel's paths, not a finished Node | landed through slice 2b (splice fix); slice 3 + flip default still pending |
| r45-gather-merge-ordered-finalize | goopg cannot combine a split aggregate with an ordered Gather Merge — Finalize GroupAggregate unreachable (K94/K96/K97) | design rejected on review (architecturally impossible: partial agg is row-less); diagnosis stands, filed as multi-round executor item (K97) |
| r47-q4-upper-rel | Slice-2: no-sort sorted-election measurement for upper ORDER BY/grouping | landed, 16 flips toward PG (Q4 itself did not flip; firing micro-rule still unidentified) |
| r52-r51-shape-adjudication | Adjudication of all R51 shape/tag movements vs PG (no code) | recon only, no code (mostly TOWARD/MIXED verdicts named) |
| r56-sort-placement-gathermerge | Third no-split upper arm: GroupAgg→GatherMerge→Sort worker-sort pricing | landed, prediction lands + in-loop Q78 defect found and fixed |
| r72-election-step0 | Q4 election census: ruling is price-defect, not election-rule defect | adjudicated (c): no election-rule slice, defect is upstream pricing |
| r76-nli-price-splice | In-situ stamp splice: Q4 election priced on a real base (temp binary) | landed (temp splice proof); deferred full gates to R77 |
| r77-production-splice | Production NLI price splice landed (dual call-site stamping) | landed, no MATCH change (categories same, costs now PG-scale) |
| r81-q4-ordered-remeasure | Q4 ordered-election DPPATH re-harvest post-R77: comparator correct as designed | adjudicated DIFFER (structural, blocked on selectivity+width inputs) |
| r82-ds-head-baseline | TPC-DS HEAD baseline (SF0.25) + nearest-miss ranking to pick next round | recon only, no code (selected Q41 as next target) |
| r83-limit-above-distinct | LIMIT moved above DISTINCT (Q41): fixes real values-correctness bug plus shape | correctness bug fixed; DS Q41 categories unchanged (3, honest miss on metric) |
| r84-distinct-outer-sort-skip | Skip redundant outer Sort over Distinct (Q41) | landed, MATCH-adjacent gain (Q41 3→1 categories) |
| r113-pg-sort-relation-bytes | opt-in GOOPG_PG_SORT_RELATION_BYTES_COST replacing costSortRun volume with PG's relation_byte_size | landed default-off, no MATCH change |

## scan-type (8 rounds)

| round | title | verdict |
|---|---|---|
| r22-plain-index-arm | the plain arm is provably unwinnable (dominance proof + production probe) | declined — reverted, provably unwinnable |
| r25-nli-decompose | decompose the NLI node into PG-native operators | landed (slice 1 + CTEScan seam-decline fix) |
| r29-escape-guard-binders | Binder-aware escape guard for planHasEscapingOuterRef (structural plan walk) | landed, lateral decline eliminated; scan-type +1 (uncovered pre-existing K33 costing bug), no MATCH change |
| r46-legacy-index-seq-competition | Cost the legacy funnel's index-vs-seq choice (K98) | landed, MATCH gained (TPC-DS Q9 — programme's first TPC-DS match) |
| r52-r51-shape-adjudication | Adjudication of all R51 shape/tag movements vs PG (no code) | recon only, no code (mostly TOWARD/MIXED verdicts named) |
| r82-ds-head-baseline | TPC-DS HEAD baseline (SF0.25) + nearest-miss ranking to pick next round | recon only, no code (selected Q41 as next target) |
| r122-narrow-join-propagation | Slice B: narrowing propagated past first join; first parity gain — TPC-H join-method/scan-type −1 (Q3) | landed default-off, categories improved (no MATCH flip) |
| r128-parity-over-throughput | promotes GOOPG_NARROW_COST_INPUTS to default-ON; TPC-H join-method/scan-type gain confirmed, throughput cost ~nil | landed, GOOPG_NARROW_COST_INPUTS shipped default-ON |

## parameterisation (6 rounds)

| round | title | verdict |
|---|---|---|
| r49-bitmap-probe-param | Parameterize bitmap-heap NLI probe: render Index Cond / move probe qual to BitmapQual | design only (Slice A/B specified; NULL-probe-key correctness bug named for Slice B) |
| r82-ds-head-baseline | TPC-DS HEAD baseline (SF0.25) + nearest-miss ranking to pick next round | recon only, no code (selected Q41 as next target) |
| r101-q96-forced-order-costs | PG's forced hdem-first vs store-first Q96 loser price observed; Goopg cannot bind the equivalent LATERAL SQL form | recon only — blocked by Goopg LATERAL binding gap (STOP) |
| r102-lateral-joined-left-namespace | Grouped-JOIN LATERAL namespace fix needs planner binding, not analyzer-only (temp code reverted) | withdrawn (reverted); boundary identified — superseded by r103 |
| r103-grouped-join-source-binding | USING-lateral output re-resolution still blocks end-to-end fix (LATERAL child loses merged-column map) | withdrawn (reverted); STOP — superseded by r104 |
| r104-grouped-join-using-lateral-output | Grouped JOIN USING column bindings now survive LATERAL re-resolution — lands | correctness bug fixed (not parity); semantic enablement only |

## qual-placement (18 rounds)

| round | title | verdict |
|---|---|---|
| r25-nli-decompose | decompose the NLI node into PG-native operators | landed (slice 1 + CTEScan seam-decline fix) |
| r26-seam-decline-audit | the seam-decline audit: TPC-DS queries that can never match PG | recon only, no code — audit + diagnosis; fix landed in a separate round (R27) |
| r40-left-anti-transplant | LEFT→ANTI transplant landed, uncovers latent column-vs-relation-granularity wrong-rows bug (K71) | landed, correctness bug fixed; decline class converted not reduced, no MATCH change |
| r42-pushdown-project-arm | Qual-pushdown descent now crosses a Project node (K76/K77) | landed, qual-placement improved on 2 queries, no MATCH change; prerequisite for R41 reland |
| r44-const-fold-before-selectivity | Fold quals where PG folds them — step A (numeric/general const-fold) + step B (date+interval temporal fold) (K83/K85/K88/K89/K91) | landed (2 steps), net category improvement both corpora (TPC-DS −10, TPC-H −3), no MATCH change; corrected an earlier false "byte-identical" harness claim |
| r48-semi-joinqual-placement | Filter:(true) EXPLAIN drop + semi JoinQual placement onto NLI probe | landed, both halves LANDED |
| r49-bitmap-probe-param | Parameterize bitmap-heap NLI probe: render Index Cond / move probe qual to BitmapQual | design only (Slice A/B specified; NULL-probe-key correctness bug named for Slice B) |
| r50-hash-joinfilter-dedup | Drop hash Join Filter conjuncts already covered by Hash Cond (bpchar whitelist) | design, landing plan specified (Slice A scoped; single-commit landing planned) |
| r51-implied-equalities-seam | Flip seam to full transitive-equalities closure (K26 §§7-9) | landed, MATCH-neutral (opens candidate set; costing half still open) |
| r52-r51-shape-adjudication | Adjudication of all R51 shape/tag movements vs PG (no code) | recon only, no code (mostly TOWARD/MIXED verdicts named) |
| r74-semi-admission | Q4 fork trace: unnestExistsExpr is where EXISTS bypasses search admission | recon only, no code (recommend admit SEMI to search) |
| r82-ds-head-baseline | TPC-DS HEAD baseline (SF0.25) + nearest-miss ranking to pick next round | recon only, no code (selected Q41 as next target) |
| r83-limit-above-distinct | LIMIT moved above DISTINCT (Q41): fixes real values-correctness bug plus shape | correctness bug fixed; DS Q41 categories unchanged (3, honest miss on metric) |
| r84-distinct-outer-sort-skip | Skip redundant outer Sort over Distinct (Q41) | landed, MATCH-adjacent gain (Q41 3→1 categories) |
| r101-q96-forced-order-costs | PG's forced hdem-first vs store-first Q96 loser price observed; Goopg cannot bind the equivalent LATERAL SQL form | recon only — blocked by Goopg LATERAL binding gap (STOP) |
| r102-lateral-joined-left-namespace | Grouped-JOIN LATERAL namespace fix needs planner binding, not analyzer-only (temp code reverted) | withdrawn (reverted); boundary identified — superseded by r103 |
| r103-grouped-join-source-binding | USING-lateral output re-resolution still blocks end-to-end fix (LATERAL child loses merged-column map) | withdrawn (reverted); STOP — superseded by r104 |
| r104-grouped-join-using-lateral-output | Grouped JOIN USING column bindings now survive LATERAL re-resolution — lands | correctness bug fixed (not parity); semantic enablement only |

## rendering (14 rounds)

| round | title | verdict |
|---|---|---|
| r2-instrument | the instrument, and what it was measuring against (comparator + reference-config fix) | landed, corrected baseline (instrument fix, not a plan change) |
| r7-parallel-aware-label | ParallelAware label threaded onto Join nodes | landed (plumbing); premise falsified, no parity movement |
| r32-targetlist-subplan-display | Targetlist subplan display fixed in EXPLAIN (K42, Project-arm skip sites) | landed, no MATCH change |
| r48-semi-joinqual-placement | Filter:(true) EXPLAIN drop + semi JoinQual placement onto NLI probe | landed, both halves LANDED |
| r49-bitmap-probe-param | Parameterize bitmap-heap NLI probe: render Index Cond / move probe qual to BitmapQual | design only (Slice A/B specified; NULL-probe-key correctness bug named for Slice B) |
| r50-hash-joinfilter-dedup | Drop hash Join Filter conjuncts already covered by Hash Cond (bpchar whitelist) | design, landing plan specified (Slice A scoped; single-commit landing planned) |
| r65-q11-explain-render | EXPLAIN rendering: Sort-key OUTER_VAR expansion through Aggregate + InitPlan .col1 | landed, Q11 reaches MATCH, all gates hold |
| r66-alias-source-keys | Alias→source Sort/Group key rendering (Slice 1: Sort-group/Star/Distinct; Slice 2: cross-boundary chase) | landed (both slices), rendering set reduced to {Q10} only |
| r67-materialize-retriage | Re-triage of Materialize producer: still not worth building | withdrawn (re-queued behind join-order/join-method costing) |
| r73-semi-price-audit | Q4 display-seam audit: no planning site produces the SEMI price 570.66 | recon only, no code (re-scope: search admission needed) |
| r77-production-splice | Production NLI price splice landed (dual call-site stamping) | landed, no MATCH change (categories same, costs now PG-scale) |
| r82-ds-head-baseline | TPC-DS HEAD baseline (SF0.25) + nearest-miss ranking to pick next round | recon only, no code (selected Q41 as next target) |
| r85-execparam-display | Deparse PARAM_EXEC ($N) to its owning SubPlan source column in EXPLAIN | landed, MATCH gained (DS match 1→2, Q41 closed) |
| r110-varchar-trailing-space-fidelity | varchar assignment/COPY coercion now preserves in-range trailing spaces | correctness bug fixed (not parity) |

## parallelism (29 rounds)

| round | title | verdict |
|---|---|---|
| r2-instrument | the instrument, and what it was measuring against (comparator + reference-config fix) | landed, corrected baseline (instrument fix, not a plan change) |
| r7-parallel-aware-label | ParallelAware label threaded onto Join nodes | landed (plumbing); premise falsified, no parity movement |
| r8-partial-path-admission | why no partial hash-join path ever wins (GOOPG_GATHER_PATHS off by default) | recon only, no code; blocker found (crash risk), filed to R9 |
| r9-partial-jointype-filter | jointype filter on the partial hash-join producer | landed, no parity movement at default; unblocks R10 |
| r10-gather-paths-default | flip GOOPG_GATHER_PATHS default | withdrawn/reverted — 15 failing tests need adjudication |
| r11-adjudicate-gather-walkers | adjudicate the 15 — Gather-blind walker bug found (15→8) | landed (4 walker fixes); flip still reverted |
| r12-remaining-eight | adjudicate the remaining 8 (8→7); "likely defect" was another walker bug | landed (1 more walker fix); flip still reverted |
| r13-mechanical-and-postpass | mechanical items resolved; post-pass Gather not gated by the knob (findings) | recon only, no code — post-pass design decision needed |
| r14-adjudicate-against-pg | adjudicating the flip against PG, on evidence (Q9) | adjudicated: flip is PG-faithful direction (join-order separate, still wrong) |
| r17-multikey-on-real-data | correction: goopg does NOT lose the hash join on real data | recon only, no code — corrects r16; confirms flip near-PG on real data |
| r19-aggregation-strategy | the aggregation-strategy move explained: flip loses the Partial/Finalize split | adjudicated: regression explained (favorable); fix recommended before landing flip |
| r20-partial-agg-over-gather | the wiring gap is one guard, and it unifies with K12 | recon only, no code — root cause unified with K12(B); TODO restructured |
| r21-upper-planner-seam | give the upper planner the join rel's paths, not a finished Node | landed through slice 2b (splice fix); slice 3 + flip default still pending |
| r31-parallelism-and-heap-density | Parallelism is the dominant axis, blocked on heap density (then: heap reload done, parity unmoved) | investigation; heap reload executed on private clone, confirmed NOT the blocker — parity unmoved |
| r33-subquery-parallel-pass | Post-cache parallel pass recurses into uncorrelated sublinks (K44) | landed, plan shape moved toward PG, no MATCH change |
| r43-parallel-hash | Parallel-hash parity re-scoped (K79/K80); TRUTHFUL-but-narrower verdict on cooperative build labelling | design only (rev 4), no code landed; corrects match count to 2/22 |
| r45-gather-merge-ordered-finalize | goopg cannot combine a split aggregate with an ordered Gather Merge — Finalize GroupAggregate unreachable (K94/K96/K97) | design rejected on review (architecturally impossible: partial agg is row-less); diagnosis stands, filed as multi-round executor item (K97) |
| r52-r51-shape-adjudication | Adjudication of all R51 shape/tag movements vs PG (no code) | recon only, no code (mostly TOWARD/MIXED verdicts named) |
| r54-parallel-admission-step0 | Parallel-admission measurement, fix, regression, and redesign relanding | fix round FAILED BACK TO DESIGN (Q7/Q8 regressed away from PG); redesign re-land + Q7 selectivity fix LANDED |
| r56-sort-placement-gathermerge | Third no-split upper arm: GroupAgg→GatherMerge→Sort worker-sort pricing | landed, prediction lands + in-loop Q78 defect found and fixed |
| r58-worker-count-sizing | Audit of worker-count 4-vs-2 divergence: faithful arithmetic, shape-driven | closed as faithful, no code change (re-owned to index-probe pricing audit) |
| r60-partial-nestloop-producer | Implement partial (parallel) nested-loop path producer | landed, CONDITIONAL PASS (Q5/Q10 shape moved, accepted as scope change) |
| r82-ds-head-baseline | TPC-DS HEAD baseline (SF0.25) + nearest-miss ranking to pick next round | recon only, no code (selected Q41 as next target) |
| r86-q96-step0 | Q96 partial-row provenance: PG-shaped L3 prefix loses within partial-path tournament (outcome F) | recon only, no code (seeds R87 on L2 selectivity/cost) |
| r92-q96-partial-aggregate-seed | Q96 upper partial-aggregate source barrier (PathPrebuilt representation barrier, not executable) | recon only, no code |
| r93-isolated-partial-prebuilt | PathNestLoop path-kind does not prove executable partial NLI node for Q96 | recon only, no code |
| r94-partial-plain-nestloop | Ordinary INNER nested-loop partial capability lands; Q96's shape is lateral, not ordinary | landed (ordinary-NL partial), Q96 out of scope — superseded by r95 |
| r95-lateral-probe-workers | Lateral-probe worker semantics land; Q96 now goes parallel by cost | landed, MATCH gained (shape-diff narrowed to join-order/scan-type/qual-placement) |
| r128-parity-over-throughput | promotes GOOPG_NARROW_COST_INPUTS to default-ON; TPC-H join-method/scan-type gain confirmed, throughput cost ~nil | landed, GOOPG_NARROW_COST_INPUTS shipped default-ON |

## costing/width (59 rounds)

| round | title | verdict |
|---|---|---|
| r1-qpqual-index | qpqual currency on index cost sites | landed, no MATCH change (values unmoved) |
| r3-hashagg-spill | the memory-blind HashAggregate | landed, no MATCH change (minor contributor; dominant cause elsewhere) |
| r4-heap-density | heap page density divergence (findings) | recon only, no code (root cause: page density, not planner) |
| r15-multikey-adjudication | the multi-key fallback is a cost question, not a capability one | recon only, no code — narrowed, not resolved |
| r16-pg-multikey-verdict | PG hash-joins the multi-key shape, so the flip loses a join PG keeps | adjudicated DIFFER: confirmed regression under flip (reversed by r17) |
| r21-index-leaf-qpqual | qual count for prebuilt index leaves — DECLINED, no witness | declined — reverted, production-dead (census-verified) |
| r22-plain-index-arm | the plain arm is provably unwinnable (dominance proof + production probe) | declined — reverted, provably unwinnable |
| r30-analyze-physical-order | ANALYZE sample sorted into physical (block,offset) order, fixing correlation stat | landed, category trade (scan-type −1, join-method +2), reproduced on re-baselined heap |
| r31-parallelism-and-heap-density | Parallelism is the dominant axis, blocked on heap density (then: heap reload done, parity unmoved) | investigation; heap reload executed on private clone, confirmed NOT the blocker — parity unmoved |
| r36-baserel-selectivity-reliable-gate | Baserel sizing multiplies unconditionally — drops the reliable early-return gate | landed, net parity improvement (TPC-DS −2, TPC-H −1 categories); largest structural movement so far |
| r37-two-cost-models | goopg has two seq-scan cost models (search vs legacy); Q12 divergence traced to hash-join row WIDTH (K60→K65) | recon only, no code; files K65 (column-pruning/row-width) as new blocker |
| r38-reltarget-width | RelOptInfo.Width narrowing rejected on review, then refuted by measurement (NCols/AvgVarBytes is the real target) | rejected on review + refuted by measurement, nothing implemented; K65 filed as necessary-but-not-sufficient |
| r43-parallel-hash | Parallel-hash parity re-scoped (K79/K80); TRUTHFUL-but-narrower verdict on cooperative build labelling | design only (rev 4), no code landed; corrects match count to 2/22 |
| r44-const-fold-before-selectivity | Fold quals where PG folds them — step A (numeric/general const-fold) + step B (date+interval temporal fold) (K83/K85/K88/K89/K91) | landed (2 steps), net category improvement both corpora (TPC-DS −10, TPC-H −3), no MATCH change; corrected an earlier false "byte-identical" harness claim |
| r46-legacy-index-seq-competition | Cost the legacy funnel's index-vs-seq choice (K98) | landed, MATCH gained (TPC-DS Q9 — programme's first TPC-DS match) |
| r53-q9-costing-step0 | Step-0 instrumentation: is Q9's L4 divergence sizing or pricing? | landed (instrument only), pricing identified as cause; scoped follow-up slice defined |
| r55-sort-gap-or-defaults-tiebreak | OR-conjunct inequality + IN-list selectivity arms (P3 of SCOPE §2) | landed, P3 lands (133→26 selectivity fix, no plan regression) |
| r56-sort-placement-gathermerge | Third no-split upper arm: GroupAgg→GatherMerge→Sort worker-sort pricing | landed, prediction lands + in-loop Q78 defect found and fixed |
| r57-join-rows-nlead | Audit of join-rows N-lead residual: units error, not an estimator gap | closed as artifact, no code change (units-error, re-owned to worker-count audit) |
| r58-worker-count-sizing | Audit of worker-count 4-vs-2 divergence: faithful arithmetic, shape-driven | closed as faithful, no code change (re-owned to index-probe pricing audit) |
| r59-index-probe-loopcount | Implement index-side loop-count pro-rating (PG num_scans>1 arm) + NLI enable-gating fix | landed, prediction FAILED (Q7 flipped) but adjudicated as priced-mechanism move; DS Q72 TIMEOUT carried to review |
| r61-groupcount-search-input | Size the grouping rel's row count from the join search input, not blind 1 | landed, all gates PASS |
| r62-yao-parameterized-probe | Decline Yao per-scan evidence under a parameterized probe | landed, all gates PASS (Q11 search PG-exact) |
| r63-resolver-ios-partialagg | Resolver arms: IndexOnlyScan + partial-agg group-key identity for n-distinct chase | landed (estimator gates PASS); commit BLOCKED by pre-existing Q13 Memoize/RightJoin bug found |
| r68-joinorder-costing-step0 | Q9 join-order DPPATH re-measured: NLI wins everywhere, pricing scoped both sides | recon only, no code |
| r69-nli-probe-audit | NLI rescan-startup term landed (missing PG term added to nestloopCost) | landed, MATCH gained (categories reduced on several queries) |
| r70-hash-footprint | Hash-build width/footprint cut for Q9: decision table blocked (fragile under ±10% rows) | blocked (no cut; needs DatumBytes/projection-pushdown) |
| r71-q4-semi-selectivity | Q4 rows-vs-election crossover probe: rows theory dead, election program owns Q4 | withdrawn (rows theory DEAD; re-scoped to election program) |
| r72-election-step0 | Q4 election census: ruling is price-defect, not election-rule defect | adjudicated (c): no election-rule slice, defect is upstream pricing |
| r73-semi-price-audit | Q4 display-seam audit: no planning site produces the SEMI price 570.66 | recon only, no code (re-scope: search admission needed) |
| r75-semi-admission-slice | Harness-only PASS: SEMI NLI admission needs no new machinery | landed (test-only proof), no code change yet |
| r76-nli-price-splice | In-situ stamp splice: Q4 election priced on a real base (temp binary) | landed (temp splice proof); deferred full gates to R77 |
| r77-production-splice | Production NLI price splice landed (dual call-site stamping) | landed, no MATCH change (categories same, costs now PG-scale) |
| r78-semi-selectivity | Q4 semi-selectivity probe: model faithful, blocked on ndistinct stats divergence | blocked (re-scope as ndistinct estimator program) |
| r81-q4-ordered-remeasure | Q4 ordered-election DPPATH re-harvest post-R77: comparator correct as designed | adjudicated DIFFER (structural, blocked on selectivity+width inputs) |
| r86-q96-step0 | Q96 partial-row provenance: PG-shaped L3 prefix loses within partial-path tournament (outcome F) | recon only, no code (seeds R87 on L2 selectivity/cost) |
| r87-q96-f-prefix-cost | Q96 F-branch: unique-index shortcut drops null-fraction charge vs PG's eqjoinsel_inner | recon only, no code (identifies F1; hands off to R88) |
| r88-q96-unique-key-null | Bare unique keys now retain ordinary null-aware equality selectivity (FK vs UNIQUE split) | landed, no MATCH change (Q96 still shape-diff; narrows gap) |
| r89-q96-partial-prefix-cost | Q96 partial-prefix cost attribution: Goopg lacks PG's inner-unique/virtual-bucket cost inputs (outcome C2) | recon only, no code (measurement-only) |
| r90-inner-unique-hash-final-cost | Inner-unique proof carried into Hash Join final costing (matched-probe term) | landed, no MATCH change (Q96 still shape-diff; cost narrows, no flip) |
| r91-pg-virtual-bucket-unmatched | PG virtual-bucket geometry ported for unmatched unique-probe Hash Join costing | landed, no MATCH change (Q96 serial order now matches PG's relation order; still not structurally equivalent — Gather/partial-agg choice remains) |
| r96-q96-join-order | Q96 join-order Step-0/P0/P1: bucket-walk hypothesis ATTRIBUTED then FALSIFIED, reframed to inner-unique inputs | recon/attribution chain, no code — superseded (reframed slice, continued in r98) |
| r98-q96-innerunique-inputs | Goopg inner-unique hash-join cost inputs reconcile exactly; PG's equivalent private inputs remain unobservable | adjudicated UNOBSERVABLE, no cost change authorized |
| r105-q96-goopg-forced-order-capture | Repeatable Goopg forced-order Q96 cost capture post-r104 (hdem-first vs store-first) | recon only, no code (measurement capture) |
| r106-q96-forced-margin-attribution | Forced-margin Hash Join cost-term attribution is unobservable from available trace/PG JSON | adjudicated UNOBSERVABLE, no code change |
| r108-pg-hash-tuple-sizing | Opt-in PG packed-tuple Hash Join spill-cost comparison (GOOPG_PG_HASH_TUPLE_SPILL_COST) for Q96 | landed opt-in experiment (default-off), no MATCH/plan change — kept off |
| r112-pg-cost-family-inventory | inventory of PG cost families vs goopg representations; no universal Datum-size substitution defensible | recon only, no code (scoped follow-up: Sort-only) |
| r113-pg-sort-relation-bytes | opt-in GOOPG_PG_SORT_RELATION_BYTES_COST replacing costSortRun volume with PG's relation_byte_size | landed default-off, no MATCH change |
| r115-q96-nonspill-forced-order | Q96 forced-order joins bypass the HashJoin path-cost seam; attribution falsified at that seam | recon only, no code (negative attribution) |
| r116-q96-legacy-join-attribution | Q96 margin not explained by legacy join-method/build-side choice; traced to lower-join cardinality | recon only, no code |
| r117-q96-legacy-child-cost | Q96's 7.68 margin first diverges at lower Hash Join rows/width | recon only, no code |
| r118-q96-lower-join-producers | Q96 lower-join divergence attributed 100% to ANALYZE-measured outer-key nullfrac inputs | recon only, no code |
| r120-hashagg-width-currency | GOOPG_HASHAGG_WIDTH_CURRENCY corrects HashAgg entry-size currency but is net-negative alone; needs ncols narrowing pairing | landed default-off; pairing hypothesis later refuted by r124 |
| r121-narrow-cost-inputs | Slice A: GOOPG_NARROW_COST_INPUTS narrows base-rel cost widths; mechanically correct, parity-inert at this slice | landed default-off; later promoted default-ON in r128 |
| r122-narrow-join-propagation | Slice B: narrowing propagated past first join; first parity gain — TPC-H join-method/scan-type −1 (Q3) | landed default-off, categories improved (no MATCH flip) |
| r123-cost-needed-cols | census: TPC-DS's 42,679 mixed-currency join pairs trace 100% to non-table leaves (CTEScan/Project/SetOp/Filter) lacking stats | recon only, no code |
| r124-nontable-leaf-widths | non-table leaves get narrowed too (mixed bucket → 0), but zero plan/cost movement; refutes r120's pairing hypothesis | landed default-off, no MATCH change; supersedes/refutes r120 pairing rationale |
| r128-parity-over-throughput | promotes GOOPG_NARROW_COST_INPUTS to default-ON; TPC-H join-method/scan-type gain confirmed, throughput cost ~nil | landed, GOOPG_NARROW_COST_INPUTS shipped default-ON |
| r130-q9-joinorder-remeasure | Q9 ground-truth check inverts the premise: goopg's estimate is 1.4x off vs PG's 344x off | withdrawn (premise inverted by ground-truth measurement) |

## statistics/ANALYZE (16 rounds)

| round | title | verdict |
|---|---|---|
| r2-instrument | the instrument, and what it was measuring against (comparator + reference-config fix) | landed, corrected baseline (instrument fix, not a plan change) |
| r4-heap-density | heap page density divergence (findings) | recon only, no code (root cause: page density, not planner) |
| r5-per-type-density | per-type storage size: char(N) padding confirmed, numeric falsified | recon only, no code; correctness bug found (char(N) padding), not parity |
| r30-analyze-physical-order | ANALYZE sample sorted into physical (block,offset) order, fixing correlation stat | landed, category trade (scan-type −1, join-method +2), reproduced on re-baselined heap |
| r31-parallelism-and-heap-density | Parallelism is the dominant axis, blocked on heap density (then: heap reload done, parity unmoved) | investigation; heap reload executed on private clone, confirmed NOT the blocker — parity unmoved |
| r36-baserel-selectivity-reliable-gate | Baserel sizing multiplies unconditionally — drops the reliable early-return gate | landed, net parity improvement (TPC-DS −2, TPC-H −1 categories); largest structural movement so far |
| r55-sort-gap-or-defaults-tiebreak | OR-conjunct inequality + IN-list selectivity arms (P3 of SCOPE §2) | landed, P3 lands (133→26 selectivity fix, no plan regression) |
| r62-yao-parameterized-probe | Decline Yao per-scan evidence under a parameterized probe | landed, all gates PASS (Q11 search PG-exact) |
| r71-q4-semi-selectivity | Q4 rows-vs-election crossover probe: rows theory dead, election program owns Q4 | withdrawn (rows theory DEAD; re-scoped to election program) |
| r78-semi-selectivity | Q4 semi-selectivity probe: model faithful, blocked on ndistinct stats divergence | blocked (re-scope as ndistinct estimator program) |
| r79-ndistinct-sampler | ndistinct-vs-target curves on l_orderkey (both engines): goopg's stats exonerated as the lever | withdrawn (verdict b: keep superior stats, close Q4 elsewhere) |
| r88-q96-unique-key-null | Bare unique keys now retain ordinary null-aware equality selectivity (FK vs UNIQUE split) | landed, no MATCH change (Q96 still shape-diff; narrows gap) |
| r107-q96-common-data-oracle | Q96 projections match but full-relation common-data oracle invalid (PG char/varchar padding vs Goopg trimmed output) | STOP: relation equivalence failed — superseded by r109 |
| r111-q96-common-data-oracle-retry | rebuilt common-data input oracle for Q96 after R110's varchar repair; clears prerequisite for R108 | recon only, no code |
| r118-q96-lower-join-producers | Q96 lower-join divergence attributed 100% to ANALYZE-measured outer-key nullfrac inputs | recon only, no code |
| r119-pg-nullfrac-compare | PG uses the same nullfrac-driven selectivity model as goopg for Q96; margin lives in cost terms above selectivity | adjudicated DIFFER (structural, not a bug) |

## correctness-bug (32 rounds)

| round | title | verdict |
|---|---|---|
| r1-qpqual-index | qpqual currency on index cost sites | landed, no MATCH change (values unmoved) |
| r5-per-type-density | per-type storage size: char(N) padding confirmed, numeric falsified | recon only, no code; correctness bug found (char(N) padding), not parity |
| r8-partial-path-admission | why no partial hash-join path ever wins (GOOPG_GATHER_PATHS off by default) | recon only, no code; blocker found (crash risk), filed to R9 |
| r9-partial-jointype-filter | jointype filter on the partial hash-join producer | landed, no parity movement at default; unblocks R10 |
| r11-adjudicate-gather-walkers | adjudicate the 15 — Gather-blind walker bug found (15→8) | landed (4 walker fixes); flip still reverted |
| r12-remaining-eight | adjudicate the remaining 8 (8→7); "likely defect" was another walker bug | landed (1 more walker fix); flip still reverted |
| r17-multikey-on-real-data | correction: goopg does NOT lose the hash join on real data | recon only, no code — corrects r16; confirms flip near-PG on real data |
| r25-nli-decompose | decompose the NLI node into PG-native operators | landed (slice 1 + CTEScan seam-decline fix) |
| r26-seam-decline-audit | the seam-decline audit: TPC-DS queries that can never match PG | recon only, no code — audit + diagnosis; fix landed in a separate round (R27) |
| r27-outer-join-reduction | Row-dropping outer-join demotion fixed, oracle-verified (applyDemotion reads upperNN not accumulatedNN) | correctness bug fixed (not parity) |
| r40-left-anti-transplant | LEFT→ANTI transplant landed, uncovers latent column-vs-relation-granularity wrong-rows bug (K71) | landed, correctness bug fixed; decline class converted not reduced, no MATCH change |
| r48-semi-joinqual-placement | Filter:(true) EXPLAIN drop + semi JoinQual placement onto NLI probe | landed, both halves LANDED |
| r49-bitmap-probe-param | Parameterize bitmap-heap NLI probe: render Index Cond / move probe qual to BitmapQual | design only (Slice A/B specified; NULL-probe-key correctness bug named for Slice B) |
| r64-nli-right-decline | Decline NLI when probe is the preserved (RIGHT-join) side; fixes Memoize wrong-results bug | landed, correctness bug fixed (Q13 33→34 rows) |
| r69-nli-probe-audit | NLI rescan-startup term landed (missing PG term added to nestloopCost) | landed, MATCH gained (categories reduced on several queries) |
| r73-semi-price-audit | Q4 display-seam audit: no planning site produces the SEMI price 570.66 | recon only, no code (re-scope: search admission needed) |
| r83-limit-above-distinct | LIMIT moved above DISTINCT (Q41): fixes real values-correctness bug plus shape | correctness bug fixed; DS Q41 categories unchanged (3, honest miss on metric) |
| r87-q96-f-prefix-cost | Q96 F-branch: unique-index shortcut drops null-fraction charge vs PG's eqjoinsel_inner | recon only, no code (identifies F1; hands off to R88) |
| r88-q96-unique-key-null | Bare unique keys now retain ordinary null-aware equality selectivity (FK vs UNIQUE split) | landed, no MATCH change (Q96 still shape-diff; narrows gap) |
| r90-inner-unique-hash-final-cost | Inner-unique proof carried into Hash Join final costing (matched-probe term) | landed, no MATCH change (Q96 still shape-diff; cost narrows, no flip) |
| r102-lateral-joined-left-namespace | Grouped-JOIN LATERAL namespace fix needs planner binding, not analyzer-only (temp code reverted) | withdrawn (reverted); boundary identified — superseded by r103 |
| r103-grouped-join-source-binding | USING-lateral output re-resolution still blocks end-to-end fix (LATERAL child loses merged-column map) | withdrawn (reverted); STOP — superseded by r104 |
| r104-grouped-join-using-lateral-output | Grouped JOIN USING column bindings now survive LATERAL re-resolution — lands | correctness bug fixed (not parity); semantic enablement only |
| r109-q96-normalized-relation-witness | Type-normalized witness still fails: Goopg varchar trailing-space data loss in store blocks common-data oracle | STOP: normalized full-relation witness fails (unfixed varchar bug) |
| r110-varchar-trailing-space-fidelity | varchar assignment/COPY coercion now preserves in-range trailing spaces | correctness bug fixed (not parity) |
| r125-tpch-foreign-keys | goopg now consumes NOT VALID foreign keys like PG does; prior defensive decision deliberately reversed | landed, no MATCH change (no corpus yet declares an FK) |
| r126-fk-persistence | foreign keys (and enforcement) now survive a restart; was silently dropping referential integrity | correctness bug fixed (not parity) |
| r50-hash-joinfilter-dedup | Drop hash Join Filter conjuncts already covered by Hash Cond (bpchar whitelist) | design, landing plan specified (Slice A scoped; single-commit landing planned) |
| r54-parallel-admission-step0 | Parallel-admission measurement, fix, regression, and redesign relanding | fix round FAILED BACK TO DESIGN (Q7/Q8 regressed away from PG); redesign re-land + Q7 selectivity fix LANDED |
| r56-sort-placement-gathermerge | Third no-split upper arm: GroupAgg→GatherMerge→Sort worker-sort pricing | landed, prediction lands + in-loop Q78 defect found and fixed |
| r59-index-probe-loopcount | Implement index-side loop-count pro-rating (PG num_scans>1 arm) + NLI enable-gating fix | landed, prediction FAILED (Q7 flipped) but adjudicated as priced-mechanism move; DS Q72 TIMEOUT carried to review |
| r63-resolver-ios-partialagg | Resolver arms: IndexOnlyScan + partial-agg group-key identity for n-distinct chase | landed (estimator gates PASS); commit BLOCKED by pre-existing Q13 Memoize/RightJoin bug found |

## instrument/harness (46 rounds)

| round | title | verdict |
|---|---|---|
| r0-baseline | (no report doc) — raw baseline plan captures (tpch/tpcds diff + plan dumps) | (no doc; superseded by r2) |
| r2-instrument | the instrument, and what it was measuring against (comparator + reference-config fix) | landed, corrected baseline (instrument fix, not a plan change) |
| r8-partial-path-admission | why no partial hash-join path ever wins (GOOPG_GATHER_PATHS off by default) | recon only, no code; blocker found (crash risk), filed to R9 |
| r11-adjudicate-gather-walkers | adjudicate the 15 — Gather-blind walker bug found (15→8) | landed (4 walker fixes); flip still reverted |
| r12-remaining-eight | adjudicate the remaining 8 (8→7); "likely defect" was another walker bug | landed (1 more walker fix); flip still reverted |
| r13-mechanical-and-postpass | mechanical items resolved; post-pass Gather not gated by the knob (findings) | recon only, no code — post-pass design decision needed |
| r31-parallelism-and-heap-density | Parallelism is the dominant axis, blocked on heap density (then: heap reload done, parity unmoved) | investigation; heap reload executed on private clone, confirmed NOT the blocker — parity unmoved |
| r35-join-cardinality-and-metric-blindness | Why estimate rounds measure zero (metric normalisation), plus a join-cardinality estimator defect | recon only, no code; corrects/narrows K49, files join-cardinality defect |
| r37-two-cost-models | goopg has two seq-scan cost models (search vs legacy); Q12 divergence traced to hash-join row WIDTH (K60→K65) | recon only, no code; files K65 (column-pruning/row-width) as new blocker |
| r39-seam-decline-triage | The 8 remaining seam declines triaged to their blockers (B-06, outer-spine, outer-link-no-sjinfo) | recon only, no code |
| r47-q4-upper-rel | Slice-2: no-sort sorted-election measurement for upper ORDER BY/grouping | landed, 16 flips toward PG (Q4 itself did not flip; firing micro-rule still unidentified) |
| r51-implied-equalities-seam | Flip seam to full transitive-equalities closure (K26 §§7-9) | landed, MATCH-neutral (opens candidate set; costing half still open) |
| r53-q9-costing-step0 | Step-0 instrumentation: is Q9's L4 divergence sizing or pricing? | landed (instrument only), pricing identified as cause; scoped follow-up slice defined |
| r54-parallel-admission-step0 | Parallel-admission measurement, fix, regression, and redesign relanding | fix round FAILED BACK TO DESIGN (Q7/Q8 regressed away from PG); redesign re-land + Q7 selectivity fix LANDED |
| r57-join-rows-nlead | Audit of join-rows N-lead residual: units error, not an estimator gap | closed as artifact, no code change (units-error, re-owned to worker-count audit) |
| r58-worker-count-sizing | Audit of worker-count 4-vs-2 divergence: faithful arithmetic, shape-driven | closed as faithful, no code change (re-owned to index-probe pricing audit) |
| r67-materialize-retriage | Re-triage of Materialize producer: still not worth building | withdrawn (re-queued behind join-order/join-method costing) |
| r68-joinorder-costing-step0 | Q9 join-order DPPATH re-measured: NLI wins everywhere, pricing scoped both sides | recon only, no code |
| r74-semi-admission | Q4 fork trace: unnestExistsExpr is where EXISTS bypasses search admission | recon only, no code (recommend admit SEMI to search) |
| r75-semi-admission-slice | Harness-only PASS: SEMI NLI admission needs no new machinery | landed (test-only proof), no code change yet |
| r79-ndistinct-sampler | ndistinct-vs-target curves on l_orderkey (both engines): goopg's stats exonerated as the lever | withdrawn (verdict b: keep superior stats, close Q4 elsewhere) |
| r81-q4-ordered-remeasure | Q4 ordered-election DPPATH re-harvest post-R77: comparator correct as designed | adjudicated DIFFER (structural, blocked on selectivity+width inputs) |
| r82-ds-head-baseline | TPC-DS HEAD baseline (SF0.25) + nearest-miss ranking to pick next round | recon only, no code (selected Q41 as next target) |
| r86-q96-step0 | Q96 partial-row provenance: PG-shaped L3 prefix loses within partial-path tournament (outcome F) | recon only, no code (seeds R87 on L2 selectivity/cost) |
| r89-q96-partial-prefix-cost | Q96 partial-prefix cost attribution: Goopg lacks PG's inner-unique/virtual-bucket cost inputs (outcome C2) | recon only, no code (measurement-only) |
| r92-q96-partial-aggregate-seed | Q96 upper partial-aggregate source barrier (PathPrebuilt representation barrier, not executable) | recon only, no code |
| r93-isolated-partial-prebuilt | PathNestLoop path-kind does not prove executable partial NLI node for Q96 | recon only, no code |
| r96-q96-join-order | Q96 join-order Step-0/P0/P1: bucket-walk hypothesis ATTRIBUTED then FALSIFIED, reframed to inner-unique inputs | recon/attribution chain, no code — superseded (reframed slice, continued in r98) |
| r97-timing-survey | TPC-DS SF0.25 and TPC-H SF1 wall-clock timing survey, goopg vs PG (no code change) | recon only, no code |
| r98-q96-innerunique-inputs | Goopg inner-unique hash-join cost inputs reconcile exactly; PG's equivalent private inputs remain unobservable | adjudicated UNOBSERVABLE, no cost change authorized |
| r99-q96-pg-oracle | Copied Goopg data dir is not a compatible native-PG18 oracle (ANALYZE partly works, PG crashes on Q96) | ORACLE INVALID — superseded by r100 |
| r100-native-pg-q96-oracle | Validated native PG18.3 Q96 plan/value oracle established from stopped reference pgdata copy | oracle validated, no code change |
| r101-q96-forced-order-costs | PG's forced hdem-first vs store-first Q96 loser price observed; Goopg cannot bind the equivalent LATERAL SQL form | recon only — blocked by Goopg LATERAL binding gap (STOP) |
| r105-q96-goopg-forced-order-capture | Repeatable Goopg forced-order Q96 cost capture post-r104 (hdem-first vs store-first) | recon only, no code (measurement capture) |
| r106-q96-forced-margin-attribution | Forced-margin Hash Join cost-term attribution is unobservable from available trace/PG JSON | adjudicated UNOBSERVABLE, no code change |
| r107-q96-common-data-oracle | Q96 projections match but full-relation common-data oracle invalid (PG char/varchar padding vs Goopg trimmed output) | STOP: relation equivalence failed — superseded by r109 |
| r109-q96-normalized-relation-witness | Type-normalized witness still fails: Goopg varchar trailing-space data loss in store blocks common-data oracle | STOP: normalized full-relation witness fails (unfixed varchar bug) |
| r111-q96-common-data-oracle-retry | rebuilt common-data input oracle for Q96 after R110's varchar repair; clears prerequisite for R108 | recon only, no code |
| r112-pg-cost-family-inventory | inventory of PG cost families vs goopg representations; no universal Datum-size substitution defensible | recon only, no code (scoped follow-up: Sort-only) |
| r115-q96-nonspill-forced-order | Q96 forced-order joins bypass the HashJoin path-cost seam; attribution falsified at that seam | recon only, no code (negative attribution) |
| r116-q96-legacy-join-attribution | Q96 margin not explained by legacy join-method/build-side choice; traced to lower-join cardinality | recon only, no code |
| r117-q96-legacy-child-cost | Q96's 7.68 margin first diverges at lower Hash Join rows/width | recon only, no code |
| r121-narrow-cost-inputs | Slice A: GOOPG_NARROW_COST_INPUTS narrows base-rel cost widths; mechanically correct, parity-inert at this slice | landed default-off; later promoted default-ON in r128 |
| r123-cost-needed-cols | census: TPC-DS's 42,679 mixed-currency join pairs trace 100% to non-table leaves (CTEScan/Project/SetOp/Filter) lacking stats | recon only, no code |
| r126-fk-persistence | foreign keys (and enforcement) now survive a restart; was silently dropping referential integrity | correctness bug fixed (not parity) |
| r130-q9-joinorder-remeasure | Q9 ground-truth check inverts the premise: goopg's estimate is 1.4x off vs PG's 344x off | withdrawn (premise inverted by ground-truth measurement) |

## other (15 rounds)

| round | title | verdict |
|---|---|---|
| r4-heap-density | heap page density divergence (findings) | recon only, no code (root cause: page density, not planner) |
| r5-per-type-density | per-type storage size: char(N) padding confirmed, numeric falsified | recon only, no code; correctness bug found (char(N) padding), not parity |
| r20-partial-agg-over-gather | the wiring gap is one guard, and it unifies with K12 | recon only, no code — root cause unified with K12(B); TODO restructured |
| r21-upper-planner-seam | give the upper planner the join rel's paths, not a finished Node | landed through slice 2b (splice fix); slice 3 + flip default still pending |
| r26-seam-decline-audit | the seam-decline audit: TPC-DS queries that can never match PG | recon only, no code — audit + diagnosis; fix landed in a separate round (R27) |
| r28-cte-inlining | goopg materialises CTEs where PG inlines them (111 vs 68 CTE Scan nodes) | recon only, no code |
| r29-escape-guard-binders | Binder-aware escape guard for planHasEscapingOuterRef (structural plan walk) | landed, lateral decline eliminated; scan-type +1 (uncovered pre-existing K33 costing bug), no MATCH change |
| r34-const-fold-preprocess | Parse-time coercion of unknown literals to typed date/time literals | landed, no parity category movement (estimate-only; later shown metric-blind by R35) |
| r44-const-fold-before-selectivity | Fold quals where PG folds them — step A (numeric/general const-fold) + step B (date+interval temporal fold) (K83/K85/K88/K89/K91) | landed (2 steps), net category improvement both corpora (TPC-DS −10, TPC-H −3), no MATCH change; corrected an earlier false "byte-identical" harness claim |
| r35-join-cardinality-and-metric-blindness | Why estimate rounds measure zero (metric normalisation), plus a join-cardinality estimator defect | recon only, no code; corrects/narrows K49, files join-cardinality defect |
| r41-anti-leaf-coordinates | Leaf numbering re-synchronised with binding order (K74/K75), reland after R42 unblocked it | landed, eligibility gain (leaf-count 3→0, declines 8→5), no MATCH change |
| r97-timing-survey | TPC-DS SF0.25 and TPC-H SF1 wall-clock timing survey, goopg vs PG (no code change) | recon only, no code |
| r107-q96-common-data-oracle | Q96 projections match but full-relation common-data oracle invalid (PG char/varchar padding vs Goopg trimmed output) | STOP: relation equivalence failed — superseded by r109 |
| r109-q96-normalized-relation-witness | Type-normalized witness still fails: Goopg varchar trailing-space data loss in store blocks common-data oracle | STOP: normalized full-relation witness fails (unfixed varchar bug) |
| r125-tpch-foreign-keys | goopg now consumes NOT VALID foreign keys like PG does; prior defensive decision deliberately reversed | landed, no MATCH change (no corpus yet declares an FK) |

