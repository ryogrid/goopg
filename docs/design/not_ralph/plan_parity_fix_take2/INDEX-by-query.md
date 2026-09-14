# INDEX-by-query.md — round corpus cross-reference (built by M0137-0008)

*Generated 2026-09-15 from all 126 round directories under this directory (`r0-baseline` .. `r130-q9-joinorder-remeasure`; r18/r23/r24/r80/r114/r129 do not exist as round dirs and are not gaps in this index). One row per query that at least one round investigated as its subject; rounds are listed oldest-first. This is a reference index, not a narrative — read a cited round's own REPORT.md/DESIGN.md for detail. Cross-check against `METHODOLOGY3/01-what-we-learned.md` (durable findings) and `02-open-problems.md` (open blockers) before treating any single row here as the full picture; this index answers "which rounds touched query X", not "what is currently true about query X".*

## TPC-H

| query | rounds (oldest→newest) | last verdict on record |
|---|---|---|
| Q1 | r2-instrument, r43-parallel-hash, r45-gather-merge-ordered-finalize, r54-parallel-admission-step0, r55-sort-gap-or-defaults-tiebreak, r56-sort-placement-gathermerge, r59-index-probe-loopcount, r60-partial-nestloop-producer, r63-resolver-ios-partialagg, r65-q11-explain-render, r97-timing-survey | r97-timing-survey: recon only, no code |
| Q2 | r49-bitmap-probe-param, r69-nli-probe-audit | r69-nli-probe-audit: landed, MATCH gained (categories reduced on several queries) |
| Q3 | r3-hashagg-spill, r54-parallel-admission-step0, r55-sort-gap-or-defaults-tiebreak, r56-sort-placement-gathermerge, r59-index-probe-loopcount, r60-partial-nestloop-producer, r62-yao-parameterized-probe, r65-q11-explain-render, r69-nli-probe-audit, r122-narrow-join-propagation, r128-parity-over-throughput | r128-parity-over-throughput: landed, GOOPG_NARROW_COST_INPUTS shipped default-ON |
| Q4 | r25-nli-decompose, r43-parallel-hash, r47-q4-upper-rel, r48-semi-joinqual-placement, r71-q4-semi-selectivity, r72-election-step0, r73-semi-price-audit, r74-semi-admission, r75-semi-admission-slice, r76-nli-price-splice, r77-production-splice, r78-semi-selectivity, r79-ndistinct-sampler, r81-q4-ordered-remeasure, r97-timing-survey, r127-semijoin-selectivity | r127-semijoin-selectivity: withdrawn (9 findings, 3 fatal, all refuted by prior evidence) |
| Q5 | r19-aggregation-strategy, r22-plain-index-arm, r49-bitmap-probe-param, r51-implied-equalities-seam, r52-r51-shape-adjudication, r54-parallel-admission-step0, r55-sort-gap-or-defaults-tiebreak, r56-sort-placement-gathermerge, r59-index-probe-loopcount, r60-partial-nestloop-producer, r61-groupcount-search-input, r62-yao-parameterized-probe, r63-resolver-ios-partialagg, r65-q11-explain-render, r67-materialize-retriage, r69-nli-probe-audit | r69-nli-probe-audit: landed, MATCH gained (categories reduced on several queries) |
| Q6 | r2-instrument, r33-subquery-parallel-pass, r43-parallel-hash, r44-const-fold-before-selectivity, r63-resolver-ios-partialagg, r68-joinorder-costing-step0, r70-hash-footprint, r72-election-step0, r97-timing-survey | r97-timing-survey: recon only, no code |
| Q7 | r47-q4-upper-rel, r54-parallel-admission-step0, r55-sort-gap-or-defaults-tiebreak, r56-sort-placement-gathermerge, r57-join-rows-nlead, r58-worker-count-sizing, r59-index-probe-loopcount, r60-partial-nestloop-producer, r65-q11-explain-render, r66-alias-source-keys, r69-nli-probe-audit, r97-timing-survey | r97-timing-survey: recon only, no code |
| Q8 | r47-q4-upper-rel, r49-bitmap-probe-param, r54-parallel-admission-step0, r55-sort-gap-or-defaults-tiebreak, r56-sort-placement-gathermerge, r59-index-probe-loopcount, r60-partial-nestloop-producer, r66-alias-source-keys, r67-materialize-retriage, r69-nli-probe-audit, r97-timing-survey | r97-timing-survey: recon only, no code |
| Q9 | r2-instrument, r14-adjudicate-against-pg, r19-aggregation-strategy, r21-upper-planner-seam, r43-parallel-hash, r51-implied-equalities-seam, r52-r51-shape-adjudication, r53-q9-costing-step0, r54-parallel-admission-step0, r55-sort-gap-or-defaults-tiebreak, r56-sort-placement-gathermerge, r59-index-probe-loopcount, r60-partial-nestloop-producer, r65-q11-explain-render, r66-alias-source-keys, r68-joinorder-costing-step0, r69-nli-probe-audit, r70-hash-footprint, r86-q96-step0, r88-q96-unique-key-null, r91-pg-virtual-bucket-unmatched, r97-timing-survey, r108-pg-hash-tuple-sizing, r125-tpch-foreign-keys, r126-fk-persistence, r130-q9-joinorder-remeasure | r130-q9-joinorder-remeasure: withdrawn (premise inverted by ground-truth measurement) |
| Q10 | r3-hashagg-spill, r43-parallel-hash, r47-q4-upper-rel, r54-parallel-admission-step0, r55-sort-gap-or-defaults-tiebreak, r56-sort-placement-gathermerge, r59-index-probe-loopcount, r60-partial-nestloop-producer, r65-q11-explain-render, r66-alias-source-keys, r120-hashagg-width-currency, r121-narrow-cost-inputs | r121-narrow-cost-inputs: landed default-off; later promoted default-ON in r128 |
| Q11 | r49-bitmap-probe-param, r61-groupcount-search-input, r62-yao-parameterized-probe, r63-resolver-ios-partialagg, r64-nli-right-decline, r65-q11-explain-render, r69-nli-probe-audit, r97-timing-survey | r97-timing-survey: recon only, no code |
| Q12 | r1-qpqual-index, r19-aggregation-strategy, r21-index-leaf-qpqual, r35-join-cardinality-and-metric-blindness, r36-baserel-selectivity-reliable-gate, r37-two-cost-models, r38-reltarget-width, r46-legacy-index-seq-competition, r64-nli-right-decline | r64-nli-right-decline: landed, correctness bug fixed (Q13 33→34 rows) |
| Q13 | r3-hashagg-spill, r21-upper-planner-seam, r43-parallel-hash, r46-legacy-index-seq-competition, r47-q4-upper-rel, r63-resolver-ios-partialagg, r64-nli-right-decline, r65-q11-explain-render, r66-alias-source-keys, r68-joinorder-costing-step0, r70-hash-footprint, r72-election-step0, r77-production-splice, r97-timing-survey | r97-timing-survey: recon only, no code |
| Q14 | r2-instrument, r7-parallel-aware-label, r33-subquery-parallel-pass, r43-parallel-hash, r44-const-fold-before-selectivity, r45-gather-merge-ordered-finalize | r45-gather-merge-ordered-finalize: design rejected on review (architecturally impossible: partial agg is row-less); diagnosis stands, filed as multi-round executor item (K97) |
| Q15a | r51-implied-equalities-seam, r52-r51-shape-adjudication | r52-r51-shape-adjudication: recon only, no code (mostly TOWARD/MIXED verdicts named) |
| Q15b | r65-q11-explain-render | r65-q11-explain-render: landed, Q11 reaches MATCH, all gates hold |
| Q16 | r43-parallel-hash, r47-q4-upper-rel, r65-q11-explain-render, r66-alias-source-keys | r66-alias-source-keys: landed (both slices), rendering set reduced to {Q10} only |
| Q17 | r69-nli-probe-audit, r85-execparam-display, r97-timing-survey | r97-timing-survey: recon only, no code |
| Q18 | r3-hashagg-spill, r43-parallel-hash, r65-q11-explain-render, r69-nli-probe-audit, r97-timing-survey, r128-parity-over-throughput | r128-parity-over-throughput: landed, GOOPG_NARROW_COST_INPUTS shipped default-ON |
| Q19 | r2-instrument, r19-aggregation-strategy, r25-nli-decompose, r54-parallel-admission-step0, r55-sort-gap-or-defaults-tiebreak, r56-sort-placement-gathermerge, r59-index-probe-loopcount, r60-partial-nestloop-producer, r97-timing-survey, r128-parity-over-throughput | r128-parity-over-throughput: landed, GOOPG_NARROW_COST_INPUTS shipped default-ON |
| Q20 | r25-nli-decompose, r49-bitmap-probe-param, r85-execparam-display, r97-timing-survey, r128-parity-over-throughput | r128-parity-over-throughput: landed, GOOPG_NARROW_COST_INPUTS shipped default-ON |
| Q21 | r43-parallel-hash, r47-q4-upper-rel, r48-semi-joinqual-placement, r74-semi-admission, r76-nli-price-splice, r77-production-splice, r97-timing-survey | r97-timing-survey: recon only, no code |
| Q22 | r25-nli-decompose, r33-subquery-parallel-pass, r47-q4-upper-rel, r65-q11-explain-render, r66-alias-source-keys, r68-joinorder-costing-step0, r70-hash-footprint, r72-election-step0, r74-semi-admission, r76-nli-price-splice, r77-production-splice | r77-production-splice: landed, no MATCH change (categories same, costs now PG-scale) |

