# M0146-0014 — census at f62ac6468 (2026-11-19): exhausted tree=72ae507c102725980c2eca550e4cc4bfb22b49aa

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261119w, reason=plan-relevant-change)
— code commit `f62ac6468` (M0149-0001h1a78: nested set-returning calls expand like PG).
The census plan A/B measured `fires=none` on all three corpora.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `f62ac6468`.

Capture: `BASELINE_REV=67dc0a51b CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9ix tmp/census-20261119x`
(BASELINE_REV = the head consumed by routing-20261119w; plan A/B fires=none on all three
corpora; candidate engine `tmp/fireset-bin/s9ix-candidate`
sha256 `20c72890eb82cb1445ff4430a1c5309855baa8da682105fb489dc23f1a42a075`).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |
| TPC-H | 22 / 22 | all 0 |

`scripts/s9-census-due.sh --compare tmp/census-20261119x/flat` (exit 0):

- `tpch IDENTICAL new=f9e805ed96b8c7078b296b21… base=routing-20261118q/s9hq-tpch.plans.txt`
- `tpcds-sf025 IDENTICAL new=640af11e9e44e812c634e340… base=routing-20261118q/s9hq-tpcds-sf025.plans.txt`
- `tpcds-sf1 IDENTICAL new=126df8d535911999d1d44687… base=routing-20261118q/s9hq-tpcds-sf1.plans.txt`

Unchanged census (S9(4a)): verdict equals routing-20261119w's; only this file is committed and
the captures are deleted from `tmp/`. Item 10 is live: M-NIGHTLY, then the M0149 queue.
