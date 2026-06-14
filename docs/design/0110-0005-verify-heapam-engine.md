# 0110-0005 — verify_heapam page-structural verification engine

Status: accepted (partial)
Milestone: M0110-0003
Date: 2026-06-14 (extended 2026-06-14, loop #52: infomask-only header invariants;
loop #55: B-tree index verification (`verify_nbtree`) page-structural tier;
loop #58: B-tree cross-page sibling-link tier;
loop #59: B-tree cross-level downlink tier (`bt_child_check`);
loop #62: heap clog-dependent HOT-chain tier (xmin commit-status checks),
completing the heap engine to logic-complete parity)

> Scope note: this doc now covers both amcheck verify engines in
> `internal/amcheck` — the heap-page checker (`verify_heapam.go`, the bulk of
> this doc) and the B-tree index checker (`verify_nbtree.go`, the
> "B-tree index verification" section near the end). Both follow the same
> engine-first/wire-later pattern and feed the same `AC-002` promotion.

## Goal

Land the reusable, fully-unit-testable **core** of amcheck's `verify_heapam()`
— the page-structural integrity checker — as a standalone `internal/amcheck`
package, decoupled from any SQL/SRF wiring. This is the keystone blocker for the
four deferred pg_amcheck TAP tests (`002_nonesuch`, `003_check`,
`004_verify_heapam`, `005_opclass_damage`, CSV row `AC-002`): all of them either
run `verify_heapam()` directly or depend on `CREATE EXTENSION amcheck` succeeding
and that function existing.

This follows the project's established **engine-first, wire-later** pattern (cf.
the data-checksum engine landed in `internal/storage/checksum.go` under
M0102-0010 before the ~50-site bootstrap sweep): the high-value, high-blast-radius
logic lands in one self-contained, exhaustively-tested slice; the plumbing that
threads it through SQL dispatch (`CREATE EXTENSION` DDL + the `verify_heapam`
set-returning function) lands in a later loop.

### Why engine-first this loop specifically

The working tree currently carries uncommitted generated-column/partition WIP
from a separate session across `internal/{parser,analyzer,catalog,executor,
planner,mvcc}` and `internal/server/dispatch.go`. Adding the SQL surface for
`CREATE EXTENSION` + an SRF would edit exactly those contaminated files and could
not be committed cleanly. The verification engine lands entirely in **new files**
under a new package, touching none of them.

## What upstream verify_heapam does

`postgres/contrib/amcheck/verify_heapam.c` checks a heap relation block by block.
Its checks form three tiers:

1. **Page-structural** (no catalog, no clog, no toast): line-pointer bounds and
   alignment, redirect-target validity, and tuple-header offset/`t_hoff`
   consistency. These are deterministic functions of the raw page bytes.
2. **HOT-chain** (`successor`/`predecessor` arrays): cross-line-pointer update
   chain consistency. Depends on goopg's HOT-bit placement convention (which
   differs from upstream — goopg stores `HEAP_HOT_UPDATED` in `t_infomask`, not
   `t_infomask2`; see `internal/storage/heap.go`).
3. **MVCC / attribute** (`check_tuple_visibility`, `check_tuple_attribute`):
   xmin/xmax bounds against clog, multixact membership, and per-attribute TOAST
   pointer validation. Needs clog, the relation's `TupleDesc`, and the toast
   relation.

## Scope of THIS slice

**Tier 1 (page-structural) only.** That is the bulk of what `002_nonesuch`
exercises in practice: the single relation it actually checks is a **clean**
catalog table (`postgres.pg_catalog.pg_class`), so a faithful structural checker
that returns *no corruption* for a well-formed page is exactly what makes
`002_nonesuch` reach exit 0 once the SQL surface is wired. Tiers 2 and 3 are
deferred (they pin to MVCC/toast subsystems and only matter for the
deliberately-corrupt fixtures of `003_check`/`004_verify_heapam`).

### Checks mirrored (exact upstream messages)

Mirrored from `verify_heapam.c` so the later SRF + `004` port reuse the strings
verbatim. For each line pointer over `[FirstOffsetNumber, maxoff]`:

- `LP_UNUSED` / `LP_DEAD` → skipped (no tuple body).
- `LP_REDIRECT`:
  - target `< FirstOffsetNumber` → `line pointer redirection to item at offset %u precedes minimum offset %u`
  - target `> maxoff` → `line pointer redirection to item at offset %u exceeds maximum offset %u`
  - target unused → `redirected line pointer points to an unused item at offset %u`
  - target dead → `redirected line pointer points to a dead item at offset %u`
  - target redirect → `redirected line pointer points to another redirected line pointer at offset %u`
- `LP_NORMAL`:
  - `lp_off != MAXALIGN(lp_off)` → `line pointer to page offset %u is not maximally aligned`
  - `lp_len < MAXALIGN(SizeofHeapTupleHeader)` (= 24) → `line pointer length %u is less than the minimum tuple header size %u`
  - `lp_off + lp_len > BLCKSZ` → `line pointer to page offset %u with length %u ends beyond maximum page offset %u`
  - `t_hoff > lp_len` → `data begins at offset %u beyond the tuple length %u`
  - `t_hoff != expected_hoff` (where `expected_hoff = MAXALIGN(SizeofHeapTupleHeader + BITMAPLEN(natts))` when `HEAP_HASNULL`, else `MAXALIGN(SizeofHeapTupleHeader)`) → the four `tuple data should begin at byte %u, but actually begins at byte %u (…)` variants keyed on `(has-nulls, natts==1)`.

