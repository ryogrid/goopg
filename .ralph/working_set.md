Task: M0145-0008 (cutover), legacy-deletion slice 2 — DONE and committed:
the legacy pipeline and GOOPG_JOINTREE_PIPELINE are deleted (Movement: none).
Files: internal/optimizer/{planner.go,joinsearchseam.go,collapse.go,
specialjoin.go,flaglabels.go}; jointreepipeline.go(+_test) and
pipeline_pin_test.go deleted; about 25 test files unpinned or trimmed;
scripts/planner-flags.env regenerated; scripts/tpcds-sf025-regression.sh
(SF025_FLOW_KNOB now defaults to 0); design doc
m0145-0008-del2-legacy-pipeline-deleted.md + README + flip doc;
analysis/m0145/m0145-0008-del2/ (gates, deadcode before/after).
Key symbols: planSelectWithSettings (now the body), tryPGShapedJoinSearch
(floor const 1, spine always empty), flagProvenanceRetired.
Findings:
- Legacy-pinned tests were run unpinned: 32 failed, all on legacy-only shapes
  (hash keys, SubPlan refusals PG no longer makes, the floor-2 decline).
  Deleted, except the 2 correlated-IN pins, which were rewritten: PG pulls
  a correlated IN up as a LATERAL semi join, and the new pin checks that
  both conjuncts are kept.
- deadcode: 20 newly unreachable optimizer functions (list in the design doc
  and analysis/m0145/m0145-0008-del2/deadcode-after.txt).
Next step: legacy-deletion slice 3 (dead code). Delete those 20 functions and
their test references (about 20 test files: predp_test, enclosingtree_test,
joinsearchspine_test, collapse_corpus_test, onerelsearch_test, ...). Retire
GOOPG_ONEREL_SEARCH and GOOPG_UNNEST_PREDP through flagProvenanceRetired and
regenerate planner-flags.env. Simplify the spine-aware code in
tryPGShapedJoinSearch (len(spine)==0). Delete the sf025 SF025_FLOW_KNOB block
and the scripts' JOINTREE vars. Rerun deadcode to confirm; fires should stay
none. Then the seam guards on M0145-0001's retired list.
Gates run: units PASS; tpch-spotcheck PASS (Q12=2, Q13=33); fireset HEAD vs
staged: fires=none at SF0.25 and SF1, PASS; sf025 sweep 96/96, shapes 99/99
same; acceptance arm 24 MATCH vs arm-on-20260922-loop77; ea-ratchet 52/52.
Selection: M0145-0008 parent (topmost in item 3; owner GO'd the deletion
slices). 0008m (wrong results, S2) is still waiting for owner placement.
In-flight: none
