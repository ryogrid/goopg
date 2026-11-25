# M0146-0014 — census at d5ed00ffe (2026-11-16): exhausted tree=d419147ff1bfc77d510350dd2693db9043abbe7c

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261116v, reason=plan-relevant-change)
— code commit `d5ed00ffe` (M0149-0001g slice 21: snowball text-search dictionaries and configurations).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `d5ed00ffe`.

Capture: `BASELINE_REV=cce5f7043 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9eu tmp/census-20261116w`
(BASELINE_REV = the head consumed by routing-20261116v; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9eu-candidate`
sha256 `d52e04dfc959a77be764a633e9f4716bba71810eed26ef5ec726e2160aad606c`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261116w/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b2… base=routing-20261114g/s9cl-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=4f34a9cbb22b9dbb… base=routing-20261114g/s9cl-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=d6d8186c45c8b6266b… base=routing-20261114g/s9cl-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261116v's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