`continue` semantics match upstream: a failed bounds/alignment check skips the
remaining checks for that line pointer (it would be unsafe to read the tuple).

### Infomask-only `check_tuple_header` invariants (added loop #52)

Two further `check_tuple_header` invariants are decidable from the tuple header
bytes alone — no clog, no `TupleDesc`, no toast — so they land in this engine:

- `HEAP_XMAX_COMMITTED && HEAP_XMAX_IS_MULTI` → `multixact should not be marked
  committed` (`verify_heapam.c:1015`). A multixact xmax is never hint-bit
  "committed". goopg has no multixact on disk and never sets `HEAP_XMAX_IS_MULTI`
  (0x1000, defined locally in the engine at the upstream value), so on a healthy
  goopg page this fires only on injected corruption — zero false positives.
- `HeapTupleHeaderIsHotUpdated && curr_xmax == 0` → `tuple has been HOT updated,
  but xmax is 0` (`verify_heapam.c:1029`). `curr_xmax` is the raw `t_xmax` field
  (the non-multi branch of `HeapTupleHeaderGetUpdateXid`); the multixact branch
  is skipped (it needs a member-table lookup). `HEAP_HOT_UPDATED` is read from
  **`t_infomask`** per goopg's divergent layout (see below). A healthy goopg
  HOT-updated tuple always carries a valid xmax, so this also has zero false
  positives — verified by a dedicated test.

Both follow upstream's "report but do not skip" semantics.

#### goopg vs upstream infomask layout

Upstream stores `HEAP_HOT_UPDATED`/`HEAP_ONLY_TUPLE` in `t_infomask2`; **goopg
packs them into `t_infomask`** (`storage/heap.go` `HeapHotUpdated`/
`HeapOnlyTuple` are read/written against `HeapTupleHeader.Infomask` — see
`storage/prune.go` and the `heap_update` path). Because this engine inspects
goopg's own pages, the HOT-updated check reads the flag from `t_infomask`; the
emitted message is byte-identical to upstream regardless.

#### One invariant intentionally NOT ported

`HeapTupleHeaderIsHeapOnly && (t_infomask & HEAP_UPDATED) == 0` → "tuple is heap
only, but not the result of an update" is **deferred**: goopg never sets
`HEAP_UPDATED` (0x2000; goopg reuses that bit value for `HeapKeysUpdated` in
`t_infomask2`). Porting it verbatim would false-positive on every legitimate
goopg HOT successor tuple. Resume point: port once goopg stamps `HEAP_UPDATED`
on update-produced tuples.

### Page-structural HOT-chain (update-chain) tier (added loop #53)

The HOT-chain tier (`verify_heapam.c`'s second and third loops over the
`successor`/`predecessor` arrays) splits into a page-structural subset — links
and flag agreement decidable from the page bytes plus the page's own block
number — and a clog-dependent subset (xmin commit-status across a link). The
page-structural subset lands in this engine:

