# M0146-0014 — census at 0203882c4 (2026-11-19): exhausted tree=ca2abf9c9e0f02c0dcd55ab0c98b9efc6e7c9a9c

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261119p, reason=plan-relevant-change)
— code commit `0203882c4` (M0149-0001h1a70: time and timetz input use DecodeTimeOnly).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `0203882c4`.

Capture: `BASELINE_REV=3d80966cf CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9iq tmp/census-20261119q`
(BASELINE_REV = the head consumed by routing-20261119p; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9iq-candidate`
sha256 `6353b5e9384fd762b64df15fa16829d2a8e8bfc3cc66371cc9c01f01d8d3febc`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261119q/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b21… base=routing-20261118q/s9hq-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=640af11e9e44e812c634e340… base=routing-20261118q/s9hq-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=126df8d535911999d1d44687… base=routing-20261118q/s9hq-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261119p's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
