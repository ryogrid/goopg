# M0146-0014 — census at f7a2a760b (2026-11-19): exhausted tree=fdfed512bb22c538b70ee6694a7b2a6c0c7dee94

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261119q, reason=plan-relevant-change)
— code commit `f7a2a760b` (M0149-0001h1a71: typed literals take modifiers, pg_catalog and more types).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `f7a2a760b`.

Capture: `BASELINE_REV=0203882c4 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9ir tmp/census-20261119r`
(BASELINE_REV = the head consumed by routing-20261119q; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9ir-candidate`
sha256 `1a439c86b34c6748bda9f24895944f41d871e77f2b700014191a3a9076a6caee`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261119r/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b21… base=routing-20261118q/s9hq-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=640af11e9e44e812c634e340… base=routing-20261118q/s9hq-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=126df8d535911999d1d44687… base=routing-20261118q/s9hq-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261119q's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
