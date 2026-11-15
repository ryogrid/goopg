# M0146-0014 — census at e11971ba1 (2026-11-16): exhausted tree=2c3879e4271b2f189ca8b0a5349e3b91f49a971c

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261116a, reason=plan-relevant-change)
— code commit `e11971ba1` (M0149-0001f: pg_shdepend / pg_shdescription virtual catalogs).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `e11971ba1`.

Capture: `BASELINE_REV=33e23600c CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9dz tmp/census-20261116b`
(BASELINE_REV = the head consumed by routing-20261116a; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9dz-candidate`
sha256 `a6a124d1aabc0df0abf13b663262eaf151f205172a03e05427654a286dae49d3`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261116b/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b2… base=routing-20261114g/s9cl-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=4f34a9cbb22b9dbb… base=routing-20261114g/s9cl-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=d6d8186c45c8b6266b… base=routing-20261114g/s9cl-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261116a's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
