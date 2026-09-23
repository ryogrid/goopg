Task: M0141-S2b-4 (UNION distinct parity) — S2b-4a landed ded1b8db3.
Next open slices: M0141-S2b-4b (Gather variants only; the serial election
landed with 4a), M0141-S2b-4c (Merge Append arm, witness Q75),
M0141-S2b-4d (hashed Distinct HashAggregate label + DISTINCT election; needs
DISTINCT candidates carried to the ordered rel; Q41 floor match at stake).
Files (4a): internal/optimizer/planner.go applySetOp (unionFolds/leavesOf),
windowsetoppaths.go createUnionDistinctPaths, distinctpaths.go
distinctCandidates / addUnionDistinctPaths, operators_explain.go (label note).
Findings: see design doc m0141-s2b-4-union-distinct-decomposition.md
§"S2b-4a landed" and §"What was tried and held back". Floor: TPC-DS SF0.25
default arm match=2 (Q9, Q41) must hold; Q41's match currently rests on the
old `Unique` label.
Next step: pick 4c (check whether goopg has a Merge Append path/executor;
Q75 PG plans Unique -> Merge Append over Gather Merge children) or 4b's
Gather variants, per the banner's order within M0141.
Gates run (4a): units, full regress suite, spotcheck, sf025 (FORCE=1, nightly
running), acceptance arm (values-only FORCE=1), fire-set — PASS.
In-flight: none. Note: the nightly batch's log commit (chore(ci)) kept
failing/retrying during this loop; its ci/logs files sit staged in the index
— never commit them under a loop task (use pathspec).
Owner calls pending: M0145-0008, M0145-0012, M0141-S2b-17a (G4 repin).
