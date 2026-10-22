# M0146-0014 — S9 re-check at 250e20249 (2026-10-21): exhausted

Trigger: `250e20249` (M0119 pg_relation_filepath) touched `internal/executor` (dbsize builtins
and regclass argument resolution) after the `b084797b2` pass.

Fresh `jointree-parity-capture.sh` captures of all three corpora on HEAD's code tree:

| corpus | capture | match | before (b084797b2) |
|---|---|---|---|
| TPC-H | `s9z-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9z-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9z-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus.

## Actionable records

None. No task is filed.
