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
- `internal/amcheck` (2026-08-11, the posting-list arm):
  `TestVerifyBtreeUnique_{PostingListBothVisible, PostingListOneVisibleClean,
  PostingThenPlainDuplicate, AdjacentPostingListsDistinctKeysClean,
  PostingListTupleFormat}` in `verify_nbtree_unique_posting_test.go`. The
  fixtures build deduplicated leaf items directly with the newly exported
  `btree.IndexFormat.PGBTPostingRaw` (the exported face of the tree's own
  `marshalPosting`, sibling of `PGBTItemRaw`), because goopg only deduplicates
  under specific write churn — driving a real tree cannot be relied on to place
  a posting list where a test needs one.
  Three properties this pins that no earlier gate reached: a duplicate INSIDE
  one line pointer (which a per-line-pointer walk cannot see at all); the two
  errdetail spellings upstream's `bt_report_duplicate` distinguishes — index tid
  printed once with `posting 0`/`posting 1` when the entries share a line
  pointer, and `posting 1` plus a second `tid=` when they do not; and the
  tuple-format case, where each expanded entry's key carries its own heap TID so
  the duplicate is visible only under the TID-blind `CompareKeyAttrs` that
  `btIndexCheckUnique` injects — the same page under the bytewise default is
  asserted to report nothing, which is what makes the comparator argument
  load-bearing rather than decorative.
  Non-vacuity was checked by mutation: collapsing `PageLeafItems`' posting
  expansion to its first TID fails all five, and neutralising the ` posting N`
  rendering fails the three that assert an errdetail.
- `internal/executor`: `TestBtIndexCheck_CheckUniqueDetectsLiveDuplicate` builds
  a real unique index, rewrites one leaf entry's key in place so two live rows
  claim the same key, and asserts (a) every non-`checkunique` call still reports
  clean — proving the finding comes from this tier — (b) `checkunique` raises
  with the upstream message and a heap-TID DETAIL, and (c) an undamaged unique
  index passes with `checkunique` on.
  `TestBtIndexCheck_CheckUniqueSkipsNonUniqueIndex` pins the `ii_Unique` gate.

- `internal/testport` (2026-08-10): `TestPort_PgAmcheck005OpclassDamage` drives
  this tier through the **real upstream `pg_amcheck --checkunique` binary**. Its
  phase 4 repoints a UNIQUE index's operator class at a comparator that declares
  the adjacent live values 768 and 769 equal and asserts pg_amcheck exits 2 with
  `index uniqueness is violated for index "bttest_unique_idx"`; its phase 3
  asserts the same command is clean on the repaired class, so the tier is proven
  to be both reached and discriminating. Reaching it at all depends on goopg
  reporting amcheck extension version ≥ 1.4 — below that `pg_amcheck.c:607-631`
  silently drops `--checkunique` and the phase would be vacuous. See
  `docs/design/0119-0006-opclass-comparator-dispatch-amcheck.md` §Verification.

## Still open for `005_opclass_damage` / `--checkunique`

- ~~Posting-list duplicates are expanded and reported with their posting index,
  but no test exercises a posting-list page.~~ **Closed 2026-08-11** by the
  `verify_nbtree_unique_posting_test.go` fixtures above. What remains is one
  layer down and belongs to the tree, not this tier: no test drives goopg's
  *deduplication* end to end into a posting list a `--checkunique` run then
  reads, so the arm is proven against the layout `marshalPosting` writes rather
  than against a posting list a live INSERT/VACUUM sequence produced.
- Expression key columns remain outside the opclass comparator's contract, so a
  `checkunique` run on such an index falls back to byte order. (`INCLUDE`
  columns were lifted by the fourth slice.)
