# M0146-0014 — census at 93ca7d4cb (2026-11-17): exhausted tree=d801a38306fee68cac36f65a2a99208ccb7b55fa

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261117k, reason=plan-relevant-change)
— code commit `93ca7d4cb` (M0149-0001h1a slice 2: large-object data pages and descriptor API).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `93ca7d4cb`.

Capture: `BASELINE_REV=17fe002fc CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9gl tmp/census-20261117l`
(BASELINE_REV = the head consumed by routing-20261117k; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9gl-candidate`
sha256 `e655610fdd0eb828f26220e1f114abebb6a65a215bce25cb00b8da97d46e2b8f`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261117l/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b2… base=routing-20261114g/s9cl-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=4f34a9cbb22b9dbb… base=routing-20261114g/s9cl-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=d6d8186c45c8b6266b… base=routing-20261114g/s9cl-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261117k's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
