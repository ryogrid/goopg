# M0146-0014 — census at 60e38672b (2026-11-16): exhausted tree=d4965404ff6b54ffd1231b951b41df6b75247341

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261116zc, reason=plan-relevant-change)
— code commit `60e38672b` (M0149-0001g slice 28: hand-added pg_proc duplicates removed).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `60e38672b`.

Capture: `BASELINE_REV=83a6c5aeb CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9fb tmp/census-20261116zd`
(BASELINE_REV = the head consumed by routing-20261116zc; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9fb-candidate`
sha256 `dccdaacbcb71a2740e066eedddc4a359f36a5f4f54a7c3946e50f27233cbaaac`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261116zd/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b2… base=routing-20261114g/s9cl-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=4f34a9cbb22b9dbb… base=routing-20261114g/s9cl-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=d6d8186c45c8b6266b… base=routing-20261114g/s9cl-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261116zc's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
