# M0146-0014 — census at ed2227a64 (2026-10-30): exhausted

Trigger: `4a0cf54cc` changed `internal/executor` (M0122-0014: a CheckCmdReplicaIdentity check in updateOp.Open /
deleteOp.Open). The goopg plan text, with `#` header lines excluded, is identical to `routing-20261030b` on all three
corpora.

| corpus | capture | match |
|---|---|---|
| TPC-H | `s9w17-tpch*` | 22 / 22 |
| TPC-DS SF0.25 | `s9w17-tpcds-sf025*` | 99 / 99 |
| TPC-DS SF1 | `s9w17-tpcds-sf1*` | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. No task is filed.
