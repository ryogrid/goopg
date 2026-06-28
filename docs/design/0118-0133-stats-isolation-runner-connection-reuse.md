# 0118-0133 — `stats` PROMOTION (final rung): isolation-runner per-session connection reuse (M0118-0009)

Status: accepted
Date: 2026-06-28
Spec: M0118-0009 (`stats.spec` cumulative-statistics isolation test)
Supersedes the open blocker recorded in [0118-0132](0118-0132-stats-slru-statistics.md).

## Summary

This is the **promotion** rung that flips `stats.spec` from `defer` to
**pass-required** (`runIsoSpecStrict`, `TestPort_IsolationStats`). It is *not* a
`pgstat` change — every pgstat subsystem the spec exercises (function /
relation / SLRU statistics, transactional `DROP FUNCTION` cross-session
visibility, 2PC stat drops) already landed in designs 0118-0123 … 0118-0132.
The one remaining divergence was in the **isolation test runner** itself.

## The blocker

`stats.spec`'s last group of permutations (e.g. the file's final permutation)
calls `s2_func_call` (`SELECT test_stat_func()`) and then reads
`pg_stat_get_function_calls(...)` via `s1_func_stats` — but those permutations
**never set `track_functions`** themselves. They rely on
`SET track_functions = 'all'` (issued by an *earlier* permutation, the last one
to set it being the snapshot-consistency block around spec line 297) **still
being in effect**.

Upstream `isolationtester.c` opens **one connection per session ONCE** (in
`main()`) and reuses it for *every* permutation. `track_functions` is a
session-level GUC set by a `step` (not by per-session `setup`, which here only
sets `stats_fetch_consistency`), so it **persists forward** across all
subsequent permutations. The expected `.out` was generated under those
semantics.

goopg's `IsolationRunner.runPermutation` previously opened **fresh** per-session
`sql.OpenDB` connections *each permutation*. Every permutation therefore started
from the boot default `track_functions = 'none'`, so `s2_func_call` was not
tracked and `pg_stat_get_function_calls` returned `NULL` instead of `1` — the
first divergence at output L3732 (the spec's last permutation).

## The fix

Hoist per-session connection creation from **permutation scope** to **spec
scope**, mirroring `isolationtester.c`:

- New `sessionConns` struct (`internal/testport/framework/isolation_runner.go`)
  bundles the per-session notice/notify queues, the backing `*sql.DB` handles,
  the live `*sql.Conn` connections, and the `pid → session` map.
- New `(*IsolationRunner).openSessionConns(...)` builds it **once** per spec in
  `RunSpec`: it attaches the lib/pq NOTICE + notification handlers, opens one
  connection per session, runs `SET application_name` once, and records each
  backend PID once (these are stable for the connection's lifetime).
- `RunSpec` holds the set across the whole permutation loop (`defer sc.close()`)
  and passes it into `runPermutation` instead of a shared `*sql.DB`.
- `runPermutation` no longer opens or closes connections. It still runs each
  session's `setup` SQL at the start of **every** permutation (upstream does
  too) — but that block only re-applies `SET stats_fetch_consistency='none'`; it
  does **not** reset the connection, so a step-set GUC like
  `track_functions='all'` stays in effect, exactly as upstream.
- `sc.drainQueues()` clears the transient notice/notification queues at the
  start of each permutation so leftover messages are not mis-attributed. The
  monotonic notice `total` (used by `notices <n>` completion blockers) is
  intentionally *not* reset — those blockers compare a delta against a per-step
  baseline, so cross-permutation accumulation is harmless.

### Robustness: post-permutation health check

With connections now reused, a timed-out step's abandoned goroutine could hold a
connection and contaminate the next permutation. After each permutation
`RunSpec` calls `sc.healthy(ctx)` — a trivial `SELECT 1` per session with a 1 s
deadline. If any connection is no longer idle (the round-trip blocks on the busy
connection until the deadline), the set is closed and rebuilt before the next
permutation. In the common case this is one cheap round-trip per session and
never triggers (a passing spec drives every step to completion, leaving all
connections idle). `close()` closes connections in a 3 s-bounded background
goroutine so a stuck lib/pq pending read cannot hang the runner.

## Why this is safe for the other ~117 strict isolation specs

The expected `.out` files are produced by upstream `pg_regress`, i.e. *with*
connection reuse. Switching goopg to reuse can therefore only make output **more
faithful**, never less:

- A spec that never sets a session GUC in a step, or always re-establishes it in
  per-session `setup`, is unaffected — reuse changes nothing observable.
- A spec that *did* rely on per-permutation GUC reset to accidentally match
  upstream would already have been diverging (upstream persists), i.e. it would
  not have been a passing strict spec.

The mechanical risk (a leaked transaction or stuck goroutine bleeding across
permutations) is contained by the health check + rebuild. The full
`TestPort_Isolation*` strict suite was re-run to confirm no regression.

## Oracle

`postgres/src/test/isolation/isolationtester.c` — `main()` opens
`PQconnectdbParams` once per session into `conns[]`; `run_permutation()` runs
the session `setup` SQL per permutation but never reconnects. The persistence of
session GUCs across permutations is the behavior mirrored here.

## Gates

- `go build ./...` clean.
- `TestPort_IsolationStats` (now **strict**, `runIsoSpecStrict`) — PASS:
  `stats.spec` matches PG 18.3 byte-for-byte across all permutations.
- Full `go test -run TestPort_Isolation ./internal/testport/` strict suite —
  PASS (no regression in the previously-passing specs from connection reuse).
- `docs/test-port/postgres-oracle-port-status.csv` D-002 rationale updated;
  `.md` regenerated via `cmd/gen-oracle-port-status`.
- pgbench smoke = pre-commit hook.
