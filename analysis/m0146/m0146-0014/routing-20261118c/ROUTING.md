# M0146-0014 — census at 25bed91ce (2026-11-18): exhausted tree=037d32f7edd12578246adb49d5f7c072852cb15c

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261118b, reason=plan-relevant-change)
— code commit `25bed91ce` (M0149-0001h1a20: pg_cast / pg_opfamily / pg_opclass / pg_amop / pg_amproc serve every built-in row).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `25bed91ce`.

Capture: `BASELINE_REV=5113b426f CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9hc tmp/census-20261118c`
(BASELINE_REV = the head consumed by routing-20261118b; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9hc-candidate`
sha256 `3648a6f223f0f6c8ad54f7f777299ab3be40070535946a04279a3823490af69c`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261118c/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b2… base=routing-20261114g/s9cl-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=4f34a9cbb22b9dbb… base=routing-20261114g/s9cl-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=d6d8186c45c8b6266b… base=routing-20261114g/s9cl-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261118b's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
