# fix-06 — GC / allocation: engage the per-statement arena (P2)

## Problem (evidence)

Aux A2 (`GOGC=400`) gained **+43 %** TPS (1,269 → 1,816) — more than
disabling the profiler (A5, +38 %) — showing allocation/GC pacing still
costs a large TPS fraction even though GC frames are small in the CPU
profile (mallocgc cum 4.8 %; the profile denominator is inflated by the
fix-01 stack storm, and less GC also means less runtime-lock traffic that
profiling amplifies). The May-2026 analysis measured 105–350 KB allocated
per UPDATE; the top OLTP allocators (`planner.Plan`,
`updateOp.Next`/`tryApplyHOTUpdate`) are still present. A per-statement
memory-context mechanism exists and **is already acquired per statement**
on the OLTP path — `internal/mctx/mctx.go` (M0107-0001, which replaced the
legacy `executor.Arena`), acquired in `dispatchSimpleQueryViaExecutor` at
`internal/server/dispatch.go:288` — but the hot allocators above do not
route their allocations through it, so per-statement garbage still reaches
the GC.

## PostgreSQL approach (03 §… / prior art)

palloc `MemoryContext`s: per-query `ExecutorState` and per-tuple contexts
are reset wholesale between statements (aset.c) — allocation is pointer-bump
and release is O(1), no tracing GC. The Go analogue in this repo's practice
doc (`practice/go_rdbms_performance_techniques.md` §1–2): arena per query,
`sync.Pool` for transient buffers, tagged-union `Datum` to avoid boxing.

## Design (measure-first discipline)

**Step 0 (prerequisite): land fix-01, re-profile.** The current profile
cannot cleanly rank allocators; re-run `run_su50.sh` post-fix-01 and rank
`alloc_space` in the OLTP window (use a mid-run allocs delta, not
cumulative-from-start — startup DDL recovery pollutes the cumulative view;
see fix-05).

**Step 1: route the hot allocators through the existing per-statement
mctx.** The bracket already exists
(`dispatchSimpleQueryViaExecutor` acquires a `mctx.KindStmt` context at
`internal/server/dispatch.go:288` and releases it at statement end); the
work is converting the ranked allocation sites (expected: plan-node
construction in `internal/planner` and the update-row buffers in
`updateOp.Next`/`tryApplyHOTUpdate`) to allocate from that mctx instead of
the global heap. Known trap (M0073/M0075
lessons, memory `m0073_arena_q5_heap_drop`): arena slot reuse aliasing —
cross-Kind String↔StringArena equivalence must hold at every comparison
site, and any datum escaping the statement (portal results, cached plans,
NOTICE payloads) must be copied out before reset. Start with the two
highest-ranked allocators only, gated per-site, not a blanket switch.

**Step 2: GOGC default review.** goopg already defaults GOGC=200
(`cmd/goopg/main.go:317`). A2 suggests 400 is better for OLTP on this
hardware profile *given GOMEMLIMIT as the backstop*; propose raising the
default to 300–400 after Step 1 lands (arena reduces garbage, which changes
the optimal GOGC — decide from the post-arena re-measurement, not A2
alone). Also fix the misleading GOMEMLIMIT log line
(`main.go:323-324`, logs the temporary maxint from a double SetMemoryLimit
swap).

## Expected lift

A2 bounds the blunt-knob version at +43 % pre-fix-01. Post fix-01/-03 the
GC share shrinks; realistic combined arena+GOGC estimate ×1.2–1.4. Treat as
a re-measured target, not a promise.

## Risks

- Aliasing/lifetime bugs (historically expensive in this repo — 608
  regression anchors; see practice card). Mitigate: per-site opt-in,
  cross-Kind equality tests, TPC-H Q12/Q13 spot-check mandatory.
- Raising GOGC increases heap high-water mark; GOMEMLIMIT=18 GiB backstop
  and the WSL2 memory-guard context must be documented in the operator
  notes.

## Verification plan

1. Post-fix-01 alloc re-profile ranks targets (recorded in this doc's
   follow-up section when done).
2. Unit + `scripts/tpch-spotcheck.sh` (Q12=2/Q13=35) + full units suite +
   pgbench smoke; `run_su50.sh` acceptance.
3. Soak: 10-minute pgbench standard run watching RSS vs GOMEMLIMIT.
