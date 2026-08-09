# amcheck `checkunique`: heap-visibility-aware duplicate-key detection (M0119-0006)

status: accepted
date: 2026-08-10
milestone: M0119-0006 (pg_amcheck server tier)
supersedes: nothing
related: [0119-0006 opclass comparator dispatch](0119-0006-opclass-comparator-dispatch-amcheck.md),
[0110-0008 amcheck SQL surface](0110-0008-amcheck-sql-surface.md),
[0110-0005 verify_heapam engine](0110-0005-verify-heapam-engine.md)

## Problem

`bt_index_check(index, heapallindexed, checkunique)` and
`bt_index_parent_check(index, heapallindexed, rootdescend, checkunique)` accepted
the `checkunique` argument for call-shape compatibility with `pg_amcheck` but ran
no tier for it: passing `checkunique := true` on a corrupted unique index
reported the index clean. `pg_amcheck --checkunique` was therefore a no-op on
goopg — it could not fail, which is worse than not supporting the flag.

Upstream implements the tier in `contrib/amcheck/verify_nbtree.c`
(`bt_entry_unique_check` / `bt_report_duplicate`, driven from
`bt_target_page_check`, lines 1650–1815).

## What the tier actually asserts

Not "no duplicate keys exist". A healthy UNIQUE index routinely holds several
entries for one key: every dead row version left by an `UPDATE`/`DELETE` keeps
its index entry until VACUUM removes it. The constraint forbids only that **two
entries with an equal key both point at a heap tuple that is visible under the
checker's snapshot**.

That is why the check needs heap visibility, and why upstream registers a
transaction snapshot once per index check (`verify_nbtree.c:471`) when
`checkunique` is on. Getting this wrong in the permissive direction makes the
flag useless; getting it wrong in the strict direction fires on every healthy
table with churn — the more expensive failure.

## Design

Three seams, matching the engine-first/wire-later split the other amcheck tiers
already use.

### 1. Engine — `internal/amcheck/verify_nbtree_unique.go`

```go
type HeapVisibilityFunc func(tid storage.ItemPointer) bool

func VerifyBtreeUnique(src PageSource, indexName string,
    cmpKeys KeyComparator, visible HeapVisibilityFunc) ([]BtreeReport, error)
```

Descends meta → root → leftmost leaf, then follows `btpo_next` across the whole
leaf level, carrying upstream's `BtreeLastVisibleEntry` state (`lastVisibleEntry`
here: the most recent entry whose heap tuple was visible, plus the key it
carried). Per entry:

1. a key that differs from the carried one (under `cmpKeys`) ends the run and
   clears the state — nothing before a key change can conflict with anything
   after it;
2. an entry whose heap TID is not visible is skipped;
3. a visible entry when the state is already set **is** the violation.

Carrying the state across the page boundary is what finds a duplicate split
between a leaf and its right sibling, exactly as upstream does while walking a
level. The first finding is conclusive (upstream `ereport(ERROR)`s on it).

### 2. `btree.PageLeafItems` — slot-retaining leaf reader

`btree.PageLeafEntries` collapses a leaf line pointer to `(key, TID)` and loses
the slot and posting position, which upstream's errdetail prints
(`Index tid=(blk,off) posting N and …`). `PageLeafItems` returns the same
entries plus `Slot` and `PostingIndex` (`-1` for a plain item), and
`PageLeafEntries` is now a projection of it — one reader for the on-disk item
layout, so the ItemIDDead skip and posting expansion cannot drift apart between
the two (sibling-paths-must-agree).

`BtreeReport` gains a `Detail` field, carried through the SQL surface's
`btIndexReportDetail` into the error's DETAIL, mirroring
`errdetail_internal`. Existing tiers leave it empty.

### 3. Executor — `btIndexCheckUnique` in `operators_bt_index_check.go`

Gates on upstream's two conditions — the call passed `checkunique` true, and
`idx.Unique` (`state->indexinfo->ii_Unique`) — then supplies visibility from the
executor's own MVCC state: pin the heap block, `storage.PageGetHeapTuple`,
`mvcc.TupleVisible` against `ctx.Snap`. An unreadable heap slot reports *not
visible*: an index entry pointing into damaged heap is `verify_heapam`'s finding,
not a live duplicate.

The argument is positional (index 2 for `bt_index_check`, 3 for
`bt_index_parent_check`); goopg's parser strips named-argument labels and keeps
written order (M0097-0003), which for both `pg_amcheck` call shapes is the
declared order.

The tier runs only when the structural tiers found nothing, mirroring upstream —
`bt_entry_unique_check` is reached from inside `bt_target_page_check`, which
never runs after an earlier invariant has raised.

## goopg / upstream divergences

| upstream | goopg | why it is faithful |
|---|---|---|
| blanks `skey->scantid` so `_bt_compare` compares keys only | nothing to blank | goopg's comparator is key-only by construction — the engine has no TID tiebreak anywhere |
| skips the check when `skey->anynullkeys` | no gate ported | goopg's byte-key B-tree has no per-attribute null bitmap and never stores a NULL-keyed row (`encodeCompositeBTreeKey`'s `hasNullKey` path, design 0119-0004 §3), so no null-bearing entry reaches the walk |
| `RegisterSnapshot(GetTransactionSnapshot())` once per check | uses the statement's `ctx.Snap` | same semantics for a single-statement check; an unseeded snapshot (`Xmax == 0`) skips the tier rather than answering against nothing |
| ereports on the first duplicate | returns one finding, the SQL surface raises `XX002` | the report-and-continue model every goopg amcheck tier uses |

The comparator seam is shared with the operator-class dispatch slice: a user
opclass governs what "the same key" means for uniqueness exactly as it governs
item order.

## Gates

- `internal/amcheck`: `TestVerifyBtreeUnique_{DistinctKeysClean,
  DuplicateBothVisible, DuplicateOneVisibleClean, DuplicateAcrossLeafBoundary,
  RunResetsOnKeyChange, HonoursKeyComparator, EdgeCases}`,
  `TestPageLeafItemsMatchesLeafEntries`.
  The load-bearing pair is `DuplicateBothVisible` vs `DuplicateOneVisibleClean`:
  identical pages, different visibility oracle, opposite verdicts.
- `internal/executor`: `TestBtIndexCheck_CheckUniqueDetectsLiveDuplicate` builds
  a real unique index, rewrites one leaf entry's key in place so two live rows
  claim the same key, and asserts (a) every non-`checkunique` call still reports
  clean — proving the finding comes from this tier — (b) `checkunique` raises
  with the upstream message and a heap-TID DETAIL, and (c) an undamaged unique
  index passes with `checkunique` on.
  `TestBtIndexCheck_CheckUniqueSkipsNonUniqueIndex` pins the `ii_Unique` gate.

## Still open for `005_opclass_damage` / `--checkunique`

- Posting-list duplicates are expanded and reported with their posting index, but
  no test exercises a posting-list page (goopg deduplicates only under specific
  churn; constructing one deterministically is a separate fixture).
- `INCLUDE`/expression key columns remain outside the opclass comparator's
  contract, so a `checkunique` run on such an index falls back to byte order.
