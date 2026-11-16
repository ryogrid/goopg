# M0146-0014 — census at a8252b3f9 (2026-11-16): exhausted tree=0743298f16a6b8bd0d11840e50721e8e441bae1c

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261116x, reason=plan-relevant-change)
— code commit `a8252b3f9` (M0149-0001g slice 23: goopg-side descriptions, obj_description catalog name, schema public owner/nspacl).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `a8252b3f9`.

Capture: `BASELINE_REV=87c1ded5a CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9ew tmp/census-20261116y`
(BASELINE_REV = the head consumed by routing-20261116x; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9ew-candidate`
sha256 `6e392309d335523e3f1cc8246a92353edc74d7bff9f8bf8a8568ea4b2e16601d`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261116y/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b2… base=routing-20261114g/s9cl-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=4f34a9cbb22b9dbb… base=routing-20261114g/s9cl-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=d6d8186c45c8b6266b… base=routing-20261114g/s9cl-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261116x's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
