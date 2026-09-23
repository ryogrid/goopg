# Working Set — ralph2 loop #5 end-state

Task: M0145-0008 cutover — pre-flip unit triage done; flip NOT applied.
**Root M0145-0001 is now [!]** (S4 lineage budget exhausted: 0004a, 0005,
0007, 0027, 0028 all Movement: none). Escalation block written into
M0145-0001 with owner options (a) re-pin LINEAGE-BASELINE + GO groups I/B,
(b) flip with I/B pinned to legacy as post-flip defects, (c) hold.
Do NOT select or file any M0145-0001 descendant (0008, 0010, 0024, 0025,
0026 included) until the owner answers.

Files: docs/design/0100-0149/m0145-0008-flip-test-triage.md (new);
internal/optimizer/pipeline_pin_test.go + 17 test files (committed
3b8b771f6); fix_plan (0008 triage bullet, 0001 escalation + [!]).

Findings: flip = one line in internal/optimizer/jointreepipeline.go
(`v == "1"` -> `v != "0"`); local flip leaves 23 failures after pins
(2 group K, ≥10 group I index coverage, 11 group B). Gate scripts defaulting
the knob to 0: tpcds-sf025-regression.sh:310 (+ its =1 control pass at :842
must become =0), tpch-estimate-audit-arm.sh:108.

Next step: select per banner — item 3 is blocked (root [!]); item 4 M0141-S2a-fix2r is already [x], so continue down the banner (item 5 M0141-S2a-fix1-sweep, …).
Read AGENT.md plan-parity rules + the task entry first.

Gates run: units under a local flip (triage only); optimizer suite without
flip PASS; pgbench smoke on commits.

In-flight: none.
