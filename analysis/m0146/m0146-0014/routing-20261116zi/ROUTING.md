# M0146-0014 — census at 20df60c66 (2026-11-16): exhausted tree=285386aecb4ce21b1dd3919ee8109f9d1136f27b

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261116zh, reason=plan-relevant-change)
— code commit `20df60c66` (M0149-0001g slice 33: imported libc + ICU collations).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `20df60c66`.

Capture: `BASELINE_REV=1745ee522 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9fg tmp/census-20261116zi`
(BASELINE_REV = the head consumed by routing-20261116zh; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9fg-candidate`
sha256 `131668fdad16354244d471be7fb70829dcffa8143b3021a7140f8b01b1468138`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261116zi/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b2… base=routing-20261114g/s9cl-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=4f34a9cbb22b9dbb… base=routing-20261114g/s9cl-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=d6d8186c45c8b6266b… base=routing-20261114g/s9cl-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261116zh's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
