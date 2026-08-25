# pg_stat_activity Probes — Design Bundle (not_ralph)

Scope: verify that goopg's `pg_stat_activity` recording is low-overhead and
GC-friendly, close the probe-coverage gaps, and validate the result with a live
pgbench wait-event sampling harness cross-checked against pprof.

Base commit: `977a487e0f6b33dd3787bd31803ba4b3f02ef9c3` (branch `waitevent-impl`).
Worktree: `.claude/waitevent-impl`. The upstream oracle lives at
`./postgres/` (read-only; reached via a symlink in this worktree).

## Documents

| file | content |
|---|---|
| [01-upstream-probe-survey.md](01-upstream-probe-survey.md) | Where PG 18.3 inserts its pgstat probes, and the instrumentation policy they imply |
| [02-goopg-current-state.md](02-goopg-current-state.md) | Current goopg implementation, hot-path cost / GC analysis, existing probe inventory |
| [03-design.md](03-design.md) | Detailed design: PG→goopg probe mapping, gap fixes, zero-allocation discipline |
| [04-execution-plan.md](04-execution-plan.md) | Phased execution plan with gates and risks |
| [TODO.md](TODO.md) | Work-plan checklist |

## Verdict up front

The foundation (`internal/utils/activity`) already meets the low-overhead /
low-GC bar: wait info is packed into a single `uint32` via init-time interned
maps, the hot path is bounds-check + 2 read-only map lookups + 2 atomic stores
(~tens of ns, **zero allocations**), and slot memory is preallocated per
backend. No base redesign is required. The work is therefore:

1. fill missing probe points (Lock:transactionid at `Manager.WaitForXID`,
   Timeout:PgSleep in `evalPgSleep`, advisory waits),
2. document the LWLock-class policy (deliberately not mapped to Go mutex
   parks),
3. prove it live with `scripts/pgbench-wait-sample.sh` + pprof correlation.
