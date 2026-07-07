Task: M-NIGHTLY tpch/Q9-timeout (AI-20260707-000712-007) — investigation loop,
no code landed (fix found but reverted after a correctness hazard was proven).

Files: NONE modified in the final state (all touched files reverted to HEAD:
internal/planner/bushy.go, internal/planner/nl_index_join.go,
internal/executor/multi_hash_join.go). Only .ralph/fix_plan.md,
.ralph/deferral_ledger.md, .ralph/working_set.md carry notes.

Key symbols: internal/planner/bushy.go `enumerateBushyPlans`/`buildJoinFromDP`/
`remapKeyToSubset` (bushy DP's LOCAL per-join coordinate convention),
`collectMultiHashTables`/`rewriteMultiWayChain`/`applyJoinTreePosMap`'s
`*MultiHashJoin` case (MHJ-Filters GLOBAL-FROM-order coordinate convention);
internal/planner/nl_index_join.go `collectCrossSideEquiKeys`/
`pickIndexCoveringAllLeadingColumns` (NLI index picker, needs LOCAL coords).

Hypothesis/Findings: CONFIRMED root cause of Q9 timeout — partsupp's NLI join
uses only the single-column FK index (ps_suppkey) because bushy DP only wires
ONE of the two available equi-conditions (ps_suppkey=l_suppkey,
ps_partkey=l_partkey) into a join's canonical Predicate; the other becomes a
whole-plan residual Filter invisible to NLI index selection, causing ~80x row
amplification per probe (6M lineitem rows x ~80 partsupp rows/supplier).
Prototyped+verified fix (attachCrossConjunctsToTree, walks final bushy tree,
ANDs unused cross-table edges onto the lowest co-locating join) via EXPLAIN
(composite partsupp_pk chosen) AND a real run: timeout -> 87s, 175 rows
(matches real-PG-18.3 structural anchor). BUT this same mechanism corrupts
results (0 rows instead of 1, proven via a 3-table toy repro) when the SAME
join instead gets folded into a MultiHashJoin chain (DP's NLI alternative for
tied/small-cost cases) — a coordinate-space conflict between two existing,
independently-correct-until-now consumers of Join.Predicate's extra AND'd
conjuncts (nl_index_join.go wants local bushy-subset coords;
bushy.go's collectMultiHashTables+applyJoinTreePosMap wants global FROM-order
coords and double-remaps otherwise). Full byte-level trace + both candidate
fixes (A: name-based fallback classification + raw global-coord attachment;
B: reorder the attachment pass to run after rewriteMultiWayChain, before
rewriteJoinsToNLI) are written up in today's deferral ledger row — read that
before re-attempting, it has the exact code-level reasoning already done.

Next step: pick option (A) or (B) from the ledger row, implement, and BEFORE
declaring success add a permanent regression test pinning the toy 3-table
repro (zz_part/zz_lineitem/zz_partsupp, partsupp non-unique per supplier,
composite PK) alongside re-verifying the real Q9 EXPLAIN/row-count/timing.
Do NOT skip the toy-repro regression test this time — it is what caught the
bug this loop; without it a future attempt could re-introduce the same
silent-wrong-results hazard undetected. If Q9 remains too large for one loop,
tpch/Q20-timeout (AI-20260707-000712-008) is next in the M-NIGHTLY queue and
is architecturally unrelated (a correlated-subquery-with-nested-IN shape, not
an under-constrained-join-key shape) — safe to attempt independently.

Gates run: go build ./... (clean), go test ./internal/planner/...
./internal/executor/... (PASS, cached — files reverted to identical-to-HEAD
content), scripts/tpch-spotcheck.sh (PASS, Q12=2/Q13=33) — all run AFTER the
revert, confirming the tree is healthy with zero functional changes this
loop.

In-flight: none. All manually-started servers (scopes goopg-q9-explore,
goopg-q9-explore2, goopg-q9-verify, goopg-q9-verify2, goopg-q9-debug through
goopg-q9-debug6, goopg-spotcheck) were stopped; `ps aux`/`systemctl --user
list-units` confirm no leftover goopg-bench-bin processes or scopes. The
extra upstream-PostgreSQL instance manually started on port 65499 (against
bench/tpch/runtime/pgdata, for oracle comparison) was stopped via `pg_ctl
stop`. All temp files under /tmp/q9* and /tmp/q9repro removed. `git status`
clean except .ralph/progress.json (harness-managed) and the untracked
`postgres` submodule (pre-existing).
