# M0146-0014 — census at c544710da (2026-11-04, run-2 loop #49): exhausted tree=3f2fb1f77d3f24fb6e0fddc6baa0ca1d1a811a4f

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261104a, reason=plan-relevant-change)
— code commit `c544710da` (M0148-0001c3c: catalog DROP / RENAME resolve the key a relation lives
under; `internal/catalog/catalog.go`). Its own fire-set gate measured `fires=none` on all three
corpora. `git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built
its engines, hence `tree=` above (HEAD's tree). Capture repo head `cbf292258` (records on top of the
code commit).

Capture: `BASELINE_REV=6a44367be CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9aw tmp/census-20261104b`
(BASELINE_REV = the head consumed by routing-20261104a; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9aw-candidate`
sha256 `01a7b6192947cf9087e0f1148a817791366edce9c919f2bde51e92548b65a291`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261104b/flat` (exit 0):

- `tpch IDENTICAL new=5175c597d5c008062df3cb56dc0f007e5eb92d2bb2019caf9d988cbb384c0f36 base=routing-20261102f/s9o-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=53e4d570779b683bf72fa8d7b1ea54c604bc221be373d3e636e021d93be594a0 base=routing-20261102f/s9o-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=5b26baf8eec97bd971f1c7dd25287e1d14415f1aad157408a1d18a34e878582d base=routing-20261102f/s9o-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261102f's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0148 queue.
