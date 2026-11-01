# M0146-0014 — census at 5ae6f5416 (2026-11-02, run-2 loop #11): exhausted tree=604834400ad81abc0a4b539b7242c02da0d3808a

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261102b, reason=plan-relevant-change)
— code commit `5ae6f5416` (M0148-0001d slice 1: GROUP BY keys whose EquivalenceClass is already
represented are dropped; `redundantConstGroupKeys` union-find arm). No corpus query groups by two
columns equated in its WHERE / inner-join ON (or the fireset would have fired), so `fires=none`
is expected and was measured. `git diff --quiet -- internal cmd scripts bench go.mod go.sum` held
while the capture built its engines, hence `tree=` above. Capture repo head `94d6e8614` (the
slice's records commit on top of the code commit).

Capture: `BASELINE_REV=359e35c97 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9l tmp/census-20261102c`
(BASELINE_REV = the commit consumed by routing-20261102b; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9l-candidate`
sha256 `4a21ede3b56cd5ead10275eaa91c691cb736ced71b1c906f04b4894db24560f7`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261102c/flat` (exit 0):

- `tpch IDENTICAL new=ce932e73bc646988fbc215145f90119edbb233bdaf1884af4402442a6ba28421 base=routing-20261030c/s9w17-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=53e4d570779b683bf72fa8d7b1ea54c604bc221be373d3e636e021d93be594a0 base=routing-20261030c/s9w17-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=5b26baf8eec97bd971f1c7dd25287e1d14415f1aad157408a1d18a34e878582d base=routing-20261030c/s9w17-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261102b's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M0148 queue (0001d hunk 1271, 0001c2, …).
