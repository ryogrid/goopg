# M0146-0014 — census at 0ad18e437 (2026-11-01, loop #15): exhausted

Trigger: `scripts/s9-census-due.sh` returned DUE (base `8ec4bfdd2dfa`, reason=plan-relevant-change)
with triggering paths `internal/nodes/datum.go`, `internal/parser/tokennums_gen.go`,
`internal/parser/yacc_parser.go` — the M0148-0001 commit `0ad18e437` (aggregate clause
cross-product grammar arms + bit_in/varbit_in prefix scan). The change is parse-shape and
bit-typed-input only: the nine new `name_or_call` arms accept `agg(...) [ORDER BY ...] FILTER
(WHERE ...)` / `WITHIN GROUP` spellings that previously died as 42601 — no TPC query contains a
FILTER, an in-args ORDER BY, or a WITHIN GROUP clause, so no existing statement re-parses; the
string→bit cast arm and `ScanBitInput` fire only on bit/varbit input, and the TPC corpora carry
no bit columns. `git diff --quiet -- internal cmd scripts bench go.mod go.sum` held while the
capture built its engines (the code commit is in HEAD; only `.ralph/` bookkeeping was dirty).
This is an unchanged census under S9(4a), so only this file is committed; the captures are
deleted from `tmp/census-20261101d/` after this record.
tree=02fe56c3ec1c7d265316f862ef5c7c2575f7b63d9f28a8015169ff32505719cb; capture repo head 0ad18e437
(the code commit; the recording commit is this file on top).

Capture: `BASELINE_REV=91bc8262a CORPORA="tpcds-sf025 tpcds-sf1 tpch" scripts/tpcds-fireset-gate.sh s9f tmp/census-20261101d`
(BASELINE_REV = the commit whose tree is `8ec4bfdd2dfa`, the base consumed by routing-20261101c,
so the plan A/B spans the whole window since that census — 91bc8262a..0ad18e437: the M0122-0015a
COPY per-database scoping (with the 18-assertion 005-filterfile port) and this commit; fires=none
on all three corpora, PLAN-PARITY 22/22 + 99/99 +
99/99 vs PG, and the A/B's own baseline-vs-candidate match was 22/22 + 99/99 + 99/99 with
divergent=0).

| corpus | match | CATEGORIES-EXCL-MATCH |
|---|---|---|
| TPC-H | 22 / 22 | all 0 |
| TPC-DS SF0.25 | 99 / 99 | all 0 |
| TPC-DS SF1 | 99 / 99 | all 0 |

Fire set: **none** on every corpus (plan A/B had no changed query), so no fireset execution was
required; gate-stamp tpcds-fireset PASS (head 0ad18e437, code_tree 02fe56c3ec1c…).
Top-residual: unchanged from routing-20261101c — the census is exhausted (no open plan-divergence
category; residual is the seam-decline count 2 (leaf-count) on the non-blocking channel).
Next census: fires on the next plan-relevant commit (S9(2) base-consumption rule).
