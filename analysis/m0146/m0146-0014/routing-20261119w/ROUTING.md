# M0146-0014 — census at 67dc0a51b (2026-11-19): exhausted tree=c89d42a2364a76092ad094dc1b50563542a14574

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261119v, reason=plan-relevant-change)
— code commit `67dc0a51b` (M0149-0001h1a77: scale() reports a numeric display scale).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `67dc0a51b`.

Capture: `BASELINE_REV=92d911ac0 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9iw tmp/census-20261119w`
(BASELINE_REV = the head consumed by routing-20261119v; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9iw-candidate`
sha256 `98805a1ba402ab1a325264e5d914c99a91c9c80ebbc49d0570f52af097d023d7`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261119w/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b21… base=routing-20261118q/s9hq-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=640af11e9e44e812c634e340… base=routing-20261118q/s9hq-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=126df8d535911999d1d44687… base=routing-20261118q/s9hq-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261119v's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
