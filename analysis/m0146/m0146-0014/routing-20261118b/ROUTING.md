# M0146-0014 — census at 5113b426f (2026-11-18): exhausted tree=142955469c62829bf0f07e607e983e1ea2ab294b

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261118a, reason=plan-relevant-change)
— code commit `5113b426f` (M0149-0001h1a19: the virtual pg_operator serves all 799 built-in rows; regprocout qualification).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `5113b426f`.

Capture: `BASELINE_REV=617b67e5f CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9hb tmp/census-20261118b`
(BASELINE_REV = the head consumed by routing-20261118a; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9hb-candidate`
sha256 `692a08da12152d990f7d231e31d43c545cc61daef746b3d38ebb80dbc0fc6ae7`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261118b/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b2… base=routing-20261114g/s9cl-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=4f34a9cbb22b9dbb… base=routing-20261114g/s9cl-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=d6d8186c45c8b6266b… base=routing-20261114g/s9cl-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261118a's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
