# M0146-0014 — census at eef0c4bef (2026-10-31): exhausted

Trigger: `scripts/s9-census-due.sh` returned DUE (base e3df993166e6, reason=plan-relevant-change).
`eef0c4bef` changed `internal/optimizer/relsize.go` (tableDataWidth probe-width ladder),
`internal/catalog/catalog.go` (virtual pg_class reltuples=-1 renderer) and `internal/initdb/open.go`
(sidecar -1 sentinel decode) — the post-TRUNCATE cost-parity slice (M-NIGHTLY); all three sit in the
trigger set (`*relsize*`, catalog plan-relevant, relstats reload). `internal/executor/operators_ddl.go`
(truncate stats loop) is not in the trigger set. tree= below: `git diff --quiet -- internal cmd scripts
bench go.mod go.sum` held while the capture's binaries were built (the code commit was in HEAD, only
`.ralph/` and `docs/` records were being written). This is an unchanged census under S9(4a), so only
this file is committed; the captures were deleted from `tmp/census-20261031/`.
tree=4c63cc1822161b075dc2c5d1bd072a70097c18ea; capture repo head eef0c4bef
(the recording commit on top of eef0c4bef, `analysis/` only).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-H | 22 / 22 | all 0 |
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261031` (exit 0):

- tpch IDENTICAL new=ce932e73bc646988fbc215145f90119edbb233bdaf1884af4402442a6ba28421 base=routing-20261030c/s9w17-tpch.plans.txt
- tpcds-sf025 IDENTICAL new=53e4d570779b683bf72fa8d7b1ea54c604bc221be373d3e636e021d93be594a0 base=routing-20261030c/s9w17-tpcds-sf025.plans.txt
- tpcds-sf1 IDENTICAL new=5b26baf8eec97bd971f1c7dd25287e1d14415f1aad157408a1d18a34e878582d base=routing-20261030c/s9w17-tpcds-sf1.plans.txt

Engine binary (AGENT.md G3) and capture repo head, per corpus:

- tpch: tmp/goopg-acceptance-bin sha256 b316c66e71b1674b75dd490ab6e78bf3e556ab6ec8dd31415e206715d76082a8
  (built from HEAD's tree at capture start; the arm's estimate-audit tmp/estimate-audit sha256
  cb329a796f5662d5045032a9e9722dfef3719fc09ec8065e21967d47f59dd898); repo head eef0c4bef.
- tpcds-sf025 and tpcds-sf1: tmp/goopg-jointree-bin sha256
  b316c66e71b1674b75dd490ab6e78bf3e556ab6ec8dd31415e206715d76082a8; repo head eef0c4bef.

Both binaries differ from the routing-20261031b capture's d312cc184b74e27be8198eb2494b6aa9fd35066c7e9cb62006524ff4ef71be6a /
eb4ba1868564c552612bff269c6fddef3c53efa4d4b489069a57d9e973cd88b2 — a new binary was measured (G3), and the plan
text is byte-identical to the base on all three corpora. The change's probe arm (analyze-width ladder,
never-analyzed sentinel) is invisible to every ANALYZEd corpus table — the fireset A/B independently
measured fires=none on the SF1 corpus.

Nothing filed: every CATEGORIES-EXCL-MATCH category is 0 on every corpus, so no record is actionable.
The id(gate) list is unchanged from the routing-20261031b note (which names the routing-20261031a note).
