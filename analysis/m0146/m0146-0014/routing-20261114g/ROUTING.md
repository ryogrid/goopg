# M0146-0014 — census at 23d395051 (2026-11-14): exhausted tree=d3d21ce33a9a086757cc2fa4ca46aabda5258300

Trigger: `scripts/s9-census-due.sh` returned DUE (base routing-20261114f, reason=plan-relevant-change)
— code commit `23d395051` (M0148-0001c3a4 item (g): the bitmap node EXPLAIN cost line as
create_bitmap_subplan stamps it; `internal/optimizer/plancost.go`, `costbitmap.go`, `pathbitmap.go`).
Its own fire-set gate fired on exactly the queries with a Bitmap Index Scan (TPC-DS Q3 Q32 Q53 Q54
Q55 Q63 Q92, TPC-H Q2 Q11 Q17 Q20 Q21); every fired query passed on values.
`git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the capture built its
engines, hence `tree=` above (HEAD's tree). Capture repo head `3ab65d690`.

Capture: `BASELINE_REV=5bf6e720f CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9cl tmp/census-20261114g`
(BASELINE_REV = the head consumed by routing-20261114f; candidate engine `tmp/fireset-bin/s9cl-candidate`
sha256 `44ac1042b9de3dc2b4a37eaaabf80d412d7e23d124920d59b4bad4dbd916b381`).

| corpus | capture | match | `--compare` vs BASE (routing-20261104e) |
|---|---|---|---|
| TPC-H | `s9cl-tpch*` | 22 / 22 | CHANGED (6 lines) |
| TPC-DS SF0.25 | `s9cl-tpcds-sf025*` | 99 / 99 | CHANGED (11 lines) |
| TPC-DS SF1 | `s9cl-tpcds-sf1*` | 99 / 99 | CHANGED (14 lines) |

Every changed line is a `Bitmap Index Scan` / `BitmapAnd` / `BitmapOr` cost annotation (startup 0.00,
total = indextotalcost, width 0, as PG prints them) or the `QUERY PLAN` header rule whose width
follows the widest line. No plan shape, row count or other node's cost moved; PLAN-PARITY is
22/22, 99/99, 99/99 as at the BASE. The SF0.25 Bitmap Index Scan lines equal PG 18.3's text in
6 of 7 places (the seventh, Q53's parameterized `store_sales_pkey` probe, is ledgered:
indextotalcost's loop_count proration).

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus (CATEGORIES identical to the BASE's,
SF1 `rendering=1` within a MATCH record). No task is filed. Because plan text changed, the full
capture is committed (S9(4a)); this directory becomes the BASE for all three corpora. Item 10 is
live: M-NIGHTLY, then the M0148 queue.
