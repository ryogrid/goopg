# M0146-0014 — census at 16fd35c92 (2026-11-19): exhausted tree=db32c9d32bc0b29d874669b3c5d1fa7f8ea96ec5

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261123v, reason=plan-relevant-change)
— code commit `16fd35c92` (M0149-0010a: standby catalog refresh + clog replay).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `16fd35c92`.

Capture: `BASELINE_REV=9d1707d54 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9iw tmp/census-20261123w`
(BASELINE_REV = the head consumed by routing-20261123v; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9iw-candidate`
sha256 `cf54f0648d21aaf858d17bbd9e49801411fc1d10cd1f0013abf6cfe8754372d6`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261123w/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b21… base=routing-20261118q/s9hq-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=640af11e9e44e812c634e340… base=routing-20261118q/s9hq-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=126df8d535911999d1d44687… base=routing-20261118q/s9hq-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261123v's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