- Build `successor[]` in the first pass: a redirect's target offset, or a normal
  tuple's same-page CTID successor (`t_ctid.block == blkno && offset != self &&
  in range`). goopg stores `t_ctid.block` as a plain `uint32` at byte `off+12`
  (not upstream's `bi_hi`/`bi_lo` `BlockIdData` split) and the offset at
  `off+16`; the engine therefore reads it from goopg's positions. This is why
  `VerifyHeapPage` now takes the page's `blkno` — without it a same-page CTID
  successor cannot be recognised.
- `redirected line pointer points to a non-heap-only tuple at offset N`
  (`verify_heapam.c:677`) — a redirect must target a HOT (heap-only) tuple.
- `redirect line pointer points to offset N, but offset M also points there`
  (`:686`) and `tuple points to new version at offset N, but offset M also
  points there` (`:720`) — HOT chains must not intersect; `predecessor[]`
  detects a second pointer reaching the same successor.
- `non-heap-only update produced a heap-only tuple at offset N` (`:743`) and
  `heap-only update produced a non-heap only tuple at offset N` (`:751`) — a
  link's HOT-updated flag (read as the **raw** `t_infomask` HOT bit, per goopg's
  layout and matching upstream's deliberate raw-bit use here) must agree with
  the successor's heap-only flag.

A normal→normal link is only formed when `curr_xmax == next_xmin` and
`curr_xmax != 0` (the non-multi `HeapTupleHeaderGetUpdateXid`); a multi xmax is
skipped (goopg has no on-disk multixact, so it is injected-only and its update
xid is not page-resolvable). Healthy same-page chains and cross-block CTIDs are
covered by dedicated false-positive guards.

### Relation-natts tier (added loop #54)

`verify_heapam.c`'s `check_tuple` (line 1942) rejects a visible tuple whose
stored attribute count exceeds the relation's column count
(`RelationGetDescr(rel)->natts < ctx->natts` → `number of attributes %u exceeds
maximum expected for table %u`). A tuple may legitimately carry *fewer*
attributes than the table (trailing columns added after it was written) but
never *more*. This is the one relation-dependent check that is faithful to
goopg's on-disk layout: the tuple's `natts` is read page-structurally from
`t_infomask2` (the engine already decodes it for the `t_hoff` check), and the
only relation metadata needed is one scalar — the column count — supplied via
`RelDesc.Natts`.

- Exposed through a new entry point `VerifyHeapPageWithRel(p, blkno, rel)`;
  `VerifyHeapPage` is now a thin wrapper passing a zero-value `RelDesc`, which
  disables the relation-dependent checks (page-bytes-only behaviour unchanged).
- `check_tuple_header` now returns a `bool` (header clean enough to continue),
  mirroring upstream's `result`. The natts check is gated on it exactly as
  upstream's `check_tuple` is gated on `check_tuple_header` — a `t_hoff`-overrun
  or `t_hoff`-mismatch tuple reports only the header error, not natts.
- **Visibility gate divergence:** upstream also gates the natts check on
  `check_tuple_visibility` (an aborted in-flight DDL could mean the tuple was
  built against a different `TupleDesc`). goopg has no clog for that gate, so the
  engine applies the check to every header-clean tuple. This is safe for goopg
  because a stored `natts` above the table's column count is structural
  corruption regardless of visibility, and goopg drops columns logically
  (`attisdropped`) rather than shrinking a tuple's `natts`.

The per-attribute walk (`check_tuple_attribute`) is **not** ported and is no
longer merely deferred — it is goopg-divergent: it decodes PG's on-disk varlena
1-byte/4-byte headers and `varatt_external` TOAST pointers (`va_rawsize`,
`VARTAG_ONDISK`, compression-method id), a format goopg does not use. goopg's
TOAST is a separate chunk relation (`chunk_id`/`chunk_seq`/`chunk_data`) reached
through a goopg-specific in-heap pointer datum (`internal/executor/toast.go`), so
a verbatim port would false-positive on every valid goopg toasted page. A
goopg-faithful attribute/TOAST tier would be a separate reimplementation against
goopg's own codec, not a port; recorded here as out-of-scope for the port.

### Deferred (recorded, with resume points)

- HOT-chain **clog-dependent** checks — the xmin commit-status consistency across
  a link (`verify_heapam.c:768`, `:790`) and the "root of chain but heap-only"
  check (`:828`) both need per-tuple `XID_COMMITTED`/`XID_ABORTED`/
  `XID_IN_PROGRESS` status, i.e. clog. Resume with the tier-3 clog wiring.
- `check_tuple_header`'s heap-only-but-not-updated invariant — deferred until
  goopg stamps `HEAP_UPDATED` (see above).
- `check_tuple_visibility` xmin/xmax bounds + multixact (tier 3) — needs clog.
- `check_tuple_attribute` per-attribute TOAST validation — goopg-divergent
  on-disk format (see the relation-natts tier above); a goopg-faithful version
  is a reimplementation against goopg's codec, not a verify_heapam.c port.
- The SQL surface: `CREATE EXTENSION amcheck` (parser + `pg_extension` row +
  `pg_proc` registration of `verify_heapam`/`bt_index_check`/`bt_index_parent_check`)
  and the `verify_heapam(regclass, …)` set-returning operator that walks a
  relation's blocks through this engine. This is the slice that promotes
  `AC-002` (`002_nonesuch`) and must wait for a clean working tree.

## API

```go
package amcheck

// Report is one corruption finding: the 1-based line-pointer offset and the
// upstream-matching message. blkno is supplied by the caller (per-relation walk).
type Report struct {
    Offset uint16
    Msg    string
}

// RelDesc carries the relation-level metadata for the relation-dependent
// checks. Natts is the table's column count; zero means "unknown" (skip).
type RelDesc struct {
    Natts int
}

// VerifyHeapPage runs the page-bytes-only checks of upstream verify_heapam
// against a single 8 KiB heap page. blkno is the page's own block number, used
// to recognise a tuple's same-page CTID successor for the HOT-chain pass.
// Returns nil for a clean page; reports are ordered first-pass then
// HOT-chain-pass, each in ascending offset order (upstream's two-loop order).
func VerifyHeapPage(p storage.Page, blkno storage.BlockNumber) ([]Report, error)

