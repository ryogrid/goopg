Task: none in flight. M0134-0078 Bucket B7 LANDED + case PARKED (commit 242fd120,
2026-08-23): executePLpgSQLTriggerBody non-plpgsql arm `(nil,true,nil)`→
`(nil,false,nil)` so a C trigger no longer suppresses its row; guard test +
design doc m0134-0078-non-plpgsql-trigger-suppression.md + README index landed.
Gates all green: guard PASS, executor Trigger units PASS, pg-regress-runner
triggers (suppression eliminated, diff 2853→2818), tpch-spotcheck Q12=2/Q13=35.

Files: committed — plpgsql_runtime.go, operators_trigger_test.go, design doc,
docs/design/README.md, fix_plan.md (parked entry + nightly evidence stamps),
deferral_ledger.md (13 rows: R1 + B1-B6/B8-B13).

Hypothesis/Findings: Region-1 byte-parity is blocked by un-executable C bodies
(PG's C triggers transform the tuple; goopg passes rows through unmodified) —
ledgered R1, NOT a B7 defect. Next-best contained slice for this case is B9
(ALTER TRIGGER RENAME parser), but see selection rule below.

Next step: nightly triage clean this loop — both AI-20260823-011911 items are
re-reports of already-filed M-NIGHTLY tasks (-001/-003); evidence paths stamped
onto those entries. Next loop: per banner, M-NIGHTLY filing first, then next
unparked unchecked M0134 task by ID ascending = M0134-0079 (tuplesort.sql).

Gates run: tester brief tmp/ralph-handoffs/m0134-0078-b7-gates/ (report inline):
guard + Trigger units + regress-runner + tpch-spotcheck ALL PASS. Pre-commit
pgbench smoke PASS on both commits.

Delegation: none active; m0134-0078-b7-gates worker finished DONE (no report.md
— harness blocked the write, verdict returned inline).

In-flight: none.
