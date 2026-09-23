Task: M0145-0030 — group B: adjudicate the behavioural flip-triage tests
(+ knob-default script audit). Design doc
docs/design/0100-0149/m0145-0030-group-b-adjudication.md (disposition table).

Done: fix 1 `903780b3e` (alias collision); fix 2 `564dc1c8e`
(searchRelIndexPathCost — index-ordered grouping priced from the search
rel's cost_index path); stale pins updated (nested scalar pair,
RowsRemovedByJoinFilter); script audit listed in the design doc.

CORRECTION: loop #21's "6 of 11 pass" was false. These 5 still FAIL under the
flip (verified at HEAD and at d9b962326, i.e. not caused by fix 1):
- executor TestNLISemiResidualExecution / TestNLIAntiResidualExecution:
  Hash Semi Join elected, test expects NLI semi (nli_semi_residual_exec_test.go:114);
- executor TestParallelNLIJointypeIdentity/semi;
- executor TestHashedInProbeActuallyFires (hashed-IN SubPlan not built, nil kvcache);
- executor TestRunFastJoinConcrete (undiagnosed).
Reproduce: GOOPG_JOINTREE_PIPELINE=1 go test -count=1 -run '<name>' ./internal/executor/
(the env var is equivalent to the local code flip; no source edit needed).
For each: run the same query on PG 18.3 (throwaway PG on 5534:
tmp/m0145-0030-pgscratch, `pg_ctl -D ... stop` when done) → decide stale pin
vs real defect.

Next step: TestNLISemiResidualExecution — read the fixture, EXPLAIN the query on
PG 18.3 (tiny no-stats tables: PG may well pick Hash Semi Join too → stale).

Gates run (for 564dc1c8e): units PASS; tpch-spotcheck PASS (Q12=2 Q13=33);
tpcds-sf025 PASS=96 same=99; acceptance arm PASS (24 MATCH vs
tmp/arm-on-20260922-loop77.txt, PGSHAPED=1); tpcds-fireset PASS; knob-arm
sf025 plan shapes unchanged 99/99. Own fireset clones deleted.
In-flight: none. Throwaway PG 18.3 still running on 127.0.0.1:5534
(data tmp/m0145-0030-pgscratch) — reuse it or stop it.
