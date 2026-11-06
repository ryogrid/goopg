# M0146-0014 — census at 52feaacf9 (2026-11-02, run-2 loop #25): exhausted tree=1d2790d8dc2652b4b648bcc5bf4b80bf7bba922d

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261102o, reason=plan-relevant-change)
— code commit `52feaacf9` (M0148-0001f slice 3: an Aggregate's VERBOSE `Output:` deparses its
targetlist). The census captures are non-VERBOSE, so its own fire-set gate measured `fires=none` on
all three corpora. `git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the
capture built its engines, hence `tree=` above. Capture repo head `82c45ed82` (records commit on top
of the code commit).

Capture: `BASELINE_REV=744895946 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9y tmp/census-20261102p`
(BASELINE_REV = the head consumed by routing-20261102o; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9y-candidate`
sha256 `9f432c26ca7b395b3842daf9d0d74db7b81f8fa99f49d7f33a7f2084f68be674`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261102p/flat` (exit 0):

- `tpch IDENTICAL new=5175c597d5c008062df3cb56dc0f007e5eb92d2bb2019caf9d988cbb384c0f36 base=routing-20261102f/s9o-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=53e4d570779b683bf72fa8d7b1ea54c604bc221be373d3e636e021d93be594a0 base=routing-20261102f/s9o-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=5b26baf8eec97bd971f1c7dd25287e1d14415f1aad157408a1d18a34e878582d base=routing-20261102f/s9o-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261102f's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M0148 queue.
