# M0146-0014 — S9 re-check at 943cbd7b1 (2026-10-21): exhausted

Trigger: `943cbd7b1` (M-NIGHTLY AI-20261021-010544-001) touched `internal/executor`
(pg_last_wal_receive_lsn / pg_last_wal_replay_lsn) after the `250e20249` pass.

Fresh `jointree-parity-capture.sh` captures of all three corpora on HEAD's code tree:

| corpus | capture | match | before (250e20249) |
|---|---|---|---|
| TPC-H | `s9c-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9c-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9c-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus.

## Actionable records

None. No task is filed.
