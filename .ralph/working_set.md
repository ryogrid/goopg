Task: M0145-0008 (cutover), legacy-deletion slice 3 — DONE and committed:
the code only the legacy pipeline reached is deleted (Movement: none).
Files: internal/optimizer: predp.go and onerelsearch.go deleted;
joinsearchseam.go (spine code gone; pgShapedOffsetChecksOK(spans,
nonEmitting, bindingOffsets)); enclosingtree.go, collapse.go, exprwalk.go,
joinlayout.go, planner.go, joinsearchtrace.go, unnest.go, flaglabels.go;
tests trimmed (remap_arms_test.go -> outerref_fixture_test.go).
Scripts: sf025 (knob lane gone), fireset/parity-capture/estimate-audit
(JOINTREE vars gone), planner-flags.env regenerated. Design doc
m0145-0008-del3-dead-code.md + README + flip doc.
Findings: deadcode for internal/optimizer went 106 -> 84, below the
pre-slice-2 baseline of 87. No plan moved on TPC-DS SF0.25/SF1 or TPC-H
(fire-set fires=none on all three).
Side note: the tpch capture lane (estimate-audit-arm) still defaults
GOOPG_PGSHAPED_DP=0 (PGSHAPED unset), so a CORPORA=tpch fire-set run
measures the non-shipped DP. It reads TPC-H match=1, not the flip's 2. This
is pre-existing and not filed yet; consider pinning PGSHAPED=1 in
jointree-parity-capture.sh's tpch lane.
Next step: an audit of M0145-0001's §6 retirement list
(docs/design/0100-0149/m0145-0001-jointree-ir-and-lowering-contract.md:381).
For each row, give a verdict: gone / dead (deadcode) / live, and if live,
the task that must land before it can go. Many rows are still the live route
(tryJoinSearch, the unnest family, planFromClause, the joinlist proto-IR,
decline classes). Close the dead rows; file the live ones as children of
M0145-0008 or map them to the M0146 tasks. That decides whether
M0145-0008 can be marked [x].
Gates run: units PASS; spotcheck PASS (Q12=2, Q13=33); fireset HEAD vs
staged fires=none at SF0.25 and SF1, and with CORPORA=tpch; sf025 96/96,
shapes 99/99 same; acceptance 24 MATCH; ea-ratchet 52/52.
Selection: M0145-0008 parent (item 3; owner GO on the deletion slices).
0008m (wrong results, S2) is still waiting for owner placement.
In-flight: none
