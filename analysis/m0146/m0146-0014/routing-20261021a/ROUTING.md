# M0146-0014 — S9 re-check at b084797b2 (2026-10-21): exhausted

Trigger: `44cf7d275` (M-NIGHTLY AI-20261020-013101-002) touched `internal/executor` after the
2026-10-20 pass (`exhausted at 04839ab7f`). That commit only adds wait-event reporting
(Lock:transactionid / tuple / relation / spectoken); no plan change is expected.

These are fresh `jointree-parity-capture.sh` captures of all three corpora on HEAD's code
tree (`b084797b2`; the commits since 04839ab7f touch nbtree, transam, wait-event reporting
and test harnesses only).

| corpus | capture | match | before (04839ab7f) |
|---|---|---|---|
| TPC-H | `s9y-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9y-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9y-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus.

## Actionable records

None. No task is filed.
