# M0146-0014 — census at b48b79212 (2026-10-31): exhausted

Trigger: `scripts/s9-census-due.sh` returned DUE (base tree 4c63cc182216, reason=plan-relevant-change)
with triggering paths `internal/catalog/pubsub.go`, `internal/optimizer/planner.go`,
`internal/parser/ast.go`, `internal/parser/ddl.go`, `internal/postmaster/dispatch.go` — the five
non-test files `b48b79212` (ALTER SUBSCRIPTION SET / ADD / DROP PUBLICATION, M0122-0014) touched in the
trigger set. All five are statement-routing surfaces for this change: the parser arms (gram.y:10873-10901
publication-list tails), the planner's pub/sub DDL type-switch arm, dispatch.go's CommandComplete tag arm
(dispatch.go is in the set as the GUC→planner-settings surface), the catalog registry setter (catalog/ is
in the set), and the executor arm lives in `internal/executor/operators_ddl.go`, which the tool does not
list. tree= below: `git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture's
candidate binary was built (the code commit was in HEAD; only `.ralph/` bookkeeping was dirty). This is an
unchanged census under S9(4a), so only this file is committed; the captures were deleted from
`tmp/census-20261031d/`.
tree=1a5b27b2a888a6bf49cf16bce109f6fe705cadd6; capture repo head b48b79212
(the recording commit on top of b48b79212, `analysis/` only).

Capture: `CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9d tmp/census-20261031d`
(BASELINE_REV=HEAD=b48b79212, so the plan A/B compares the committed tree against itself; fires=none on
all three corpora, PLAN-PARITY 22/22 + 99/99 + 99/99 vs PG).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-H | 22 / 22 | all 0 |
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261031d-compare` (exit 0; the three candidate captures
renamed to `cap-<corpus>.plans.txt`):

- tpch IDENTICAL new=ce932e73bc646988fbc215145f90119edbb233bdaf1884af4402442a6ba28421 base=routing-20261030c/s9w17-tpch.plans.txt
- tpcds-sf025 IDENTICAL new=53e4d570779b683bf72fa8d7b1ea54c604bc221be373d3e636e021d93be594a0 base=routing-20261030c/s9w17-tpcds-sf025.plans.txt
- tpcds-sf1 IDENTICAL new=5b26baf8eec97bd971f1c7dd25287e1d14415f1aad157408a1d18a34e878582d base=routing-20261030c/s9w17-tpcds-sf1.plans.txt

Engine binary (AGENT.md G3), candidate arm, per corpus:

- tpcds-sf025 + tpcds-sf1: tmp/fireset-bin/s9d-candidate sha256
  08263882d6d2b3c2154291e9cf00ee2d4452f6c313f014c2e43764b541be503f (built from HEAD's tree at capture start).
- tpch: tmp/fireset-bin/s9d-tpch-runner sha256
  b1c29bccc95ed56cdd4d61478082675700ac89ac1947125b724ba4a0329cad0e.

Both differ from the routing-20261031c capture's b316c66e71b1674b75dd490ab6e78bf3e556ab6ec8dd31415e206715d76082a8 —
a new binary was measured (G3), and the plan text is byte-identical to the base on all three corpora. The
change is statement-side subscription DDL (parser arms, merge_publications algebra, refresh gates, registry
+ pg_subscription heap write) — nothing on the EXPLAIN plan-text path; the fireset A/B independently
measured fires=none on all three corpora.

Nothing filed: every CATEGORIES-EXCL-MATCH category is 0 on every corpus, so no record is actionable.
The id(gate) list is unchanged from the routing-20261031c note (which names the routing-20261031b note).
