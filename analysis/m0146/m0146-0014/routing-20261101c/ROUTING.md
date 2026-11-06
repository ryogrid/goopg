# M0146-0014 — census at 91bc8262a (2026-11-01): exhausted

Trigger: `scripts/s9-census-due.sh` returned DUE (base tree 7a76494f0683, reason=plan-relevant-change)
with triggering paths `internal/optimizer/planner.go`, `internal/parser/expr.go`, `internal/parser/select.go`,
`internal/parser/support.go`, `internal/parser/yacc_parser.go` + goldens — the M0122-0015f commit
`91bc8262a` (reject `x = ANY (non-array)` with PG's 42809). The change is analyzer-typing only:
`checkAnyAllArrayOperand` fires exclusively on the `op ANY/ALL (scalar)` shape, which exists in no TPC
query (the fireset's own A/B proved it: fires=none on both TPC-DS corpora for exactly this commit);
the splice marker (`SplicedArray`) is a parse-AST field consumed by nothing on the planning path.
tree= below: `git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture's
candidate binaries were built (the code commit was in HEAD; only `.ralph/` + `analysis/` bookkeeping
was dirty). This is an unchanged census under S9(4a), so only this file is committed; the captures
are deleted from `tmp/census-20261101c/` after this record.
tree=8ec4bfdd2dfa6363af2341949971798e2e07e64f; capture repo head 91bc8262a
(the code commit; the recording commit is this file + bookkeeping on top).

Capture: `BASELINE_REV=12aef8583 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9f tmp/census-20261101c`
(BASELINE_REV = the routing-20261101 base tree's commit, so the plan A/B spans the whole window since
that census — 7a76494f0683..91bc8262a: the two M-NIGHTLY test-flake fixes, the refresh-data half,
and this commit; fires=none on all three corpora, PLAN-PARITY 22/22 + 99/99 + 99/99 vs PG, and the
A/B's own baseline-vs-candidate match was 22/22 + 99/99 + 99/99 with divergent=0).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-H | 22 / 22 | all 0 |
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261101-compare` (exit 0; the three candidate captures
renamed to `cap-<corpus>.plans.txt`):

| corpus | plan-text hash (new == base) |
|---|---|
| tpch | ce932e73bc646988fbc215145f90119edbb233bdaf1884af4402442a6ba28421 IDENTICAL |
| tpcds-sf025 | 53e4d570779b683bf72fa8d7b1ea54c604bc221be373d3e636e021d93be594a0 IDENTICAL |
| tpcds-sf1 | 5b26baf8eec97bd971f1c7dd25287e1d14415f1aad157408a1d18a34e878582d IDENTICAL |

All three corpora are byte-identical to the routing-20261030c baselines: the census is exhausted at
this base, and the base is consumed — the next plan-relevant commit opens the next S9 window.
Top-residual unchanged: routing class=41 (join-order 23 / agg-join-order 8 / join-method 7 /
seq-scan-join 3) at SF0.25, cost-model dominated.
