# M0146-0014 — census at 3c844e578 (2026-11-18): exhausted tree=06e74f9752a86e7ad9c480f597b2faae95d8a2b5

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261118p, reason=plan-relevant-change)
— code commit `3c844e578` (M0149-0001h1a38: RowDescription typlen and typmod; `exprType` follows PG's
exprTypmod). Its fire-set gate listed the queries whose EXPLAIN widths moved; every one passed on values.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `3c844e578`.

Capture: `BASELINE_REV=f125f870f CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9hq tmp/census-20261118q`
(BASELINE_REV = the code consumed by routing-20261118p; candidate engine `tmp/fireset-bin/s9hq-candidate`
sha256 `cb8cb6697d8a77ce97206d9db4c9b19a20245ab9c611c25f2c6f9936c79fd2fa`).

| corpus | capture | match | `--compare` vs BASE (routing-20261114g) |
|---|---|---|---|
| TPC-H | `s9hq-tpch*` | 22 / 22 | IDENTICAL |
| TPC-DS SF0.25 | `s9hq-tpcds-sf025*` | 99 / 99 | CHANGED (363 lines) |
| TPC-DS SF1 | `s9hq-tpcds-sf1*` | 99 / 99 | CHANGED (374 lines) |

Every changed line differs only in `width=`, checked line by line against the BASE. No plan shape,
row count or cost moved. An aggregate or computed column no longer carries its input's typmod, so its
width estimate is the type's default, as in PG: 85 (SF0.25) and 89 (SF1) changed nodes now equal PG
18.3's width, and 3 nodes of Q5 at each scale moved away from it.

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed. Because plan text
changed, the full capture is committed (S9(4a)); this directory becomes the BASE for all three
corpora. Item 10 is live: M-NIGHTLY, then the M0149 queue.
