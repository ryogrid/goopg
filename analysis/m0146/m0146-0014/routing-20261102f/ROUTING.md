# M0146-0014 — census at 5040c8c72 (2026-11-02, run-2 loop #14): exhausted tree=b92dc0927b6b86c45e875d8439a0aeb871261755

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261102e, reason=plan-relevant-change)
— code commit `5040c8c72` (M0148-0001c2 arm (1): btree tree_height read off the metapage,
`catalog.IndexTreeHeightFunc`, PG's `_bt_getrootheight`). `git diff --quiet -- internal cmd scripts
bench go.mod go.sum` held while the capture built its engines, hence `tree=` above. Capture repo
head `5a6adb488` (records commit on top of the code commit and its pin).

Capture: `BASELINE_REV=0e11ec1de CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9o tmp/census-20261102f`
(candidate engine `tmp/fireset-bin/s9o-candidate`
sha256 `2ec71090d8e56717da0b88470f33f81f4c02400b8f871e04d2de0a6576fd3c59`).

| corpus | capture | match | `--compare` vs BASE |
|---|---|---|---|
| TPC-H | `s9o-tpch*` | 22 / 22 | CHANGED (new 5175c597…, base ce932e73… routing-20261030c) |
| TPC-DS SF0.25 | `s9o-tpcds-sf025*` | 99 / 99 | IDENTICAL (53e4d570…) |
| TPC-DS SF1 | `s9o-tpcds-sf1*` | 99 / 99 | IDENTICAL (5b26baf8…) |

The TPC-H change is cost text only: Q2 and Q11 (the fireset plan A/B `fires=Q2 Q11`) price
`Index Scan using partsupp_supplier_fkidx` at startup 0.42 instead of 0.30 — the descent cost now
uses the index's real height, exactly PG 18.3's reference value in `s9o-tpch-pg.plans.txt` — and
every node above carries the shifted total. No plan shape changed (PLAN-SHAPE same=20 changed=2,
both cost-only; PLAN-PARITY 22/22).

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed. Because a plan text
changed, the full capture is committed (S9(4a)); this directory becomes the TPC-H BASE.
