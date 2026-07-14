# 08-10 — Gate maybeForceGCAfterCommit to write transactions

status: design · date: 2026-07-14 · base: `a640d2b0` · gates: G-race, G-perf →
[README](README.md)

## 1. Problem and numbers

`maybeForceGCAfterCommit` costs **5.9 % of `-S` CPU** at scale 100 (06-03; 53 s
in a 90 s window at 90 k TPS) and 5.4 % at scale 500 (07-02) — on a **read-only**
autocommit workload that commits nothing durable. It is a leftover from the
M0107 fix (counter-first was applied to avoid the `ReadMemStats` STW, but the
helper still runs on the per-query commit path even for read-only statements).

## 2. Current-code map (verified at `a640d2b0`)

- **`maybeForceGCAfterCommit()`** — `internal/server/dispatch.go:68`: called on
  the commit path of every dispatched query, including read-only autocommit
  `SELECT`s (pgbench `-S`). The M0107 fix made it counter-first (no
  `ReadMemStats` per call), but the counter increment + branch still executes per
  query.

## 3. PostgreSQL reference

PostgreSQL has no per-query forced GC — Go's runtime GC is a goopg-specific
concern. The reference is simply: **a read-only transaction produces no garbage
worth forcing a GC for**, so the helper should be scoped to transactions that
actually wrote (allocated retained heap via WAL/tuple work).

## 4. Target design

Gate the call so it runs only for transactions that performed a write (or
allocated above a threshold): a `SELECT`-only autocommit statement skips it
entirely. Track a per-transaction "did-write" flag (the commit path already
knows whether WAL was emitted / the txn was read-only) and branch on it before
even the counter increment.

### Decision log

- **D1 — gate on did-write, not on statement kind.** A read-committed
  transaction can contain a write; the correct predicate is "did this
  transaction dirty anything," which the commit path already knows (WAL emitted /
  xid assigned). Read-only txns never assign an xid — that is the cheap test.
- **D2 — keep the M0107 counter-first design for write txns.** The STW-avoidance
  from M0107 stays; this only removes the call from the read path.

## 5. Invariants and failure modes

- **I1 — GC pressure from writes still managed.** Write-heavy workloads keep the
  forced-GC behavior (the M0107 regime); only read-only statements are exempted.
  G-perf on `-N` must show no regression.
- **F1 — a read txn that allocates a lot (big sort/hash) still needs GC.** If the
  did-write gate exempts a memory-heavy read, the general Go GC still runs (this
  helper is an *extra* forced GC, not the only one); confirm large read queries
  don't OOM without it (TPC-H is the test).

## 6. Migration slices

| # | slice | content | gates |
|---|---|---|---|
| S1 | gate the call | branch on the txn's did-write/xid-assigned flag before `maybeForceGCAfterCommit`. | G-race |
| S2 | perf acceptance | `-S`: the 5.4–5.9 % CPU share disappears; `-N` and TPC-H unchanged (no OOM, no throughput loss). | G-perf, G-tpch |

## 7. Test-impact matrix

| test | file | slice |
|---|---|---|
| dispatch commit path | `internal/server/dispatch_test.go` | S1 |
| memory behavior under read-heavy TPC-H | `scripts/tpch-spotcheck.sh` + heap watch | S2 |

## 8. Performance verification

`-S` at scale 100: `maybeForceGCAfterCommit` should vanish from the CPU profile;
`-S` TPS gains ~5–6 %. `-N` and TPC-H: no regression, no heap growth.

## 9. Open questions

- **O-GC-1** — Is "xid assigned" the exact right did-write predicate, or are
  there writes that don't assign an xid (e.g. unlogged/temp)? Confirm the flag
  covers all garbage-producing writes.
