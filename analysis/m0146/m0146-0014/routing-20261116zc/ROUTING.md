# M0146-0014 — census at 83a6c5aeb (2026-11-16): exhausted tree=54f132ebbee667daf46442cc5eb5ffc9a7aeb984

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261116zb, reason=plan-relevant-change)
— code commit `83a6c5aeb` (M0149-0001g slice 27: mapped catalogs carry relfilenode 0; pg_filenode.map equals PG).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `83a6c5aeb`.

Capture: `BASELINE_REV=8fe3291d0 CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9fa tmp/census-20261116zc`
(BASELINE_REV = the head consumed by routing-20261116zb; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9fa-candidate`
sha256 `5eab46df1a9b6ca43dc58852ef5388458f49e49659b98ca3cfa2b9be6e88ef4d`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261116zc/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b2… base=routing-20261114g/s9cl-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=4f34a9cbb22b9dbb… base=routing-20261114g/s9cl-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=d6d8186c45c8b6266b… base=routing-20261114g/s9cl-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261116zb's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
