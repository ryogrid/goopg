# 0110-0005 — verify_heapam page-structural verification engine

Status: accepted (partial)
Milestone: M0110-0003
Date: 2026-06-14 (extended 2026-06-14, loop #52: infomask-only header invariants)

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

### Deferred (recorded, with resume points)

- HOT-chain **clog-dependent** checks — the xmin commit-status consistency across
  a link (`verify_heapam.c:768`, `:790`) and the "root of chain but heap-only"
  check (`:828`) both need per-tuple `XID_COMMITTED`/`XID_ABORTED`/
  `XID_IN_PROGRESS` status, i.e. clog. Resume with the tier-3 clog wiring.
- `check_tuple_header`'s heap-only-but-not-updated invariant — deferred until
  goopg stamps `HEAP_UPDATED` (see above).
- `check_tuple_visibility` xmin/xmax bounds + multixact (tier 3) — needs clog.
- `check_tuple_attribute` per-attribute TOAST validation (tier 3) — needs the
  relation `TupleDesc` + toast relation.
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

// VerifyHeapPage runs the page-structural checks of upstream verify_heapam
// against a single 8 KiB heap page. blkno is the page's own block number, used
// to recognise a tuple's same-page CTID successor for the HOT-chain pass.
// Returns nil for a clean page; reports are ordered first-pass then
// HOT-chain-pass, each in ascending offset order (upstream's two-loop order).
func VerifyHeapPage(p storage.Page, blkno storage.BlockNumber) ([]Report, error)
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

## Upstream references

- `postgres/contrib/amcheck/verify_heapam.c` (`verify_heapam`,
  `check_tuple_header`) — the mirrored check order and messages.
- `postgres/src/include/access/htup_details.h` — `SizeofHeapTupleHeader` (23),
  `BITMAPLEN`, infomask bits.
- `postgres/src/include/storage/bufpage.h` — `MAXALIGN`, `BLCKSZ`,
  `FirstOffsetNumber`, line-pointer (`ItemIdData`) layout.
</content>
</invoke>
