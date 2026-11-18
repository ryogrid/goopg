# M0146-0014 — census at 2e73c299c (2026-11-19): exhausted tree=dae1b43132f6ffaa5a1fee203b9701bc2b74f450

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261119b, reason=plan-relevant-change)
— code commit `2e73c299c` (M0149-0001h1a60: generate_series over numeric and timestamps).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `2e73c299c`.

Capture: `BASELINE_REV=5665e52ac CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9ic tmp/census-20261119c`
(BASELINE_REV = the head consumed by routing-20261119b; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9ic-candidate`
sha256 `95f14f57633e8b64b03c8ee718db5150745378cb64ef0808c28b32490fbf15fe`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261119c/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b21… base=routing-20261118q/s9hq-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=640af11e9e44e812c634e340… base=routing-20261118q/s9hq-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=126df8d535911999d1d44687… base=routing-20261118q/s9hq-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261119b's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
