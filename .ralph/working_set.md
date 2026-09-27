Task: M0146-0027 slice 6 — ordered-PLAIN arm for ungrouped ordered/distinct
aggregates (Q28's `PG Gather Merge | goopg Gather` record). COMPLETE pending
commit.

Files: internal/optimizer/partialaggupper.go (PLAIN arm presorted branch +
shared workerSortGatherMergePath helper), partialaggupper_test.go (3 tests),
docs/design/0100-0149/m0146-0027-sorted-partial-gather-merge.md (slice-6
section), docs/design/README.md (row), .ralph/fix_plan.md (slice-6 block +
Movement), analysis/m0146/m0146-0027/slice6/ (evidence).

Key symbols: addPartialAggSplitPath, workerSortGatherMergePath,
presortedAggKeysOrAbsent, partialAggGatherMergeProducer.

Findings: Q28 = full MATCH vs PG at SF0.25 AND SF1 (1 row,
ck=58f05f6812160030 oracle-exact). Q16 agg input gained PG's
`Sort -> Gather`; record shrank 6->3 categories (still MISSING-NODE on the
pre-existing join spine). Census: SF0.25 match 11->12 divergent 88->87
(parallelism 53->52); SF1 match 13->14 divergent 86->85.

Next step: stage explicit paths + commit + push (commit message drafted:
"optimizer(M0146-0027): ordered-PLAIN arm — Gather Merge -> Sort under
ungrouped ordered aggregates (slice 6)"). Then re-check the banner;
remaining parallelism records are Q17/Q25/Q29 (routed M0142-0005c) and
Q10/Q66 (reverse-direction election — costing, not reach).

Gates run: tpch-spotcheck PASS (Q12=2 Q13=33), tpcds-sf025 sweep PASS
(96/0, plans changed Q16/Q28 both toward PG), tpch-acceptance-arm PASS
(24 MATCH), tpcds-fireset PASS (fires Q16/Q28 both arms both scales, no
new timeouts), units PASS.

In-flight: none (private clone :5590 stopped).
