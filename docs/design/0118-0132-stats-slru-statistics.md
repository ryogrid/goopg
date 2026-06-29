# 0118-0132 — `stats` enabler rung 8 (final): `pg_stat_slru` SLRU statistics

**Status:** accepted
**Milestone:** M0118-0009 (Upstream isolation-spec suite pass-through — misc/system-level specs)
**Spec:** `postgres/src/test/isolation/specs/stats.spec`
**Predecessor:** [0118-0131](0118-0131-stats-relation-transactional-counters-2pc.md) (rung 7 — transactional relation-counter staging + 2PC)
**Kind:** ENABLER, not a promotion. `stats.spec` stays `defer` (one final blocker remains — see "Remaining").

## Summary

The final cumulative-statistics rung implements the `pg_stat_slru` view's
**notify SLRU `blks_zeroed`** counter plus the `block_size` preset GUC the spec
uses to size its test payload, advancing `stats.spec`'s first divergence from
line **3072** to line **3732** — *every* SLRU permutation (own-transaction,
separate-transaction, uncommitted-not-visible, and all three
`stats_fetch_consistency` models with `pg_stat_clear_snapshot`) now matches
PostgreSQL 18.3 byte-for-byte. L3732 is the spec's **last** permutation.

## Problem

The spec exercises the notify SLRU's page-zeroing counter:

```
step s1_slru_save_stats { INSERT … VALUES('notify','blks_zeroed',
    (SELECT blks_zeroed FROM pg_stat_slru WHERE name = 'notify')); }
step s1_big_notify { SELECT pg_notify('stats_test_use',
    repeat(i::text, current_setting('block_size')::int / 2)) FROM generate_series(1,3) g(i); }
step s1_slru_check_stats { SELECT current.blks_zeroed > before.value
    FROM test_slru_stats before JOIN pg_stat_slru current ON before.slru = current.name
    WHERE before.stat = 'blks_zeroed'; }
```

It LISTENs, fires a large `pg_notify`, forces a stats flush, and asserts
`blks_zeroed` strictly increased. Upstream this counts `SimpleLruZeroPage()`
events in `asyncQueueAddEntries()` as the committed notifications advance the
async-queue head across SLRU pages.

Two gaps in goopg:

1. **No SLRU counter.** goopg's `pg_stat_slru` virtual view returned static
   all-zero rows under the *pre-PG-17* directory names (`pg_notify`, `pg_xact`,
   …), so the spec's `WHERE name = 'notify'` joined nothing and `blks_zeroed`
   never moved. goopg's async-notify queue (`notify.go`) is an in-memory
   per-session inbox, not an SLRU, so there are no real page-zeroing events.

2. **`current_setting('block_size')` returned NULL.** `block_size` was not a
   registered GUC, so the payload `repeat(i, NULL/2)` evaluated to NULL → an
   empty payload → the three notifications collapsed to one zero-length entry,
   never crossing a page boundary even once the counter existed.

## Design

All stats logic lives in `internal/executor`; the writer is reported from the
(server-side) notification-publish path.

### 1. Modelled notify-SLRU counter (`pgstat_slru.go`, new)

A process-global `slruStatsManager` models the notify SLRU's `blks_zeroed` by
tracking a modelled queue head (`notifyQueueHead`, bytes). `RecordNotifyQueueWrite(n)`
advances the head by `n` and increments `blksZeroed` by the number of `slruPageSize`
(8192-byte) pages newly crossed — exactly the `SimpleLruZeroPage()` events upstream
would record (the page containing the old head was already zeroed, unless the
queue was empty, in which case the first page is zeroed too). All other SLRUs and
counters stay zero (goopg does not model their page traffic; the spec reads only
notify `blks_zeroed`).

`snapshotAll()` returns a by-name copy of every SLRU row (mirrors
`pgstat_build_snapshot_fixed` copying the whole fixed-amount array), and
`fetchSLRURows(ctx)` renders the rows honouring `stats_fetch_consistency` (below).

### 2. Counting hook (`server/notify.go`)

