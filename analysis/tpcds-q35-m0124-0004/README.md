# M0124-0004 — Q35 row-count probe artefacts (2026-07-29)

**Both timing readings here are VOID.** They were taken while the nightly CI
batch was running (fired `2026-07-29T00:23:44`; its TPC-H stage held a capped
goopg server on :65434 at 112% CPU / 7.5 GiB RSS on the 16-core host). They are
kept so a later loop does not re-derive them, not as evidence about Q35.

| file | what | verdict |
|---|---|---|
| `sweep-20260729-051827.txt` | solo SF0.5 sweep, fresh server, `TIMEOUT_SEC=900` | `TIMEOUT` 921 s — **void** |
| `goopg-sf1-explain-analyze.txt` | SF=1 `EXPLAIN (ANALYZE, TIMING OFF)`, fresh server, 1800 s | `rc=124` at 1846 s — **void** |
| `goopg-sf05-explain.txt` | plain `EXPLAIN` at SF0.5 | valid — plan shape |
| `goopg-sf1-explain.txt` | plain `EXPLAIN` at SF=1 | valid — **identical shape to SF0.5** |

The two `EXPLAIN`s are the loop's durable result: Q35 is RC-8's exact
`exists(…) and (exists(…) or exists(…))` shape, with the correlation landing as
a nested-loop **Filter** (`$0 = ss_customer_sk`) rather than an index condition,
so each of three `EXISTS` re-scans a whole fact table per outer row. The plans
match at both scale factors and `customer` holds 100,000 rows at both, which
rules out a plan flip as the explanation for Q35 being slower on half the data.

Full analysis and resume point:
`docs/design/0124-0004-q35-rowcount-resolution.md` §"Execution record (2026-07-29)".
