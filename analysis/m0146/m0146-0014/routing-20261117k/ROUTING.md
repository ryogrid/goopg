# M0146-0014 — census at 17fe002fc (2026-11-17): exhausted tree=ab1c63d93e1d1fe5d4f3c56be2bd3c471a167b0f

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261117j, reason=plan-relevant-change)
— code commit `17fe002fc` (M0149-0001h1a slice 1: large-object metadata, lo_create / lo_unlink / has_largeobject_privilege).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `17fe002fc`.

Capture: `BASELINE_REV=565905450 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9gk tmp/census-20261117k`
(BASELINE_REV = the head consumed by routing-20261117j; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9gk-candidate`
sha256 `abe90a03eef85f3bbc7937ce82cd4ace7c4b57234cfc9794236e2a1c389b26df`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261117k/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b2… base=routing-20261114g/s9cl-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=4f34a9cbb22b9dbb… base=routing-20261114g/s9cl-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=d6d8186c45c8b6266b… base=routing-20261114g/s9cl-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261117j's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
