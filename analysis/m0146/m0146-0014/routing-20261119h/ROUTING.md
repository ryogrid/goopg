# M0146-0014 — census at e7ed10d48 (2026-11-19): exhausted tree=c8b1b25aa76b79b0cb91a2b920c753a76aef4eba

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261119g, reason=plan-relevant-change)
— code commit `e7ed10d48` (M0149-0001h1a58: to_char locale values; a0d01aeb9 far dates is covered too).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `e7ed10d48`.

Capture: `BASELINE_REV=3394aa34b CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9ih tmp/census-20261119h`
(BASELINE_REV = the head consumed by routing-20261119g; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9ih-candidate`
sha256 `98c37b84efdc603a533af93717b498b6a349187641009a45b63b46bff3f85ea8`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261119h/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b21… base=routing-20261118q/s9hq-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=640af11e9e44e812c634e340… base=routing-20261118q/s9hq-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=126df8d535911999d1d44687… base=routing-20261118q/s9hq-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261119g's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