`publishPendingNotify` (the single COMMIT-time notification-publish point, for
both the autocommit and explicit-transaction paths) sums the modelled byte
length of the committing transaction's buffered notifications
(`notifyEntryBytes` = fixed `AsyncQueueEntry` header + NUL-terminated channel +
payload, MAXALIGN'd) and calls `executor.RecordNotifyQueueWrite`. **Gated on a
listener** (`hub.hasAnyListener()`): PostgreSQL only writes to the shared queue
when a backend is LISTENing — which is exactly why the spec runs `s1_listen`
first. Counting at COMMIT (not at buffer time) is what makes the in-transaction
checks read `f` (no page zeroed until the notify commits) and the post-commit
check read `t`.

### 3. `stats_fetch_consistency` integration (`pgstat_functions.go`)

SLRU stats are *fixed-amount* statistics and share the per-transaction snapshot
with function stats. `funcStatSnapshot` gains `slruFrozen` (snapshot mode) and
`slruCache` (cache mode). The shared `ensureFullSnapshot(snap)` helper now copies
**both** the function store *and* the SLRU store at the first access to any
cumulative statistic in the transaction — so under `snapshot` a variable-amount
(function) access freezes SLRU too, and vice versa (the spec's "variable-amount
access caches fixed-amount stat too" / "the other way round" permutations).
Under `cache` the whole SLRU set is cached on its first SLRU access, independent
of function objects. Discarded at transaction end / `pg_stat_clear_snapshot()`
like the function snapshot (rung 4, 0118-0126).

### 4. Live, snapshot-aware view rows (`operators.go`)

`valuesOp.Open` already special-cases session-specific virtual views by name
(`pg_prepared_statements`, `pg_extension`). It now serves `pg_stat_slru` from
`fetchSLRURows(ctx)` so the executor path returns live, snapshot-aware rows; the
static catalog `VirtualRows` fallback (used only by non-executor readers such as
`pg_dump`) is corrected to the PG-17+ `pg_stat_slru` names (`notify`,
`commit_timestamp`, … `other`) instead of the on-disk directory names.

### 5. `block_size` preset GUC (`config/defaults.go`)

Registered `block_size = 8192` as a read-only `PGC_INTERNAL` preset (not
settable, not in `postgresql.conf.sample`), mirroring upstream's BLCKSZ report,
so `current_setting('block_size')` resolves and the spec's payload is the
intended ~4 KB × 3.

## Why this is faithful and low-blast-radius

- The notify counter is bumped only at a committed notify with a listener; every
  other commit is a no-op. Query *output* is unchanged for any session that does
  not read `pg_stat_slru`.
- The snapshot extension is gated exactly as the function-stats snapshot
  (explicit transaction only); TPC-H / pgbench / other isolation specs do not
  read `pg_stat_slru` inside a multi-statement transaction.
- `block_size` is a preset report GUC; it cannot be set and is excluded from the
  sample config (sample-config parity test unaffected).

## Testing / gates

- New `TestSLRUNotifyBlksZeroed` (executor) — page-crossing counter model
  (no-op on zero write, ≥2 pages for a 12 KB write, strict increase, other SLRUs
  zero). New `TestNotifyEntryBytes` / `TestHasAnyListener` (server). Both also
  under `-race`.
- `go test ./internal/executor/ ./internal/server/ ./internal/config/
  ./internal/catalog/` PASS; `go build ./...` clean.
- `TestFetchFuncStatConsistency` / `TestStatsGUCs` PASS (no regression to the
  consistency models or stats GUCs), under `-race`.
- `TestPort_IsolationStats` soft probe: first divergence **L3072 → L3732** (every
  SLRU permutation matches PG 18.3); `TestPort_IsolationAsyncNotify` +
  `TestPort_TwoPhaseCommitSameBackend` PASS (no regression).
- pgbench CI-parity smoke = `.githooks/pre-commit`.

## Remaining — the one blocker to promoting `stats` to `pass`

L3732 (the spec's **last** permutation) reads
`pg_stat_get_function_calls(test_stat_func)` after `s1_clear_snapshot`, expecting
`1`; goopg returns NULL. Root cause is **not** SLRU: the permutation has no
`s2_track_funcs_all` step and relies on `track_functions = 'all'` having been set
in an *earlier* permutation and **persisting across permutations**. Upstream
`isolationtester.c` opens one connection per session **once** (in `main`) and
reuses it for every permutation, so session GUCs leak forward; goopg's
`IsolationRunner.runPermutation` opens fresh per-session connections each
permutation, resetting `track_functions` to its boot default (`none`), so the
call is untracked → NULL.

Closing this requires hoisting the runner's per-session connections to the
spec level (open once, reuse across permutations, run only session `setup` SQL
per permutation) — a shared test-infrastructure change touching all ~117
strict-passing isolation specs, deferred to its own loop as the **promotion
rung**. With that, `stats.spec` matches PG 18.3 end-to-end and promotes to
`pass`, closing the last failed M0118-0009 spec.
