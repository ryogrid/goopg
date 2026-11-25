# M0146-0014 — census at e7f2dfa83 (2026-11-11, run-2 loop #4): exhausted tree=47731e05d81093a59d27a574fba7cb22dca10729

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261111a, reason=plan-relevant-change)
— code commit `e7f2dfa83` (M0148-0001j4 item 1: routines draw OIDs from the catalog's single
counter; `internal/catalog/routines.go`, `internal/catalog/catalog.go`). The script's trigger file
was `internal/catalog/catalog.go`. The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `581d1a818`.

Capture: `BASELINE_REV=34e287597 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9be tmp/census-20261111b`
(BASELINE_REV = the head consumed by routing-20261111a; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9be-candidate`
sha256 `0e04c7ae84b42787ad2bc48f76f03f97f4ddcfdd2da82841d2f5f8d439c84db3`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261111b/flat` (exit 0):

- `tpch IDENTICAL new=62dae4067986ea6ecd0e210ca537d70e9e58ec8574d63a3a935956adf62c52b4 base=routing-20261104e/s9bb-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=53e4d570779b683bf72fa8d7b1ea54c604bc221be373d3e636e021d93be594a0 base=routing-20261104e/s9bb-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=5b26baf8eec97bd971f1c7dd25287e1d14415f1aad157408a1d18a34e878582d base=routing-20261104e/s9bb-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261111a's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0148 queue.
