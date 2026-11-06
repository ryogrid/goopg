# M0146-0014 — census at f1709f7ab (2026-11-02, run-2 loop #17): exhausted tree=ae51bf47bc7bbde809dde5fbc1f542132f1fe24d

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261102h, reason=plan-relevant-change)
— code commit `f1709f7ab` (M0148-0001e slice 2: EXPLAIN qualifies upper-node columns by the
statement's range-table size, `Explain.RtableSize`). Its own fire-set gate measured `fires=none` on
all three corpora (every corpus query already holds more than one visible relation). `git diff
--quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its engines,
hence `tree=` above. Capture repo head `fd70b3b85` (records commit on top of the code commit).

Capture: `BASELINE_REV=d26246bad CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9r tmp/census-20261102i`
(BASELINE_REV = the head consumed by routing-20261102h; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9r-candidate`
sha256 `e8dabd278c8518528ff92d53aab84061e8922ca5a8148e61d64c609a32566805`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261102i/flat` (exit 0):

- `tpch IDENTICAL new=5175c597d5c008062df3cb56dc0f007e5eb92d2bb2019caf9d988cbb384c0f36 base=routing-20261102f/s9o-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=53e4d570779b683bf72fa8d7b1ea54c604bc221be373d3e636e021d93be594a0 base=routing-20261102f/s9o-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=5b26baf8eec97bd971f1c7dd25287e1d14415f1aad157408a1d18a34e878582d base=routing-20261102f/s9o-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261102f's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M0148 queue.
