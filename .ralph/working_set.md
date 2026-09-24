Task: M0145-0008 — cutover. FLIP LANDED 2026-09-24 (loop #49); the
legacy-deletion slices remain, so the task stays [ ].

Files: see docs/design/0100-0149/m0145-0008-cutover-flip.md. The flip code is
commit ddb4eabd4: a concurrent session's pathspec-less
"analysis(latency-trend)" commit swept the loop's gated staged index. It is
already pushed and is NOT rewritten. The prerequisite NULL-key fix is
eb0e7d388.

Key symbols: jointreePipelineFromEnv (v != "0"); SetJointreePipeline /
SetIndexProbeCostMultiplier (test hooks); inListElementSelectivity
(isunique); baseColumnOfTable (EstRelRows fallback);
planOneIndexScan (executor tests).

Findings: every gate is green on the new default. TPC-H match 2/22 equals
the baseline; SF0.25 match 2 holds the floor. ea-ratchet 53->50 with 1 NEW
(Q95), filed as M0145-0008c. Q18 1.6x / Q22 3.2x slower, filed as
M0145-0008b. Grouping ignores ORDER BY DESC: M0145-0008d.

Next step: root M0145-0001 is [!]: the S4 lineage budget is exhausted
(escalation 2026-09-24 in its entry, holding would-be 0008b/c/d). Item 3
is held until the owner answers, so select item 4 (M0141-S2a-fix2r) per
the banner, or the next live item. Do not select M0145 descendants.

Gates run: units; tpch-spotcheck; tpcds-sf025 sweep; acceptance arm (vs
loop77); fireset; TPC-H + SF0.25 parity captures; make ea-ratchet
(re-run after the scratch PG on :5534 voided the first run).

Parked WIP (M0122-0008 ALTER SYSTEM, outranked by the banner):
tmp/m0122-alter-system-wip.patch (sqlkeywords.QuoteIdentifier only). Oracle
behaviour for ALTER SYSTEM was captured in loop #48's transcript.

In-flight: none.