// VerifyHeapPageWithRel adds the relation-dependent checks (currently the
// tuple-natts-vs-table check) driven by rel. A zero-value rel makes it
// identical to VerifyHeapPage.
func VerifyHeapPageWithRel(p storage.Page, blkno storage.BlockNumber, rel RelDesc) ([]Report, error)
```

The engine takes raw `storage.Page` bytes and is therefore trivially testable
with hand-built clean and corrupt fixtures — no server, no buffer pool, no clog.

## Testing

`internal/amcheck/verify_heapam_test.go`:

- a freshly `InitPage`d empty page → no reports;
- a page built with `PageAddHeapTuple` (clean tuples, with and without null
  bitmaps) → no reports;
- targeted corruptions, each asserting the exact upstream message:
  unaligned `lp_off`; `lp_len` below the 24-byte minimum; `lp_off+lp_len` past
  `BLCKSZ`; `t_hoff` beyond `lp_len`; `t_hoff` mismatching `expected_hoff`;
  redirect out of range; redirect to unused/dead/redirect targets;
  `HEAP_XMAX_COMMITTED|HEAP_XMAX_IS_MULTI`; `HEAP_HOT_UPDATED` with `t_xmax==0`.
- false-positive guards (must report **no** corruption): a healthy HOT-updated
  tuple (HOT bit set + valid xmax), and a HOT bit set together with
  `HEAP_XMAX_INVALID` and `t_xmax==0` (IsHotUpdated is false, so skipped).
- HOT-chain tier (added loop #53): each new message asserted via a built chain —
  redirect-to-non-heap-only, redirect/normal chain intersection,
  non-heap-only↔heap-only update mismatch; plus false-positive guards for a
  healthy normal chain, a healthy redirect→heap-only chain, and a cross-block
  CTID (must not start an in-page chain).
- relation-natts tier (added loop #54): `natts > RelDesc.Natts` via
  `VerifyHeapPageWithRel` asserts the exact message; plus guards that
  fewer-or-equal natts reports nothing, that the page-bytes-only `VerifyHeapPage`
  never runs the check, and that a header-corrupt tuple suppresses it.

## B-tree index verification (`verify_nbtree`) tier (added loop #55)

`pg_amcheck` does not only run `verify_heapam()` on heap relations — for every
B-tree index it runs `bt_index_check()` (and, with `--parent-check`,
`bt_index_parent_check()`). `003_check.pl` and `005_opclass_damage.pl` both
exercise the index path, so the `AC-002` promotion needs a B-tree checker as
well as the heap checker. This tier lands its page-structural core as
`internal/amcheck/verify_nbtree.go`, the index-side companion to
`verify_heapam.go`, following the same engine-first/wire-later pattern.

The slice ports the per-page sanity checks upstream applies to **every** page it
reads, in `verify_nbtree.c:palloc_btree_page`:

- **Metapage (block 0) magic/version** — `index "%s" meta page is corrupt` when
  the magic word does not match, and `version mismatch in index "%s": file
  version %d, current version %d, minimum supported version %d` when the version
  does. goopg writes exactly one on-disk version (`btree.BTreeVersion`), so the
  version check is an equality test and "minimum supported version" equals the
  current version.
- **Page-level consistency** — a leaf page must sit at level 0
  (`invalid leaf page level %u for block %u in index "%s"`) and an internal page
  must not (`invalid internal page level 0 for block %u in index "%s"`). Fully
  deleted pages type-pun their level field and hold no items, so they are exempt
  (upstream's `!P_ISDELETED` guard; goopg's `BTPageOpaque.IsDeleted`).

Messages are byte-for-byte upstream, including the `in index "<name>"` clause —
the index name is the one piece of context not in the page bytes, so
`VerifyBtreePage` takes it as a parameter (the SQL surface supplies it from the
`regclass`).

**Layout single-source-of-truth.** Rather than re-decode the metapage and opaque
formats (which have changed across versions — v3 grew the opaque for
variable-length high keys, v4 widened that field), the engine reads through
newly exported accessors on the `btree` package: `btree.ParseMeta`,
`btree.ParseOpaque`, `btree.BTreeMagic`, `btree.BTreeVersion`, and the
`BTPageOpaque.IsDeleted` method. A duplicated decoder would be a classic
sibling-path drift hazard (`pattern_sibling_paths_must_agree`).

### goopg vs upstream PG divergences (not ported)

- **High-key placement.** Upstream stores a page's high key as line-pointer item
  `P_HIKEY` (offset 1) and derives `P_FIRSTDATAKEY` from it; goopg keeps the high
  key in the opaque special area (`BTPageOpaque.HighKey`). The upstream item-count
  checks phrased in terms of `P_HIKEY`/`P_FIRSTDATAKEY` ("internal block lacks
  high key and/or at least one downlink", "non-rightmost leaf block lacks high
  key item") therefore do **not** translate and are not ported.
- **`MaxIndexTuplesPerPage` ceiling.** Upstream's item-count upper bound is a
  constant from PG's `IndexTupleData` size; goopg's index-tuple layout differs
  (inline key with a 2-byte length), so the ceiling is computed from goopg's own
  per-item footprint — see the item-count tier below. (Was deferred through loop
  #56; ported loop #57.)

### Deferred (B-tree, with resume points)

- Cross-page tiers (`bt_check_level_from_leftmost`, downlink/sibling-link
  agreement, cross-page item order, root-descent via `bt_index_parent_check`) —
  need multi-page traversal state.
- The SQL surface: `bt_index_check` / `bt_index_parent_check` registration,
  shared with the `verify_heapam` SRF wiring above; waits for a clean tree.

### Testing (`verify_nbtree_test.go`)

Hand-built clean and corrupt pages (no server/buffer pool), each self-checked
through `btree.ParseMeta`/`btree.ParseOpaque` so a future layout change fails the
fixture loudly: clean metapage → no reports; bad magic and bad version each
assert the exact upstream message; magic masks version (first conclusive problem
wins); clean leaf-at-0 and internal-at-2; bad leaf level and internal-level-0
each assert the exact message and block number; a deleted page suppresses the
level check; a root+leaf single-page tree is clean. 10 tests.

## B-tree item-order / high-key tier (`VerifyBtreeItemOrder`, added loop #56)

The second `verify_nbtree` tier ports the two **page-local** key invariants from
upstream `bt_target_page_check` (`verify_nbtree.c:1565-1642`) — the checks that
need only a page's own bytes plus its high key, no sibling traversal or
cross-level descent:

- **High-key invariant.** On a non-rightmost page every item key must respect the
  page's high key — `<=` on a leaf, strictly `<` on an internal page. Upstream
  weakens the leaf check to `<=` because suffix truncation can leave a leaf high
  key that is an untruncated copy of the last data item; an internal high key is
  "just another separator", unique on its level. Message:
  `high key invariant violated for index "%s"`.
- **Item-order invariant.** Items must be stored in strictly ascending key order:
  each key strictly less than the next. Message:
  `item order invariant violated for index "%s"`.

goopg specifics making the port faithful:

- The high key lives in the opaque special area (`BTPageOpaque.HighKey`), never a
  line-pointer item, so there is no `P_HIKEY` slot to skip; rightmost /
  has-high-key gating matches the engine's own `keyExceedsHighKey`
  (`Next == InvalidBlockNumber` ⇒ rightmost).
- An internal page's leftmost negative-infinity downlink has an **empty** key
  (see `findChildBlock`); empty compares strictly less than any real separator
  and any high key, so it satisfies both invariants without a special case —
  exactly as upstream's zero-attribute negative-infinity tuple does.
- Keys are decoded through the new `btree.PageItemKeys` (one separator key per
  physical line pointer, **collapsing** a posting-list item's many TIDs to its
  single shared key) and compared with `btree.CompareKeys` — the same
  order-preserving comparator the live index uses. The comparison is over stored
  separators, not expanded `(key, TID)` pairs. `PageItemKeys` is exported so the
  engine never re-implements the inline `2-byte-len | TID | key` item layout (a
  v3→v4 drift hazard), the same single-source-of-truth discipline as
  `ParseMeta`/`ParseOpaque`.

Like `VerifyBtreePage`, `VerifyBtreeItemOrder` returns 0 or 1 findings (upstream
`ereport(ERROR)`s on the first violation) and never a Go error; an undecodable
page surfaces as a finding. The metapage and deleted pages hold no orderable
items and yield nil.

### Testing (item-order tier)

`verify_nbtree_test.go` gains a `makeItemsPage` builder (sets `pd_special`/
`pd_upper` to the B-tree special offset before adding items so item data grows
above the opaque area, then writes the opaque bytes; self-checks the decoded
opaque + key sequence through `btree.ParseOpaque`/`btree.PageItemKeys`):
ascending leaf clean; out-of-order and duplicate-adjacent keys each assert the
item-order message + block; leaf `key == high key` clean (`<=`) vs leaf
`key > high key` violation; internal `key == high key` violation (`<`) vs
internal negative-infinity clean; rightmost page ignores a lingering high key;
metapage and deleted pages yield nil. 10 tests. `btree/posting_test.go` gains
`TestPageItemKeys` asserting the posting-collapse behaviour (regular + 3-TID
posting → 2 keys).

## B-tree item-count ceiling tier (`VerifyBtreePage`, added loop #57)

The third `verify_nbtree` tier ports `palloc_btree_page`'s item-count upper bound
(`verify_nbtree.c:3396-3402`): a page whose line-pointer count exceeds the number
of index tuples that can physically fit is corrupt. Upstream phrases the bound as
the constant `MaxIndexTuplesPerPage`
(`(BLCKSZ - SizeOfPageHeaderData) / (MAXALIGN(sizeof(IndexTupleData)+1) + sizeof(ItemIdData))`,
`postgres/src/include/access/itup.h`). The check is folded into `VerifyBtreePage`
(the `palloc_btree_page` tier) after the leaf/internal level checks, matching the
upstream order. Message is upstream-verbatim:
`Number of items on block %u of index "%s" exceeds MaxIndexTuplesPerPage (%u)`.

goopg specifics making the port faithful:

- **The bound is goopg's, not PG's.** goopg's index tuple is `keyLen(2) |
  block(4) | offset(2) | key` stored **unaligned** (the writer's `pageHasSpaceFor`
  reserves exactly `itemIDSize + itemPrefixSize + len(key)`), where the smallest
  possible body is a zero-length-key negative-infinity downlink. The ceiling is
  therefore `(BlockSize - SizeOfPageHeaderData) / (4 + itemPrefixSize)` =
  `8168 / 12` = **680**, exported as `btree.MaxItemsPerPage`. It lives in
  `btree.go` beside `itemPrefixSize` so the tuple-size accounting has a single
  source of truth — the engine never re-derives the inline item layout (the same
  v3→v4 drift discipline as `ParseMeta`/`ParseOpaque`/`PageItemKeys`). Like
  upstream the bound deliberately ignores the per-page special (opaque) area, so
  the real maximum is a little lower; that headroom keeps the check free of false
  positives.
- **Deleted-page divergence.** Upstream applies the count check to deleted pages
  too (it sits outside `palloc_btree_page`'s `!P_ISDELETED` guard); goopg returns
  for deleted pages before the count check. A goopg deleted page holds no live
  items so the check is moot there, and skipping it avoids reading a deleted
  page's type-punned fields. Documented in-code.
- A corrupt `pd_lower` whose line-pointer area is not an `itemIDSize` multiple
  surfaces as a damaged-page finding (`PageLinePointerCount` errors), never a Go
  error or panic — matching the report-and-continue model.

### Testing (item-count tier)

`verify_nbtree_test.go` gains a `makeCountPage` builder that bumps `pd_lower` to
claim an arbitrary line-pointer count without materialising item bodies (a count
above the ceiling cannot physically fit, so a corrupt `pd_lower` is the only way
the corruption arises). `TestBtreeMaxItemsPerPageValue` pins the derived constant
(680) so a layout change trips it; a page exactly at the ceiling is clean; one
item over asserts the exact upstream message + block; a non-multiple `pd_lower`
yields a damaged-page finding; a deleted page with an over-ceiling `pd_lower` is
suppressed. 5 tests.

## B-tree cross-page sibling-link tier (`VerifyBtreeLevelSiblingLinks`)

The per-page tiers (`VerifyBtreePage`, `VerifyBtreeItemOrder`) inspect one page's
bytes in isolation. The next faithful slice is the first **cross-page** tier: the
checks upstream amcheck performs while walking one B-tree level left-to-right in
`bt_check_level_from_leftmost` (`verify_nbtree.c:650-790`). It needs no clog, no
index `TupleDesc`, and no parent/downlink descent — only the sibling-link state
(`btpo_prev` / `btpo_next`) of the pages on a single level — so it ports cleanly
onto goopg's `BTPageOpaque.Prev` / `BTPageOpaque.Next`.

### Relation-walking dependency (`PageSource`)

To stay new-file/additive while the working tree carries another session's
uncommitted WIP, the driver does **not** open the index catalog itself. It takes
a `PageSource` — `func(storage.BlockNumber) (storage.Page, error)` — the minimal
seam over "read block N of this index". The SQL surface (a later loop) satisfies
it from the index's smgr; tests back it with an in-memory `map`. A source error
(out-of-range block, short read) becomes a damaged-page finding, never a panic,
matching the per-page tiers' report-and-continue model.

### Ported checks (each upstream-verbatim message)

- **Sibling-link agreement.** Following `btpo_next` from `leftmost`, each page's
  `btpo_prev` must equal the block we arrived from. Upstream gates this on
  `leftcurrent != P_NONE`, so the leftmost page is exempt (its left link may
  legitimately differ when the left sibling is half-dead). Message: `left
  link/right link pair in index "%s" not in agreement` (`verify_nbtree.c:1193`).
- **Per-level uniformity.** Every page reached on the horizontal walk must report
  the same `btpo_level` as the leftmost page. Message: `leftmost down link for
  level points to block in index "%s" whose level is not one level down`
  (`verify_nbtree.c:774`).
- **Circular link chain.** A corrupt index can form a sibling cycle. Upstream
  catches the immediate case (`current == leftcurrent || current == btpo_prev`);
  goopg tracks every block visited on the walk and flags a revisit, which
  subsumes the immediate case (a self-loop or back-link revisits within one step)
  **and** bounds the walk against longer cycles a bytes-only checker cannot
  otherwise terminate. Message: `circular link chain found in block %u of index
  "%s"` (`verify_nbtree.c:787`).

### Divergences

- `P_NONE` is `storage.InvalidBlockNumber`; a rightmost page (`btpo_next ==
  P_NONE`) ends the walk, exactly as `P_RIGHTMOST`.
- Reaching a fully deleted page through a sibling link is itself corruption in
  readonly mode — upstream ereports `downlink or sibling link points to deleted
  block in index "%s"` (`verify_nbtree.c:676`); goopg mirrors that (a deleted
  page is unlinked and must not be reachable).
- A `leftmost` of the metapage is a damaged starting point (the metapage carries
  no sibling links) and surfaces as a damaged-page finding.
- Like the per-page tiers it returns 0 or 1 findings (upstream ereports on the
  first violation). It performs **only** the cross-page checks; per-page
  structure and key order are run by the per-page tiers and composed by the SQL
  surface.

### Testing (sibling-link tier)

`verify_nbtree_test.go` gains a `makeLinkedPage` builder (explicit `prev`/`next`
links) and a `mapSource` adapter (block→page map → `PageSource`). 10 tests: a
clean three-page level; a back-link mismatch (exact message + block); the
leftmost-prev exemption; a level mismatch; a two-page cycle and a self-loop
(both → circular message); a deleted page reached via sibling link; a dangling
right link (→ damaged-page finding); a metapage leftmost; and the single-page
new-tree level.

## B-tree cross-level downlink tier (`VerifyBtreeParentDownlinks`)

The next faithful slice descends one level: given an internal `parentBlk`, it
follows every downlink to its child and applies the per-downlink checks of
upstream's `bt_child_check` (verify_nbtree.c:2393-2543), reading children through
the same `PageSource` seam as the sibling-link tier. A new exported accessor,
`btree.PageDownlinks`, decodes an internal page's `(separator key, child block)`
entries through the canonical on-disk reader (single source of truth, like
`PageItemKeys`), so the engine never re-derives the inline item layout.

Three invariants are checked per downlink — none needs clog, the index
`TupleDesc`, or sibling traversal:

- **Downlink-to-deleted.** A child marked `BTDeleted` is unlinked and cannot be a
  legitimate downlink target in readonly mode →
  `downlink to deleted page found in index "%s"` (verify_nbtree.c:2494).
- **Child level one down.** An internal parent at `btpo_level` *L* must point
  only to children at level *L−1* →
  `downlink points to block in index "%s" whose level is not one level down`
  (verify_nbtree.c:2655).
- **Down-link lower bound.** The parent's separator key *K_i* must bound every
  key in child *C_i* from below (`bt_child_check`'s
  `invariant_l_nontarget_offset` loop, verify_nbtree.c:2500-2540) →
  `down-link lower bound invariant violated for index "%s"`
  (verify_nbtree.c:2535).

**goopg / upstream divergences that keep the port false-positive-free:**

- **Inclusive vs strict lower bound.** Upstream (heapkeyspace) requires the
  downlink key *strictly* less than each non-negative-infinity child key, because
  nbtree suffix-truncates separators and tie-breaks on heap TID. goopg routes a
  search to the rightmost internal item whose key `<=` the search key
  (`findChildBlock`), so child *C_i* covers the half-open range `[K_i, K_{i+1})`;
  *K_i* is an **inclusive** lower bound and the faithful goopg test is
  `CompareKeys(childKey, K_i) >= 0`. Upstream's strict test would misfire on
  every separator that equals its child's first key.
- **Negative-infinity skip.** Upstream skips the child's negative-infinity item
  (`offset_is_negative_infinity`: the first data item of an internal page).
  goopg stores that item with the empty key; an empty key sorts below any real
  *K_i* and would falsely trip the bound, so the first item of an *internal*
  child is skipped. A leaf child has no negative-infinity item and all its keys
  are checked.

Like the other tiers it returns 0 or 1 findings (upstream `ereport(ERROR)`s on
the first violation). A leaf or deleted `parentBlk` and the metapage have no
downlinks to descend → nil. Per-page structure (`VerifyBtreePage`) and key order
(`VerifyBtreeItemOrder`) run separately; the SQL surface composes all four
B-tree tiers.

### Deferred (needs a heap scan / TupleDesc)

The remaining `bt_index_check` tier — `heapallindexed` (bloom-filter
fingerprinting that cross-checks the index against a fresh heap scan) — needs the
heap relation and the index `TupleDesc`, so it is deferred to the SQL surface,
which supplies the catalog/regclass lookup.

### Testing (cross-level tier)

`verify_nbtree_test.go` gains a `makeInternalPage` builder (`(key, child)`
downlinks + `btDownlinkRaw`) reusing `mapSource`. 10 tests: a clean two-downlink
parent; a leaf-child key below the separator (lower-bound message + child block);
a downlink to a deleted child; a child whose level is not one down; an internal
child whose negative-infinity item is correctly skipped; an internal child with a
*real* key below the bound (skip applies only to item 0); a leaf parent and the
metapage (both nil); a damaged parent; and a dangling child downlink (both →
damaged-page finding).

## Heap clog-dependent HOT-chain tier (2026-06-14)

The heap engine's last update-chain tier — the three checks that need to know
each tuple's xmin commit status — is now ported, completing the heap side to
logic-complete parity with the B-tree side. These mirror `verify_heapam.c`'s
second and third update-chain loops (`verify_heapam.c:759-833`):

1. **in-progress xmin → committed xmin** (`:759`): a chain root whose xmin is
   still in progress cannot have produced a successor whose xmin has committed.
2. **aborted xmin → in-progress/committed xmin** (`:791`/`:797`): a chain root
   whose xmin aborted cannot have produced a live successor.
3. **root of chain but heap-only** (`:831`, the separate third loop): a tuple
   with a committed/in-progress xmin and no predecessor must not be heap-only —
   a chain starts with a normal tuple or a redirect, never a heap-only tuple.

### Decoupling seam: `XidStatusFunc`

Upstream fills a per-offset `xmin_commit_status[]` / `xmin_commit_status_ok[]`
array from `get_xid_status` (a clog + proc-array lookup). To keep the engine free
of the contaminated `internal/mvcc` package and fully unit-testable, the port
injects the status as a callback, `XidStatusFunc func(xid uint32) XidCommitStatus`,
threaded through the new entry point `VerifyHeapPageWithXminStatus(p, blkno, rel,
xidStatus)`. `XidCommitStatus` is the branch-relevant subset of upstream's enum:
`Unknown` (= `xmin_commit_status_ok == false`; gates the check off), `Committed`,
`InProgress`, `Aborted`, and `Current` (upstream `XID_IS_CURRENT_XID`, kept
distinct so a current-transaction xmin trips neither the in-progress nor the
root-of-chain check). The bootstrap (1) and frozen (2, or the
`HEAP_XMIN_COMMITTED | HEAP_XMIN_INVALID` hint pair) xids resolve to committed
without consulting the callback, mirroring `get_xid_status`'s special-casing.

The page-bytes-only entry points (`VerifyHeapPage`, `VerifyHeapPageWithRel`)
pass a nil callback, which leaves `xminStatusOK` false for every tuple and
disables exactly these three checks — their output is byte-for-byte unchanged
(regression-guarded by `TestVerifyHeapPage_NilXminStatusDisablesClogChecks`).

The reported offset and xmins are verbatim from upstream: the message names the
**current** tuple's offset and the **frozen-resolved** xmins of both tuples.

Still deferred (the MVCC/attribute tier): xmin/xmax numeric bounds against the
cluster's xid range, multixact member validation, and TOAST-pointer validation
(goopg's TOAST is a separate chunk relation, so `check_tuple_attribute` is
goopg-divergent, not merely deferred). The SQL surface — `CREATE EXTENSION
amcheck` + the `verify_heapam` SRF that supplies `RelDesc` and a clog-backed
`XidStatusFunc` — remains blocked on a clean tree (a separate live session holds
uncommitted gen-column WIP across parser/planner/executor/catalog).

### Testing (clog tier)

`verify_heapam_test.go` gains a `mapXidStatus` builder (map-backed
`XidStatusFunc`, missing xid → `Unknown`) and 10 tests: the three positive
corruption cases (in-progress→committed, aborted→committed, aborted→in-progress,
each isolated so only the clog report appears); the heap-only-root positive; and
false-positive guards — a heap-only tuple with a predecessor (legit chain tail),
an aborted heap-only root (gated off), a current-xid root, an unknown-status
root, a nil callback (proves page-bytes-only callers unchanged), and a frozen
xmin that resolves to committed without the callback being consulted.

## Relation-walking driver (`verify_heapam_relation.go`)

The per-page entry points (`VerifyHeapPage*`) check one page; the SRF must walk a
whole relation. `VerifyHeapRelation(src PageSource, nblocks, opts)` ports the
**outer loop** of the `verify_heapam()` SRF body
(`verify_heapam.c:367-405,480-501`): the empty-relation early exit
(`nblocks == 0` → no rows), block-range resolution from the `startblock` /
`endblock` SRF args (`*int64`, nil = SQL NULL → 0 / nblocks-1) with the
upstream-worded `ERRCODE_INVALID_PARAMETER_VALUE` range errors
(`starting/ending block number must be between 0 and N`), and the block
iteration that runs each page through `verifyHeapPage` and tags every finding
with its block number (`HeapRelReport{Blkno, Offset, Msg}`, mapping 1:1 to the
SRF's `(blkno, offnum, msg)` output rows; `attnum` is always -1 for these
structural checks and is supplied by the SRF).

This reuses the same `PageSource func(BlockNumber)(Page,error)` seam the B-tree
relation walkers already take, making the heap and index sides symmetric: the
SRF fills the seam from the buffer manager, tests from a map. Keeping the block
loop in the engine (not in the eventual `verify_heapam` executor op) reduces SQL
slice **S3** (docs/design/0110-0008) to a thin adapter — fill a `PageSource`,
pass `nblocks` + `RelDesc.Natts` + a clog-backed `XidStatusFunc`, stream the
returned rows — and lets the block-iteration logic be unit-tested without an
execution context.

Deliberately **out of scope** here, matching where upstream draws the line: the
relkind / relam guard (`verify_heapam.c:333-349`) and relation open/lock are
catalog- and goopg-storage-coupled, so they stay in the SRF executor (S3); the
caller invokes `VerifyHeapRelation` only on a heap relation. The toast walk
(`check_toast`, goopg-divergent) and the read-stream skip-pages optimisation are
buffer-manager concerns, also left to the wire layer.

### Testing (relation tier)

`verify_heapam_relation_test.go` adds a `heapMapSource` builder (map-backed
`PageSource`) and 9 tests: clean multi-block relation (asserts every in-range
block is actually read), empty-relation early exit (source never touched),
finding tagged with its non-zero block, ordered findings across blocks,
`startblock`/`endblock` sub-range restriction, the two range-validation error
messages (plus the negative-value case), a surfaced read error, nil-source
rejection, and the `RelDesc` option threading through to the per-page natts
check (off without `Rel`, on with it — which by the same seam covers
`XidStatusFunc` forwarding).

## Upstream references

- `postgres/contrib/amcheck/verify_nbtree.c` (`palloc_btree_page`,
  `bt_check_level_from_leftmost`, `bt_recheck_sibling_links`, `bt_child_check`,
  `invariant_l_nontarget_offset`) — the mirrored per-page, cross-page
  sibling-link, and cross-level downlink checks and messages.
- `postgres/contrib/amcheck/verify_heapam.c` (`verify_heapam`,
  `check_tuple_header`) — the mirrored check order and messages.
- `postgres/src/include/access/htup_details.h` — `SizeofHeapTupleHeader` (23),
  `BITMAPLEN`, infomask bits.
- `postgres/src/include/storage/bufpage.h` — `MAXALIGN`, `BLCKSZ`,
  `FirstOffsetNumber`, line-pointer (`ItemIdData`) layout.
</content>
</invoke>
