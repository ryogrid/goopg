# B-tree Insert Move-Right Check (M0055 follow-up / M-NIGHTLY AI-20260709-010336-082)

| field      | value |
|------------|-------|
| status     | landed |
| date       | 2026-07-09 |
| supersedes | — |
| relates to | `0055-0002-btree-multi-writer-split-protocol.md` |

## 1. Problem

`insertIntoBlock` (`internal/access/btree/btree.go`) is the single entry point
that pins a target block and either inserts `it` in place or splits the page,
for three call shapes:

- the leaf fast-path fallback after `tryInsertNoSplit` returns `errNeedsSplit`
  (`Insert` re-descends under `bt.splitMu`, then calls `insertIntoBlock`);
- the recursive parent-downlink insert after a child split
  (`insertIntoBlock` calling itself with a `path[]` ancestor);
- the root-lift downlink insert.

None of these call sites re-validated the target block's current high key
against the item being inserted — `insertIntoBlock` pinned `blk` and inserted
(or split) unconditionally. `bt.splitMu` only serializes structural writes
within one `*BTree` Go instance, and every backend opens its own instance per
statement (a prior finding, `M-NIGHTLY AI-20260709-010336-082` 4th loop), so a
concurrent split from a *different* connection can move the key range `it`
belongs in to `blk`'s right sibling in the window between the caller deciding
`blk` was correct (a fresh descend, or a `path[]` ancestor recorded earlier)
and `insertIntoBlock`'s own `pinW(blk)`. Missing PostgreSQL's Lehman-Yao
"move right" step at the insert entry point let such an item land on a
too-narrow page.

Symptom, reproduced via `pgbench -i -s 10 --no-vacuum` then `pgbench -c 60 -j
12 -T 30` racing an explicit `VACUUM pgbench_accounts` loop every 0.3s:
`bt_index_check` reported `high key invariant violated ... block 4026` — a
direct block dump confirmed block 4026 (internal, level 1) had its last
downlink key exceed its own `HighKey`, while all 246 preceding keys were
correctly bounded (see `.ralph/deferral_ledger.md`, row dated 2026-07-09,
task-id `M-NIGHTLY (AI-20260709-010336-082, 3rd pgbench reopen)`, status
`resolved`).

## 2. Fix

`insertIntoBlock` now loops on pin instead of pinning once:

```go
for {
    slot, err = bt.pinW(blk)
    op = readOpaque(slot.Page())
    if itemOvershootsHighKey(op, it.key) {
        next := op.Next
        bt.unpinW(slot)
        blk = next
        continue
    }
    break
}
```

`itemOvershootsHighKey` (`btree.go`, next to the existing `keyExceedsHighKey`
search-routing helper) applies the same leaf/internal boundary amcheck's
`VerifyBtreeItemOrder` enforces (`internal/amcheck/verify_nbtree.go:220-229`):

- leaf pages: an item is out of bounds only if it is strictly `>` `HighKey`
  (an item equal to `HighKey` is a valid duplicate boundary case);
- internal pages: an item is out of bounds if it is `>=` `HighKey`, because
  `HighKey` is itself the separator that was pushed up when this page last
  split — a downlink equal to it belongs to the right sibling by definition.

This is deliberately **not** the same predicate as `keyExceedsHighKey`, which
gates *search-key* descent (`descendToLeaf`) where equality to `HighKey`
means "stay on this page" for both leaf and internal levels — that asymmetry
is what the original bug's code-reading pass missed.

On overshoot the loop steps to `op.Next` and retries — a bounded number of
hops equal to however many concurrent splits raced ahead since the caller's
last read of the tree, matching PostgreSQL's `_bt_moveright`/`_bt_insertonpg`
protocol (`postgres/src/backend/access/nbtree/nbtinsert.c`). No `path[]`
threading is needed for the move-right hop itself: siblings are on the same
level, so the ancestor chain passed to a possible subsequent split/parent
insert is unaffected.

The pre-existing `op := readOpaque(slot.Page())` read further down
(previously computing `oldNext` for the split branch, and later read again
for `op.IsRoot()` after the split) now reuses the same `op` value captured by
the move-right loop — safe because the exclusive `pinW` latch is held
continuously from that read through the rest of the function, so the page
cannot change underneath it.

## 3. Scope / non-goals

- `tryInsertNoSplit` (leaf-only fast path) already had its own high-key check
  (`keyExceedsHighKey`, leaf semantics only) predating this fix; unchanged.
- Does not address `bt.splitMu`'s cross-connection non-serialization itself
  (separately tracked, deferral ledger note on the same row) — this fix
  tolerates that gap at every structural-insert entry point rather than
  closing it.
- Does not add a live-instrumentation trace; the root cause was confirmed by
  code reading against the exact reported symptom (leaf-vs-internal boundary
  asymmetry between `keyExceedsHighKey` and the stored-item invariant), and
  verified by re-running the exact repro that surfaced the bug.

## 4. Verification

- `go build ./...` clean.
- `go test ./internal/access/btree/...` and `go test -race
  ./internal/access/btree/...` PASS.
- `go test ./internal/amcheck/... ./internal/executor/...` PASS.
- Fresh repro (isolated port 5533, `pgbench -i -s 10 --no-vacuum` then two
  rounds of `pgbench -c 60..100 -j 12..20 -T 30` racing a `VACUUM
  pgbench_accounts` loop every 0.3s): 0 failed transactions both rounds;
  `bt_index_check('pgbench_accounts_pkey'::regclass, true)` reports no
  findings after either round (previously reproduced the block-4026 high-key
  violation on the pre-fix binary under the same recipe).
- `scripts/tpch-spotcheck.sh`: Q12 rows=2, Q13 rows=33 (canonical counts).

## 5. Follow-ups

- The nightly `pgbench` stage (`AI-20260709-010336-082`) should be watched
  for a 4th occurrence; if the class recurs, capture a live
  `DebugTraceSiblingRelink`-style trace around this new move-right loop
  rather than re-deriving from a cold repro.
- `bt.splitMu`'s cross-connection non-serialization remains open; closing it
  (making structural writes for one relation actually mutually exclusive
  across connections, not just within one `*BTree` instance) would remove
  the enabling condition for this whole bug family rather than tolerating it
  at each entry point.
