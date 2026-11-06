# M0146-0014 — census at 68997af77 (2026-11-02, run-2 loop #4): exhausted tree=0d26af9ad8db4b6fe9ed1f108577d6a041cb87e9

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261101h, reason=plan-relevant-change)
— the M0148-0001c1 code commit `68997af77` (min/max InitPlans priced as PG's
create_minmaxagg_path; `Result.minmaxCharged`; initPlan charge walk skips a pre-charged min/max
Result below another level top). It changes costs only on min/max-rewritten InitPlans; the
captured plan texts (EXPLAIN, costs included in the census capture) are unchanged on all three
corpora, so `fires=none`. `git diff --quiet -- internal cmd scripts bench go.mod go.sum` held
while the capture built its engines (only bookkeeping was dirty), hence `tree=` above. Capture
repo head `c80216b18` (the slice's records commit on top of the code commit).

Capture: `BASELINE_REV=c4b80c6d6 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9i tmp/census-20261102a`
(BASELINE_REV = the commit consumed by routing-20261101h; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9i-candidate`
sha256 `dd1548bb01fe6e3baf72645304800dfa5f0ef287bad18647ba423ca0cc87a349`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261102a/flat` (exit 0):

- `tpch IDENTICAL new=ce932e73bc646988fbc215145f90119edbb233bdaf1884af4402442a6ba28421 base=routing-20261030c/s9w17-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=53e4d570779b683bf72fa8d7b1ea54c604bc221be373d3e636e021d93be594a0 base=routing-20261030c/s9w17-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=5b26baf8eec97bd971f1c7dd25287e1d14415f1aad157408a1d18a34e878582d base=routing-20261030c/s9w17-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261101h's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M0148-0001c slice (d) (queue head).
