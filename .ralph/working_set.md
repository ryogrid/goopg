(idle — nothing in flight)

M0125-0034's Q65 arm landed loop #17 (2026-07-31) — **M0125-0034 IS CLOSED**.
Committed on `tpcds-fix2` (see git log for the loop-#17 commit), pushed.

**Next loop: read the `## Current Priority` banner FIRST.** It now names
`-0035`'s CTE-body arm as the next selection (single-reference CTE inlining,
PG `subselect.c::inline_cte`, + equivalence-class constant propagation
`equivclass.c`; Q31's 6×-referenced `ws3` is the must-NOT-inline control),
then `M0125-0045`, then `M0125-0038` last.

Findings worth not re-deriving (all in
`docs/design/0125-0034a-comma-from-connectivity-order.md` §7):
- `parser.RangeVar.Lateral` now exists. It is set at BOTH accept sites in
  `internal/parser/select.go` — `parseRangeVar` AND the `JOIN LATERAL` path,
  which consumes the keyword before `parseRangeVar` can see it. Only the
  join-order pass reads it; LATERAL *evaluation* is still uncorrelated
  (ledger row 2026-07-31).
- `reorderCommaFromByCardinality`: table functions decline the whole list
  (PG treats LATERAL as noise before a function item — absence proves
  nothing); `Lateral` derived tables decline; non-lateral derived tables are
  opaque relations → connectivity mode, same standing as WITH references.
- Q72 straddles the 300 s cap (307/309/314 s over three loops) and flips
  status noise-wise between sweeps; it is all-`JOIN…ON`, unreachable by the
  comma-FROM pass — do not chase it as a regression.
- SF0.5 timeout class is now Q78 (+ the Q72 straddle). Q78 = -0035 CTE-body.
- Plan-diff baseline: use `m0125-0044-after` with `PLAN_DB=tpch
  PLAN_USER=tpch` (needs the 65433 TPC-H server up: `bench/tpch/setup_goopg.sh`).

Gates run this loop (all PASS): `go build ./...`; `go vet
./internal/planner/ ./internal/parser/`; `go test ./internal/planner/...
./internal/executor/ ./internal/parser/`; full 99-query TPC-DS SF0.5 gate,
one binary, 3 chunks (`analysis/m0125-0034c/gate/`) — PASS=93 MISMATCH=0
CKMISMATCH=0 ERROR=0 TIMEOUT=2 (Q72 straddle, Q78) SKIP=4, exactly 2/99
cells moved vs loop #16; `scripts/tpch-spotcheck.sh` RESULT=PASS (Q12=2
Q13=35, 40.5 s); `make plan-diff LABEL=m0125-0044-after` 22/22 MATCH;
`RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh`;
`make ralph-state-guard`.

In-flight: none. All goopg bench servers stopped (65433/65436/65437 down;
65433 was started for plan-diff and stopped after). PG oracle :65438 left as
found. Private gate binary at `tmp/goopg-sf05-m0125-0034c-bin`.
