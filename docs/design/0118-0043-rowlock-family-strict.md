# 0118-0043 — Harden the remaining M0118-0003 row-lock isolation specs to pass-required

**Status:** accepted
**Milestone:** M0118-0003 (Row locking) — regression-protection hardening (completes design 0118-0042)
**Date:** 2026-06-23

## Summary

Completes the M0118-0003 row-lock hardening begun in design 0118-0042 (which
promoted the four `skip-locked` specs). This loop promotes the remaining 16
dedicated row-lock isolation tests from observability (`runIsoSpec`, which
`t.Skip()`s a non-matching `defer` result) to **pass-required**
(`runIsoSpecStrict`, where a non-match is a hard red test). All 16 already match
PG 18.3 byte-for-byte; **no engine change** is involved.

With this change every spec named in M0118-0003's "All 20 specs PASS vs PG 18.3"
completeness claim is now machine-enforced — a regression in goopg's row-lock
engine surfaces as a red test instead of a silent skip.

## Specs promoted (16)

| spec | what it proves (row-lock path) |
|------|--------------------------------|
| `nowait`   | `FOR UPDATE NOWAIT` on a row already `FOR UPDATE`-locked fails fast (no block) |
| `nowait-2` | `FOR UPDATE NOWAIT` aborts `55P03` when a multixact `FOR SHARE` member blocks the upgrade |
| `nowait-3` | a blocking `FOR UPDATE` queues behind `s1` while `NOWAIT` from a third session fails fast |
| `nowait-4` | `NOWAIT` over a multixact lock structure |
| `nowait-5` | `NOWAIT` on an updated tuple chain |
| `lock-nowait` | heavyweight `LOCK TABLE … NOWAIT` early-grant special case (lockmgr `JoinWaitQueue`) |
| `tuplelock-conflict`  | the full 4-strength tuple-lock conflict table incl. SAVEPOINT-subxid multixacts |
| `tuplelock-update`    | a no-key UPDATE follows the ctid chain and propagates `FOR KEY SHARE` forward (no block) |
| `tuplelock-partition` | no-key vs key UPDATE conflict with a concurrent `FOR KEY SHARE` on a LIST-partitioned table |
| `tuplelock-upgrade-no-deadlock` | self-upgrading a held row lock while others wait does not deadlock; arrival-order grants; savepoint-rollback retry (M0118-0004; design 0118-0012) |
| `lock-update-traversal` | the 4-way row-lock-strength distinction: DELETE/key-UPDATE wait on `FOR KEY SHARE`, no-key UPDATE proceeds |
| `lock-update-delete`    | advisory-gated locker traverses an update chain: waits on in-flight DELETE/key-UPDATE, proceeds on no-key |
| `update-locked-tuple`   | `KEY SHARE` (FK) does not conflict with a no-key UPDATE of the referenced row (no block / no 40001) |
| `propagate-lock-delete` | propagated FK `KEY SHARE` locks survive a parent UPDATE; the later DELETE waits on in-flight child INSERTs then raises `23503` |
| `lock-committed-update`    | `FOR KEY SHARE` over a concurrent committed **no-key** UPDATE (compatible — locker proceeds after the wait) |
| `lock-committed-keyupdate` | `FOR KEY SHARE` over a concurrent committed **key** UPDATE (conflicts — RC follows the ctid chain, RR/SSI raise `40001`) |

All pass on the cumulative M0118-0003/0004 work already in the tree: the
multixact-aware lock producer (`stampLockInner` conflict-before-wait gate, the
multixact member conflict check, `stampMultiLock` survivor preservation), the
subxact-scoped row-lock infrastructure, the `WaitForXID`/ctid-chain follow on a
no-key update, and the transaction-scoped heavyweight `LOCK TABLE` lifecycle.
This change only hard-asserts that behaviour.

`tuplelock-upgrade-no-deadlock` is formally an M0118-0004 (deadlock) spec
(promoted to `pass` in design 0118-0012) but is also listed in the M0118-0003
20-spec set; it is hardened here alongside its row-lock siblings.

## Change

`internal/testport/isolation_port_test.go`: `runIsoSpec` → `runIsoSpecStrict` in
the 16 dedicated test functions above. No production code changes. No
catalog/parser/executor/lockmgr changes.

## Oracle

`postgres/src/test/isolation/specs/{nowait,nowait-2,nowait-3,nowait-4,nowait-5,
lock-nowait,tuplelock-conflict,tuplelock-update,tuplelock-partition,
tuplelock-upgrade-no-deadlock,lock-update-traversal,lock-update-delete,
update-locked-tuple,propagate-lock-delete,lock-committed-update,
lock-committed-keyupdate}.spec` and their `expected/*.out`. Compared
byte-for-byte (after isolationtester normalization) against the local PG 18.3
install via `IsolationRunner.RunAndCompare`.

## Verification

- All 16 promoted tests — strict PASS (single `go test` run over the family,
  `ok` in ~83 s; `runIsoSpecStrict` fails rather than skips on a non-`pass`, so
  `ok` proves every one byte-matched PG 18.3).
- Build + `go vet ./internal/testport/` clean.
- pgbench TPC-B smoke is the machine-enforced pre-commit hook.

## Scope

With designs 0118-0042 + 0118-0043, **all 20 M0118-0003 row-lock specs are now
pass-required** (strict). M0118-0003 is fully hardened; no row-lock spec can
regress silently. The genuinely **deferred** M0118-0008 DDL/maintenance tail
(`alter-table-{1,2,4}`, `detach-partition-concurrently-*`,
`partition-concurrent-attach`, `partition-drop-index-locking`,
`reindex-concurrently-toast`, `vacuum-no-cleanup-lock`, `plpgsql-toast`) still
diverges and needs real engine work — out of scope here.
