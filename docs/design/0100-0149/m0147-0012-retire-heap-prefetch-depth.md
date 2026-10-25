# M0147-0012 — retire `GOOPG_HEAP_PREFETCH_DEPTH` (bitmap heap look-ahead window)

Status: landed 2026-10-24.

Milestone: [M0147 lever retirement](../../milestones/0147-lever-retirement.md), phase 1. Lever count 29 → 28
(`scripts/lever-count.sh`).

## What was retired

The E-19 / EX5-05 slice S4 caller (`docs/design/storage-prefetch-buffer/E19-INSTALLING-PREFETCH.md`). With
`GOOPG_HEAP_PREFETCH_DEPTH=D` set, a serial `Bitmap Heap Scan` kept a FIFO of up to D in-flight installing reads
(`Pool.StartRead`) for the blocks its TID bitmap would visit next, refilled at each page transition and drained at
every lifecycle end.

The S4 arm measured NO-GO: 36 % slower cold at depth 16 on Probe 0's witness, with I/O byte-identical
(`docs/design/not_ralph/minimize_datum/TODO_ALL.md:4144-4190`). A bitmap heap scan visits its blocks in ascending
order, and kernel readahead already covers that pattern. Probe 0's budget was on the index scan's heap fetch, which
is a different access pattern. The knob stayed default 0 (no window) from the day it landed.

## Removed

- `internal/executor/bitmap_prefetch.go`: `prefetchDepthEnv`, `maxPrefetchDepth`, `heapPrefetchDepth`,
  `heapPrefetchWindow` and its `refill` / `holds` / `take` / `drain`.
- `internal/executor/bitmap_prefetch_test.go`: the window's seven tests (pin balance across EOF, mid-scan drain and
  an injected read error; stale-head drop; depth cap; knob parsing; depth-0 inertness). They tested only the window.
- `operators_bitmap.go`:
  - the `bitmapHeapScanOp.pf` field and its construction in Open;
  - the four `o.pf.drain()` calls (Rescan, Close, EOF, Pin error);
  - the refill/take branch at the page transition. The page is now pinned with `Pool.Pin` directly, which was the
    path taken whenever the window was nil.
- `tidbitmap.go`: `tbmIterator.peekBlocks`, which only the window called.

**Kept.** The storage layer is unchanged: `Pool.StartRead` / `ReadOp.Finish` / `Abort`, the vectored read and the
prefetch accounting (S1–S3). Their own tests (`internal/storage/prefetch_test.go`, `prefetch_accounting_test.go`)
still run. The TODO_ALL entry records them as reusable: if the item reopens, the resume point is the index scan's
heap fetch, which is a btree change.

## Inert proof (AGENT.md C6)

- `heapPrefetchDepth()` returned 0 when the variable was unset, and `newHeapPrefetchWindow` returned nil at depth 0.
  So `o.pf` was nil on every default run.
- Each removed call was a no-op on a nil window: `drain` and `refill` returned at once, and `take` returned nil, so
  the scan fell through to `Pool.Pin`. That fallback is the code that stays.
- Nothing in scripts, CI, bench configs or the Makefile sets the variable. A `git grep` finds only design docs and
  the tracker.

## Gates

- units PASS;
- spotcheck PASS (Q12=2, Q13=35);
- acceptance arm 24 MATCH;
- sf025 PASS=99 (61 ck), run with `SF025_NO_PLANS=1`;
- standalone plan capture PLAN-SHAPE same=99 changed=0;
- census on the tree: TPC-H 22/22, SF0.25 99/99, SF1 99/99, every category 0;
- TestPort\_RegressSuite PASS;
- TestPort\_Isolation PASS.

`scripts/fireset-scope.sh` puts the change out of scope, as it did for the other executor-only retirements.

## Supersedes

The 2026-09-07 `take3-E-19-installing-prefetch` ledger row, for the S4 caller only: the window and its knob are
gone. S1–S3 and the row's resume point (index-scan heap fetch) stand.
