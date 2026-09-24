Task: M0145-0008 (cutover), legacy-deletion slice 1. Done and committed:
the fire-set gate now compares HEAD with the staged tree.
Files: scripts/tpcds-fireset-gate.sh;
docs/design/0100-0149/m0145-0008-del1-fireset-head-vs-staged.md (+ README row,
cutover-flip doc "Remaining"); analysis/m0145/m0145-0008-del1/; fix_plan
M0145-0008 sub-bullet.
Key symbols: BASELINE_REV (default HEAD, git archive go.mod go.sum cmd internal),
CANDIDATE_BIN / BASELINE_BIN (tmp/fireset-bin/<label>-*), run_arm (now takes a
bin), remove_clone, FIRESET_KEEP_CLONES.
Findings:
- The old design (one binary, JOINTREE 0 vs 1) measured legacy vs jointree,
  not the change. Deleting the knob would have made it derive zero fires, so
  the redesign had to come first.
- A/A at SF0.25+SF1: fires=none. Non-vacuity vs bb90e51a4^: 8 fires,
  introduced=none.
- 132 stale fire-set execution clones (262 GB) deleted; disk 89% -> 62%.
  8 recent ones (< 1 h, maybe a concurrent session's) were left.
Selection note: the ties rule puts the M0145-0008 parent (topmost in file
order in item 3; owner GO on the legacy-deletion slices) ahead of its
children 0008i/j/k/l. 0008m (wrong results, S2) is still waiting for the
owner to place it.
Next step: legacy-deletion slice 2. Delete planSelectLegacyPipeline and the
`jointree` param of planSelectImpl (planner.go:1182-1202, sites 1622, 1780,
1883, 1944, 1960, 2059, 2091, 5896), plus the jointreePipeline branches in
joinsearchseam.go:261/291/328/408, collapse.go:396/411 and specialjoin.go:335.
Delete the legacy-only machinery that becomes dead (pre-DP unnest, pinned
spine) and the tests pinned via SetJointreePipeline("0") (about 20 test files,
grep SetJointreePipeline|pinLegacyPipeline). Then retire the knob with
flagProvenanceRetired and drop the sf025 legacy control pass. Every such
commit is in fire-set scope, and the gate now shows exactly which plans
the deletion moves.
Gates run: fire-set gate A/A (PASS, both scales) and the non-vacuity run
(PASS). bash -n. No Go code changed, so the unit suite was not run; the
commit hook ran the pgbench smoke.
In-flight: none
