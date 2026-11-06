# M0146-0014 — census at 8c85bc43d (2026-10-30): exhausted

Trigger: `a45162dc3` changed `internal/executor` by adding the `pg_logical_emit_message` builtin handler
(M0122-0014). Under S9 the previous census, `routing-20261029n` at `45b925ffd`, can no longer be reused. The census
was captured at `8c85bc43d`. The goopg plan text is byte-identical to `routing-20261029n` on all three corpora; only
the header lines (engine id, repo head) differ.

| corpus | capture | match | before (45b925ffd, routing-20261029n) |
|---|---|---|---|
| TPC-H | `s9w15-tpch*` | 22 / 22 | 22 / 22 |
| TPC-DS SF0.25 | `s9w15-tpcds-sf025*` | 99 / 99 | 99 / 99 |
| TPC-DS SF1 | `s9w15-tpcds-sf1*` | 99 / 99 | 99 / 99 |

CATEGORIES-EXCL-MATCH is 0 in every category on every corpus. DIVERGENCE-CLASSES shows divergent=0 everywhere. No
task is filed.
