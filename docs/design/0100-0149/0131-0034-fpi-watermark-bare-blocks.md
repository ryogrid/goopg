# 0131-0034 — image-less blocks must not advance the FPI watermark (M0131-S26b)

Status: **implemented** (2026-08-12).
Predecessor: [0131-0033](0131-0033-pdlsn-completeness-audit.md) — this defect is
what that slice's hook audit found on the second axis.

## The invariant the watermark encodes

`Slot.nativeImageLSN` is goopg's "this page has a full-page image in WAL at or
after LSN X" claim, and `Pool.needsImage` is its only consumer:

```go
func (p *Pool) needsImage(s *Slot) bool {
    return s.nativeImageLSN.Load() <= p.redoRecPtr.Load()
}
```

A page whose watermark sits above the published redo pointer is considered
already imaged for this checkpoint epoch, so `maybeEmitFPI` emits nothing on its
next mutation. That is the whole first-touch-FPI mechanism: torn-write
protection for every page a record describes *incrementally*, because an
incremental description presupposes that the pre-image on disk is intact.

`MarkDirtyWithLSNLocked` stamps `pd_lsn` **and** advances the watermark. Its doc
comment states the premise plainly: "Callers of the WithLSN variants log their
own image-bearing multi-page records … so the LSN also advances the native-image
watermark."

## The defect

The premise holds per *record*, not per *block*. A PG-format record registers
each block in one of three ways, and only two of them justify the watermark:

| registration | redo reconstructs the page from | watermark may advance? |
|---|---|---|
| full-page image (`Image`) | the record | yes — the record *is* the image |
| `WILL_INIT` + full block data | the record | yes — rebuilt wholesale, the on-disk copy is irrelevant |
| bare reference (no image, no data) | **the page it finds on disk** | **no** — a torn page is unrecoverable |

goopg's btree paths stamped every block of a multi-page record with
`MarkDirtyWithLSNLocked`, including blocks of the third kind. Each such stamp
silently cancelled a first-touch FPI the page still owed:

| record | bare block | primary's mutation | site |
|---|---|---|---|
| `xl_btree_split` | 2 — old right sibling | `btpo_prev` relinked to the new right page | `internal/access/btree/btree.go` `splitPage` |
| `xl_btree_split` | 3 — incomplete-split child | `BTP_INCOMPLETE_SPLIT` cleared | same |
| `xl_btree_split` | 0 — left page, **incremental form only** | rewritten as a cut of the pre-split page | same |
| `xl_btree_mark_page_halfdead` | 1 — subtree parent | downlink retargeted and deleted | `btree_vacuum.go` |
| `xl_btree_unlink_page` | 1, 2 — the two live siblings | sibling links spliced across the deleted page | `btree_vacuum.go` |

Upstream registers all of these as ordinary buffers (`REGBUF_STANDARD`), which
means `XLogRecordAssemble` attaches an FPI whenever the page has not been
modified since the checkpoint. The unlink comment in `btree_vacuum.go` even said
outright that it "skips the per-epoch FPI path" — a described behaviour, not an
oversight, but the description was wrong about being safe.

Exposure is torn-write-only. That is not a reason to leave it: full-page writes
exist for exactly that failure, and the whole point of M0131 is that a *real PG*
may be the one replaying this stream.

## The fix

Use `MarkDirtyCoveredByRecordLocked` (added by S26 for the cross-page heap
UPDATE) at each bare-block site. It does what upstream's ordinary registration
does, in goopg's ordering: emit the first-touch image if one is owed, raise
`pd_lsn` to the record (never lower it — `maybeEmitFPI` may have just stamped a
larger LSN), and leave the watermark to `maybeEmitFPI`.

The image is a *post*-mutation image, as every goopg FPI is: `markDirtyCore`
also images after the caller has written the page. Its LSN is therefore above
the change record's, and redo applies the change and then restores an identical
page — idempotent in the same way the plain `MarkDirty` path already is.

### The left page needs the encoder's answer, not a guess

Block 0 of a split is an image *or* an incremental description, decided inside
`EncodeBtreeSplitPG` by whether `DescribeSplitLeft` can express the split and
`CheckSplitLeft` can reproduce the written page. The primary must pick its stamp
by the same answer, and re-deriving the condition at the call site would be a
sibling-path split waiting to drift (`pattern_sibling_paths_must_agree`).

Both askers now call one predicate — `btree.SplitLeftIsIncremental` — which
returns the description and whether it is usable. The encoder uses the
description to build the block data; `splitPage` uses only the boolean.

Blocks that keep `MarkDirtyWithLSNLocked`: split's right page (`WILL_INIT` +
full item list), new-root's root and metapage (both `WILL_INIT`), unlink's
target and leaf (`WILL_INIT`), half-dead's leaf (`WILL_INIT`).

## Tests

`TestSplitBareBlocksKeepFirstTouchFPI`
(`internal/access/btree/pgsplit_fpi_test.go`) builds a multi-leaf tree, publishes
a redo pointer above every page's watermark (the post-checkpoint state), then
drives one split of an interior leaf and asserts the sibling block took a
first-touch image. Reverting the sibling call site to `MarkDirtyWithLSNLocked`
fails it with `sibling block 2 took no first-touch FPI across the split`. It
also asserts the left page was imaged whenever the split was logged
incrementally, which pins the encoder/primary agreement.

## WAL volume

Measured with `analysis/s26b-walvolume.sh` (deterministic scattered-key load of
40k rows into an indexed table plus an UPDATE pass; `pg_waldump --stats` from the
PG 18.3 oracle over the resulting stream, since goopg has no
`pg_current_wal_lsn`):

| arm | total WAL bytes | FPI bytes |
|---|---|---|
| HEAD (29aa7cf1) run 1 | 9,337,546 | 2,582,768 |
| S26b run 1 | 9,280,089 | 2,525,108 |
| HEAD run 2 | 9,344,350 | 2,589,508 |
| S26b run 2 | 9,342,632 | 2,588,008 |

The arms are indistinguishable (run-to-run spread on the same binary, ±0.6%,
exceeds the between-arm difference, and the sign flips between pairs). The cost
is near zero because a page reached as a split sibling or an unlink neighbour is
almost always a page this backend also mutates directly within the same epoch —
it had already paid its first-touch FPI through `markDirtyCore`. What the change
buys is the case where it had *not*.

## Gates

`go test ./internal/access/btree/ ./internal/wal/ ./internal/storage/`,
`RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh`,
`scripts/tpch-spotcheck.sh` (Q12=2 / Q13=35 PASS), and the pgbench smoke via the
commit hook.
