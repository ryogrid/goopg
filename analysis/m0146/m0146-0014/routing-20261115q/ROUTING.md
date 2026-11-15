# M0146-0014 — census at 900c5bbe9 (2026-11-15): exhausted tree=b9d41a5e51780f6a0728423f46df83749916b55c

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261115p, reason=plan-relevant-change)
— code commit `900c5bbe9` (M0149-0001a1 S3: template1 at base/1; namespace OIDs equal PostgreSQL
database OIDs; base/2 → base/1 startup migration).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `900c5bbe9`.

Capture: `BASELINE_REV=2bf9fe449 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9du tmp/census-20261115q`
(BASELINE_REV = the head consumed by routing-20261115p; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9du-candidate`
sha256 `23f235af86af2d8c406ef81df2ed62b829ddae30293f473165e78d1ed841d1f8`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261115q/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b2… base=routing-20261114g/s9cl-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=4f34a9cbb22b9dbb… base=routing-20261114g/s9cl-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=d6d8186c45c8b6266b… base=routing-20261114g/s9cl-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261115p's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