## TPC-DS

| query | rounds (oldest→newest) | last verdict on record |
|---|---|---|
| Q1 | r32-targetlist-subplan-display, r69-nli-probe-audit | r69-nli-probe-audit: landed, MATCH gained (categories reduced on several queries) |
| Q3 | r47-q4-upper-rel, r49-bitmap-probe-param, r61-groupcount-search-input | r61-groupcount-search-input: landed, all gates PASS |
| Q4 | r50-hash-joinfilter-dedup, r51-implied-equalities-seam, r52-r51-shape-adjudication, r56-sort-placement-gathermerge | r56-sort-placement-gathermerge: landed, prediction lands + in-loop Q78 defect found and fixed |
| Q5 | r8-partial-path-admission, r9-partial-jointype-filter, r34-const-fold-preprocess | r34-const-fold-preprocess: landed, no parity category movement (estimate-only; later shown metric-blind by R35) |
| Q6 | r85-execparam-display, r122-narrow-join-propagation, r124-nontable-leaf-widths | r124-nontable-leaf-widths: landed default-off, no MATCH change; supersedes/refutes r120 pairing rationale |
| Q7 | r47-q4-upper-rel, r82-ds-head-baseline | r82-ds-head-baseline: recon only, no code (selected Q41 as next target) |
| Q8 | r22-plain-index-arm, r97-timing-survey | r97-timing-survey: recon only, no code |
| Q9 | r32-targetlist-subplan-display, r33-subquery-parallel-pass, r46-legacy-index-seq-competition | r46-legacy-index-seq-competition: landed, MATCH gained (TPC-DS Q9 — programme's first TPC-DS match) |
| Q10 | r47-q4-upper-rel | r47-q4-upper-rel: landed, 16 flips toward PG (Q4 itself did not flip; firing micro-rule still unidentified) |
| Q11 | r50-hash-joinfilter-dedup, r51-implied-equalities-seam, r52-r51-shape-adjudication, r56-sort-placement-gathermerge | r56-sort-placement-gathermerge: landed, prediction lands + in-loop Q78 defect found and fixed |
| Q12 | r2-instrument, r3-hashagg-spill, r34-const-fold-preprocess | r34-const-fold-preprocess: landed, no parity category movement (estimate-only; later shown metric-blind by R35) |
| Q15 | r47-q4-upper-rel, r62-yao-parameterized-probe | r62-yao-parameterized-probe: landed, all gates PASS (Q11 search PG-exact) |
| Q16 | r34-const-fold-preprocess | r34-const-fold-preprocess: landed, no parity category movement (estimate-only; later shown metric-blind by R35) |
| Q17 | r47-q4-upper-rel, r69-nli-probe-audit | r69-nli-probe-audit: landed, MATCH gained (categories reduced on several queries) |
| Q19 | r47-q4-upper-rel, r49-bitmap-probe-param | r49-bitmap-probe-param: design only (Slice A/B specified; NULL-probe-key correctness bug named for Slice B) |
| Q20 | r34-const-fold-preprocess | r34-const-fold-preprocess: landed, no parity category movement (estimate-only; later shown metric-blind by R35) |
| Q21 | r34-const-fold-preprocess, r49-bitmap-probe-param | r49-bitmap-probe-param: design only (Slice A/B specified; NULL-probe-key correctness bug named for Slice B) |
| Q24 | r50-hash-joinfilter-dedup, r97-timing-survey | r97-timing-survey: recon only, no code |
| Q25 | r47-q4-upper-rel, r51-implied-equalities-seam, r52-r51-shape-adjudication, r82-ds-head-baseline | r82-ds-head-baseline: recon only, no code (selected Q41 as next target) |
| Q26 | r47-q4-upper-rel, r82-ds-head-baseline | r82-ds-head-baseline: recon only, no code (selected Q41 as next target) |
| Q27 | r69-nli-probe-audit | r69-nli-probe-audit: landed, MATCH gained (categories reduced on several queries) |
| Q28 | r82-ds-head-baseline | r82-ds-head-baseline: recon only, no code (selected Q41 as next target) |
| Q29 | r47-q4-upper-rel, r82-ds-head-baseline | r82-ds-head-baseline: recon only, no code (selected Q41 as next target) |
| Q30 | r25-nli-decompose, r49-bitmap-probe-param | r49-bitmap-probe-param: design only (Slice A/B specified; NULL-probe-key correctness bug named for Slice B) |
| Q31 | r51-implied-equalities-seam, r52-r51-shape-adjudication, r56-sort-placement-gathermerge | r56-sort-placement-gathermerge: landed, prediction lands + in-loop Q78 defect found and fixed |
| Q32 | r32-targetlist-subplan-display, r34-const-fold-preprocess, r49-bitmap-probe-param, r97-timing-survey | r97-timing-survey: recon only, no code |
| Q33 | r47-q4-upper-rel | r47-q4-upper-rel: landed, 16 flips toward PG (Q4 itself did not flip; firing micro-rule still unidentified) |
| Q35 | r47-q4-upper-rel, r69-nli-probe-audit | r69-nli-probe-audit: landed, MATCH gained (categories reduced on several queries) |
| Q36 | r34-const-fold-preprocess | r34-const-fold-preprocess: landed, no parity category movement (estimate-only; later shown metric-blind by R35) |
| Q37 | r34-const-fold-preprocess, r47-q4-upper-rel, r49-bitmap-probe-param, r62-yao-parameterized-probe | r62-yao-parameterized-probe: landed, all gates PASS (Q11 search PG-exact) |
| Q38 | r2-instrument, r44-const-fold-before-selectivity | r44-const-fold-before-selectivity: landed (2 steps), net category improvement both corpora (TPC-DS −10, TPC-H −3), no MATCH change; corrected an earlier false "byte-identical" harness claim |
| Q39 | r49-bitmap-probe-param, r61-groupcount-search-input, r62-yao-parameterized-probe | r62-yao-parameterized-probe: landed, all gates PASS (Q11 search PG-exact) |
| Q40 | r34-const-fold-preprocess, r47-q4-upper-rel, r49-bitmap-probe-param, r61-groupcount-search-input | r61-groupcount-search-input: landed, all gates PASS |
| Q41 | r82-ds-head-baseline, r83-limit-above-distinct, r84-distinct-outer-sort-skip, r85-execparam-display | r85-execparam-display: landed, MATCH gained (DS match 1→2, Q41 closed) |
| Q42 | r47-q4-upper-rel, r49-bitmap-probe-param | r49-bitmap-probe-param: design only (Slice A/B specified; NULL-probe-key correctness bug named for Slice B) |
| Q43 | r97-timing-survey | r97-timing-survey: recon only, no code |
| Q44 | r2-instrument, r33-subquery-parallel-pass, r46-legacy-index-seq-competition | r46-legacy-index-seq-competition: landed, MATCH gained (TPC-DS Q9 — programme's first TPC-DS match) |
| Q45 | r47-q4-upper-rel, r62-yao-parameterized-probe | r62-yao-parameterized-probe: landed, all gates PASS (Q11 search PG-exact) |
| Q47 | r42-pushdown-project-arm, r50-hash-joinfilter-dedup, r52-r51-shape-adjudication, r56-sort-placement-gathermerge, r61-groupcount-search-input | r61-groupcount-search-input: landed, all gates PASS |
| Q49 | r26-seam-decline-audit, r49-bitmap-probe-param | r49-bitmap-probe-param: design only (Slice A/B specified; NULL-probe-key correctness bug named for Slice B) |
| Q50 | r47-q4-upper-rel | r47-q4-upper-rel: landed, 16 flips toward PG (Q4 itself did not flip; firing micro-rule still unidentified) |
| Q51 | r26-seam-decline-audit, r56-sort-placement-gathermerge | r56-sort-placement-gathermerge: landed, prediction lands + in-loop Q78 defect found and fixed |
| Q52 | r47-q4-upper-rel, r49-bitmap-probe-param | r49-bitmap-probe-param: design only (Slice A/B specified; NULL-probe-key correctness bug named for Slice B) |
| Q53 | r49-bitmap-probe-param | r49-bitmap-probe-param: design only (Slice A/B specified; NULL-probe-key correctness bug named for Slice B) |
| Q54 | r47-q4-upper-rel | r47-q4-upper-rel: landed, 16 flips toward PG (Q4 itself did not flip; firing micro-rule still unidentified) |
| Q55 | r47-q4-upper-rel, r49-bitmap-probe-param, r97-timing-survey | r97-timing-survey: recon only, no code |
| Q56 | r47-q4-upper-rel, r50-hash-joinfilter-dedup | r50-hash-joinfilter-dedup: design, landing plan specified (Slice A scoped; single-commit landing planned) |
| Q57 | r42-pushdown-project-arm, r50-hash-joinfilter-dedup, r52-r51-shape-adjudication, r56-sort-placement-gathermerge, r61-groupcount-search-input | r61-groupcount-search-input: landed, all gates PASS |
| Q58 | r50-hash-joinfilter-dedup, r97-timing-survey | r97-timing-survey: recon only, no code |
| Q59 | r50-hash-joinfilter-dedup, r56-sort-placement-gathermerge | r56-sort-placement-gathermerge: landed, prediction lands + in-loop Q78 defect found and fixed |
| Q60 | r47-q4-upper-rel, r50-hash-joinfilter-dedup | r50-hash-joinfilter-dedup: design, landing plan specified (Slice A scoped; single-commit landing planned) |
| Q61 | r49-bitmap-probe-param, r97-timing-survey | r97-timing-survey: recon only, no code |
| Q63 | r49-bitmap-probe-param | r49-bitmap-probe-param: design only (Slice A/B specified; NULL-probe-key correctness bug named for Slice B) |
| Q64 | r49-bitmap-probe-param, r51-implied-equalities-seam, r52-r51-shape-adjudication, r61-groupcount-search-input, r122-narrow-join-propagation, r124-nontable-leaf-widths, r128-parity-over-throughput | r128-parity-over-throughput: landed, GOOPG_NARROW_COST_INPUTS shipped default-ON |
| Q65 | r67-materialize-retriage | r67-materialize-retriage: withdrawn (re-queued behind join-order/join-method costing) |
| Q66 | r47-q4-upper-rel | r47-q4-upper-rel: landed, 16 flips toward PG (Q4 itself did not flip; firing micro-rule still unidentified) |
| Q68 | r26-seam-decline-audit, r29-escape-guard-binders | r29-escape-guard-binders: landed, lateral decline eliminated; scan-type +1 (uncovered pre-existing K33 costing bug), no MATCH change |
| Q69 | r47-q4-upper-rel, r69-nli-probe-audit | r69-nli-probe-audit: landed, MATCH gained (categories reduced on several queries) |
| Q70 | r34-const-fold-preprocess | r34-const-fold-preprocess: landed, no parity category movement (estimate-only; later shown metric-blind by R35) |
| Q71 | r47-q4-upper-rel, r97-timing-survey | r97-timing-survey: recon only, no code |
| Q72 | r47-q4-upper-rel, r51-implied-equalities-seam, r52-r51-shape-adjudication, r59-index-probe-loopcount | r59-index-probe-loopcount: landed, prediction FAILED (Q7 flipped) but adjudicated as priced-mechanism move; DS Q72 TIMEOUT carried to review |
| Q74 | r46-legacy-index-seq-competition, r50-hash-joinfilter-dedup, r61-groupcount-search-input | r61-groupcount-search-input: landed, all gates PASS |
| Q75 | r49-bitmap-probe-param, r122-narrow-join-propagation, r124-nontable-leaf-widths, r128-parity-over-throughput | r128-parity-over-throughput: landed, GOOPG_NARROW_COST_INPUTS shipped default-ON |
| Q76 | r47-q4-upper-rel, r49-bitmap-probe-param | r49-bitmap-probe-param: design only (Slice A/B specified; NULL-probe-key correctness bug named for Slice B) |
| Q77 | r26-seam-decline-audit, r28-cte-inlining, r34-const-fold-preprocess, r69-nli-probe-audit | r69-nli-probe-audit: landed, MATCH gained (categories reduced on several queries) |
| Q78 | r26-seam-decline-audit, r28-cte-inlining, r40-left-anti-transplant, r41-anti-leaf-coordinates, r42-pushdown-project-arm, r56-sort-placement-gathermerge | r56-sort-placement-gathermerge: landed, prediction lands + in-loop Q78 defect found and fixed |
| Q79 | r22-plain-index-arm, r61-groupcount-search-input | r61-groupcount-search-input: landed, all gates PASS |
| Q80 | r34-const-fold-preprocess, r49-bitmap-probe-param, r97-timing-survey | r97-timing-survey: recon only, no code |
| Q81 | r2-instrument, r3-hashagg-spill, r25-nli-decompose, r32-targetlist-subplan-display, r49-bitmap-probe-param | r49-bitmap-probe-param: design only (Slice A/B specified; NULL-probe-key correctness bug named for Slice B) |
| Q82 | r34-const-fold-preprocess, r47-q4-upper-rel, r49-bitmap-probe-param, r62-yao-parameterized-probe | r62-yao-parameterized-probe: landed, all gates PASS (Q11 search PG-exact) |
| Q83 | r50-hash-joinfilter-dedup | r50-hash-joinfilter-dedup: design, landing plan specified (Slice A scoped; single-commit landing planned) |
| Q84 | r51-implied-equalities-seam, r52-r51-shape-adjudication, r54-parallel-admission-step0, r55-sort-gap-or-defaults-tiebreak, r59-index-probe-loopcount | r59-index-probe-loopcount: landed, prediction FAILED (Q7 flipped) but adjudicated as priced-mechanism move; DS Q72 TIMEOUT carried to review |
| Q85 | r47-q4-upper-rel | r47-q4-upper-rel: landed, 16 flips toward PG (Q4 itself did not flip; firing micro-rule still unidentified) |
| Q86 | r34-const-fold-preprocess | r34-const-fold-preprocess: landed, no parity category movement (estimate-only; later shown metric-blind by R35) |
| Q87 | r44-const-fold-before-selectivity | r44-const-fold-before-selectivity: landed (2 steps), net category improvement both corpora (TPC-DS −10, TPC-H −3), no MATCH change; corrected an earlier false "byte-identical" harness claim |
| Q88 | r97-timing-survey | r97-timing-survey: recon only, no code |
| Q89 | r49-bitmap-probe-param | r49-bitmap-probe-param: design only (Slice A/B specified; NULL-probe-key correctness bug named for Slice B) |
| Q91 | r47-q4-upper-rel, r82-ds-head-baseline | r82-ds-head-baseline: recon only, no code (selected Q41 as next target) |
| Q92 | r32-targetlist-subplan-display, r34-const-fold-preprocess | r34-const-fold-preprocess: landed, no parity category movement (estimate-only; later shown metric-blind by R35) |
| Q93 | r26-seam-decline-audit, r47-q4-upper-rel | r47-q4-upper-rel: landed, 16 flips toward PG (Q4 itself did not flip; firing micro-rule still unidentified) |
| Q94 | r34-const-fold-preprocess | r34-const-fold-preprocess: landed, no parity category movement (estimate-only; later shown metric-blind by R35) |
| Q95 | r34-const-fold-preprocess, r122-narrow-join-propagation, r124-nontable-leaf-widths, r128-parity-over-throughput | r128-parity-over-throughput: landed, GOOPG_NARROW_COST_INPUTS shipped default-ON |
| Q96 | r2-instrument, r4-heap-density, r82-ds-head-baseline, r86-q96-step0, r87-q96-f-prefix-cost, r88-q96-unique-key-null, r89-q96-partial-prefix-cost, r90-inner-unique-hash-final-cost, r91-pg-virtual-bucket-unmatched, r92-q96-partial-aggregate-seed, r93-isolated-partial-prebuilt, r94-partial-plain-nestloop, r95-lateral-probe-workers, r96-q96-join-order, r97-timing-survey, r98-q96-innerunique-inputs, r99-q96-pg-oracle, r100-native-pg-q96-oracle, r101-q96-forced-order-costs, r102-lateral-joined-left-namespace, r103-grouped-join-source-binding, r104-grouped-join-using-lateral-output, r105-q96-goopg-forced-order-capture, r106-q96-forced-margin-attribution, r107-q96-common-data-oracle, r108-pg-hash-tuple-sizing, r109-q96-normalized-relation-witness, r111-q96-common-data-oracle-retry, r115-q96-nonspill-forced-order, r116-q96-legacy-join-attribution, r117-q96-legacy-child-cost, r118-q96-lower-join-producers, r119-pg-nullfrac-compare | r119-pg-nullfrac-compare: adjudicated DIFFER (structural, not a bug) |
| Q97 | r26-seam-decline-audit, r56-sort-placement-gathermerge | r56-sort-placement-gathermerge: landed, prediction lands + in-loop Q78 defect found and fixed |
| Q98 | r34-const-fold-preprocess, r49-bitmap-probe-param | r49-bitmap-probe-param: design only (Slice A/B specified; NULL-probe-key correctness bug named for Slice B) |
| Q99 | r44-const-fold-before-selectivity | r44-const-fold-before-selectivity: landed (2 steps), net category improvement both corpora (TPC-DS −10, TPC-H −3), no MATCH change; corrected an earlier false "byte-identical" harness claim |

## Corpus-wide (no single query named as subject)

Rounds whose extraction found no single-query subject (census/instrument/design-only rounds spanning the whole corpus, or rounds where every mentioned query was contextual rather than the round's subject): r0-baseline, r5-per-type-density, r6-window-sort, r10-gather-paths-default, r11-adjudicate-gather-walkers, r12-remaining-eight, r13-mechanical-and-postpass, r15-multikey-adjudication, r16-pg-multikey-verdict, r17-multikey-on-real-data, r20-partial-agg-over-gather, r27-outer-join-reduction, r30-analyze-physical-order, r31-parallelism-and-heap-density, r39-seam-decline-triage, r110-varchar-trailing-space-fidelity, r112-pg-cost-family-inventory, r113-pg-sort-relation-bytes, r123-cost-needed-cols.

