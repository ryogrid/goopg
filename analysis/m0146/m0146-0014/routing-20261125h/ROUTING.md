# M0146-0014 — census at 1a440ab9a (2026-11-19): exhausted tree=d22d580221d63fb43dd1c6d111de9829f311030e

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261125g, reason=plan-relevant-change)
— code commit `1a440ab9a` (standby database-list refresh (M0149-0032f)).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `1a440ab9a`.

Capture: `BASELINE_REV=fff19ea2c CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9ih tmp/census-20261125h`
(BASELINE_REV = the head consumed by routing-20261125g; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9ih-candidate`
sha256 `4297fee43eb83808ef31c04e4a6f204f78fd7b0bbb3ca0fb63b829e65a1961fc`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261125h/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b21… base=routing-20261118q/s9hq-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=640af11e9e44e812c634e340… base=routing-20261118q/s9hq-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=126df8d535911999d1d44687… base=routing-20261118q/s9hq-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261125g's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
