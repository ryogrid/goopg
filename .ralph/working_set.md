(idle — nothing in flight)

Last loop: **`M0125-0008` CLOSED — and it took `M0125-0023` (Q95) with it.**
`EXISTS (…) AND NOT EXISTS (…)` over one outer rel returned MORE rows than
either conjunct alone. One derivation in `internal/planner/plan.go`
(`Join.Output()` returns `Left.Output()` for Semi/Anti). Design
`docs/design/0125-0008-semi-anti-conjunction-residual.md` (indexed); 2 ledger
rows; 2 regression tests, both verified RED with the fix reverted.

Nightly triage: `ci/logs/action-items.md` unchanged (mtime Jul 25 03:20), all
26 `AI-` subjects already filed — no-op.

## NEXT (banner order, rewritten this loop)

1. **`M0125-0013`** (Q47) — now the top M0125 item; banner marks it NEXT.
2. Then `M0125-0002 / -0003 / -0004 / -0005` (the 5 remaining open items).
Owed independently, now **two commits deep**: one full 99-query SF0.5 gate
run on a quiet host (own ledger row, 2026-07-30).

## Facts the next loop should NOT re-derive

- **All three CKMISMATCH cells are GONE.** Q16 `40dbec0df91d2438`, Q94
  `04afc1b69831a5ea`, Q95 `e498634c02595c29` — each equals the SF0.5 oracle.
  Do not re-probe them.
- Root cause worth remembering as a CLASS: a field every writer sets to the
  same derived expression should BE that expression. Semi/Anti `schema` was a
  copy of `Left.Output()` refreshed at 4 sites; `rewriteMultiWayChain` (a 5th
  pass) OID-sorts the subtree **in place** and knew nothing about it, leaving
  a stale *permutation* — widths matched, so nothing detected it.
- **Classify a subquery defect by the JOIN the planner builds, not the SQL
  keyword.** `IN (subquery)` → `JoinTypeSemi`, same as `EXISTS`. That mistake
  cost a whole filed task (M0125-0023).
- MHJ-packing threshold is **3 base tables** — below it, semi/anti bugs of
  this class do not reproduce. Use ≥3 in any repro.
- **DEFERRED, same class, unfixed:** `NestedLoopIndexJoin.Output()`
  (`plan.go:707`) caches identically; its refresher `reconcileNLILayout` is
  gated on `costDrivenJoinOrder` (off by default). Needs its own reproducer
  first — remapping NLI keys previously broke TPC-H Q9/Q21.
- SF0.5 goopg cluster takes db **`postgres`** (not `tpcds05`; that is the PG
  side). PG oracle :65438 takes user **`ryo`**. `bench/tpcds/server.sh
  {start|stop} {pg|sf05}`.
- The sweep supports **`QUERIES="16 94 95" scripts/tpcds-sf05-regression.sh
  sweep`** for a subset probe — self-labels as NOT a gate result.
- Pre-existing TIMEOUTs, NOT regressions: Q10, Q14, Q35, Q69 (each TIMEOUT in
  sweeps `-181319`, `-210715`, `-221359`, `-225808`).
- `plan-gate` picks the newest `plan_snapshots/*.txt` by mtime — currently
  `tpcds-round2-head.txt`, which despite its name holds **TPC-H** plans and
  needs the 65433 server up (`bench/tpch/setup_goopg.sh`), db/user `tpch`.

Gates run: planner + executor package suites PASS; units suite PASS;
`tpch-spotcheck.sh` PASS (Q12=2, Q13=35); `make plan-gate` 22/22 MATCH;
SF0.5 **subset probe** over all 13 EXISTS/IN queries — PASS=9 MISMATCH=0
CKMISMATCH=0 ERROR=0 TIMEOUT=4 (all 4 pre-existing); `make ralph-state-guard`
(auto-repaired); pgbench smoke via the commit hook.

In-flight: none.
