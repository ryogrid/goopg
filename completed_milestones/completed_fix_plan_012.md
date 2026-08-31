# Completed Fix-Plan Milestones — Archive 012

Completed (`[x]`) milestones moved out of `.ralph/fix_plan.md` on 2026-09-01
to keep the live plan small. Open work and standing rules remain in `.ralph/fix_plan.md`.
Full history is in git.

## M0130 — Cluster-directory compat with PG 18.3 + PG physical replication (filed 2026-08-09)

**Milestone doc:** `docs/milestones/0130-cluster-dir-compat-and-pg-physical-replication.md`
**Implementation plan:** `docs/design/0130-cluster-dir-compat-and-pg-physical-replication.md`
**Source:** `analysis/cluster-dir-level-compat/README.md` (15-gap catalog, 2026-07-26); deferral-ledger rows #27, #29, #50, #389–#393, #404; B5 feasibility row 2026-07-18.

**Filing rule (inherited from M0129):** no task deferred without a ledger-recorded strong reason; subtasks inline in fix_plan; every non-trivial subsystem lands its design doc (draft → accepted) within M0130.

Theme A — Cluster directory format compatibility:
- [x] **M0130-S1 — Per-relation FSM/VM fork files** (est ~2 loops). Replace aggregate `pg_fsm_state.bin`/`pg_vm_state.bin` with per-relation `_fsm`/`_vm` fork files in PG FSM/VM binary layout. Design: `docs/design/0130-0001-fsm-vm-per-relation-fork-files.md`. **DONE (2026-08-09).** Core implementation: `fsm_fork.go`/`vm_fork.go` with PG-compatible binary format; old aggregate `Save`/`Load`/`FSMStatePath`/`VMStatePath` and `fsmFileMagic`/`vmFileMagic` constants removed. Design doc accepted. BASE_BACKUP includes `_fsm`/`_vm` implicitly via `filepath.Walk`.
- [x] **M0130-S2 — pg_class heap persistence** (est ~2 loops). Heap-backed pg_class with bootstrap rows, runtime sync audit, reload pass, and reverse-start (goopg from PG-created 1259 heap). Design: `docs/design/0130-0002-pg-class-heap-persistence.md`.
- [x] **M0130-S3 — Catalog heap sync for remaining DDL** (est ~2 loops). ADD COLUMN → pg_attribute sync; CREATE SCHEMA → pg_namespace; pg_collation/FDW/server heap rows. Design: `docs/design/0130-0003-catalog-heap-sync-coverage.md`.

Theme B — WAL fidelity verification (B5 landed 2026-07-18/19; these tasks audit + add gates):
- [x] **M0130-S4 — B5 verification: index/attrdef native WAL kinds** (est ~1 loop). Kinds 20/21/94/69 already retired at HEAD (commit `eb88b8a2`); grep-verify zero emit sites remain; add regression gate. Design: `docs/design/0130-0004-b5-index-and-attrdef-retirement.md`. **DONE (2026-08-09).**
- [x] **M0130-S5 — B5 verification: view/matview native WAL kinds** (est ~1 loop). Kinds 102/103 already retired at HEAD (commit `2697504f`); grep-verify zero emit sites; confirm standby DDL replay. Design: `docs/design/0130-0005-b5-view-matview-retirement.md`. **DONE (2026-08-09).**
- [x] **M0130-S6 — Verify zero rmid-128 emitted; document keep-classify-arms** (est ~1 loop). Zero goopg-catalog WAL records emitted at HEAD; `RmgrGoopgCatalog` constant deliberately kept for non-catalog kinds (ledger #415). Add regression gate + document decision. Design: `docs/design/0130-0006-rmgr-goopg-catalog-retirement.md`. **DONE (2026-08-09).**
- [x] **M0130-S7 — WAL fidelity audit: xl_prev 0-based + atomic heap-update** (est ~1 loop). xl_prev already fixed at HEAD (`writer.go` −1 conversion, ledger #29); audit atomic heap-update completeness (ledger #27). Design: `docs/design/0130-0007-wal-record-fidelity-xlprev-atomic-update.md`. **DONE (2026-08-09).** S7.1: confirmed xl_prev 0-based fix at HEAD; added `TestCrossSegmentXLPrevChain` cross-segment regression gate (PASS). S7.2: traced all three heap-update WAL paths — PG-canonical `updateHeapRowCanonicalPG` IS atomic (`EncodeHeapUpdatePG`, single record); general `updateOp.Next()` non-HOT fallback uses two separate records (`HeapDelete`+`HeapInsert`, known ledger gap M0118-0129); native `EncodeHeapUpdate` is legacy/test-only (zero production call sites). Design doc status draft→accepted.

Theme C — Physical replication PG compatibility:
- [x] **M0130-S8 — Multi-timeline START_REPLICATION + timeline reconciliation** (est ~2 loops). TLI source-of-truth = pg_control; IDENTIFY_SYSTEM/START_REPLICATION TIMELINE n; promotion TLI bump. **DONE (2026-08-09).** S8.1: LoadOrCreateTimelineID reads pg_control first (CRC-validated), falls back to flat file. S8.2: IDENTIFY_SYSTEM returns real TLI. S8.3: multi-timeline START_REPLICATION accepted (n ≤ current). S8.3x: FormatSegmentNameTLI + TLI-tolerant reader (openSegmentFile, readStreamFrom). S8.5: finalizePromotion updates pg_control ThisTimeLineID/PrevTimeLineID. Design: `docs/design/0130-0008-multi-timeline-streaming-and-timeline-reconciliation.md` (draft→accepted).
- [x] **M0130-S9 — recovery.signal archive recovery** (est ~2 loops). recovery.signal mode; restore_command GUC; segment fetch → replay → promote. Design: `docs/design/0130-0009-recovery-signal-archive-recovery.md`. **Build-break repair (2026-08-10):** the S9 commit `2da52113` shipped the tracked caller (`cmd/goopg/main.go` → `wal.RunArchiveRecovery`) but left the implementation files untracked, so a clean checkout of HEAD did **not** compile (`undefined: wal.RunArchiveRecovery`). `internal/wal/archive_recovery.go`, `archive_restore.go`, `archive_recovery_test.go` are now committed, plus a `TestRunArchiveRecoveryFetchesThenStops` end-to-end gate over the fetch/terminate loop.
- [x] **M0130-S10 — PG 18.3 standby E2E harness** (est ~2 loops). pg_basebackup → pg_ctl start → stream → failover → reverse attach; replcluster PG instance management. Design: `docs/design/0130-0010-pg183-standby-e2e-harness.md`. **DONE (2026-08-09).** `TestE2E_PGStandbyFullCycle` four-phase test: goopg primary → pg_basebackup → PG standby → DDL/DML replay → promote → reverse-attach goopg standby on new timeline. Design accepted.

Theme D — nbtree PG-identical on-disk format (filed 2026-08-10, from S10 blocker #12):

**Why a new theme:** S10's `TestE2E_PGStandbyFullCycle` (AI-20260810-011258-003)
is blocked on two chained blockers. #10 (`pg_class.relhasindex` hardcoded false)
cannot be flipped until #12 is done: goopg's user B-tree files use a private page
format (272-byte special area) that upstream `_bt_checkpage` rejects with XX002
`contains corrupted page at block 0`. Measured — flipping #10 alone makes the E2E
fail EARLIER, in Phase B. #12 is milestone-sized, so it is decomposed here rather
than worked inside an M-NIGHTLY triage loop.
**Design:** `docs/design/0130-0011-nbtree-pg-on-disk-format.md` (draft).
**Caution:** S11.2–S11.4 break the on-disk format of every existing goopg index;
there is no in-place upgrade path (REINDEX). Say so in those commit messages.

- [x] **M0130-S11.1 — PG nbtree format layer** (est ~1 loop). **DONE (2026-08-10).**
      `internal/access/btree/pgformat.go`: upstream 16-byte `BTPageOpaqueData`
      and 48-byte `BTMetaPageData` codecs, the `BTP_*` flag set, `P_NONE`,
      `InitPGBTPage` (`_bt_pageinit`), `InitPGMetaPage` (`_bt_initmetapage`),
      and `CheckPGBTPage` — a Go transcription of `_bt_checkpage` so writers can
      be gated on the oracle's own acceptance test. Additive: the legacy layout
      in `btree.go` is untouched. Layout verified twice — a
      `sizeof`/`offsetof` probe against `postgres/src/include`, and a byte
      golden taken from a metapage a real PG 18.3 wrote
      (`bench/tpch/runtime/pgdata/base/1`). Guards:
      `internal/access/btree/pgformat_test.go` (7 tests; both padding clears
      mutation-verified).
- [x] **M0130-S11.2a — page-shape primitives** (est ~1 loop). **DONE
      (2026-08-10).** `internal/storage/linepointer.go`:
      `PageReserveLinePointer` + `PageDeleteLinePointerAt` (upstream's
      `_bt_blnewpage` `pd_lower` bump and `_bt_slideleft`) — the two
      line-pointer-array operations goopg could not express.
      `internal/access/btree/pgpage.go`: `P_HIKEY`/`P_FIRSTKEY`,
      `PGFirstDataKey`, the data-slot accessor wrappers that hide the high key
      from the ~45 `storage.PageXxx` call sites, the high-key item accessors
      (`PGHighKeyRaw`, `pgSetHighKeyRaw`, `pgPromoteToNonRightmost`),
      `pgReserveHiKeySlot`/`pgSlideLeft`, and the sentinel/flag translators.
      Additive — no writer flipped yet. Guards: `pgpage_test.go` (8) +
      `linepointer_test.go` (3); the `P_FIRSTDATAKEY` bias mutation-verified.
- [x] **M0130-S11.2b — the flip** (est ~2 loops). **DONE (2026-08-10).**
      `readOpaque`/`writeOpaque`/`ParseOpaque` now speak upstream's 16-byte
      `BTPageOpaqueData`; `BTHasHighKey` is deleted and `HasHighKey()` is
      `!P_RIGHTMOST`; the high key is an item at `P_HIKEY`; every accessor in
      `btree.go` / `btree_vacuum.go` / `lpdead_kill.go` goes through S11.2a's
      data-slot wrappers; the sibling sentinel and flag bits translate at the
      read/write boundary only (one `flagTranslation` table for both
      directions). Three invariants the plan did not anticipate, all now
      guarded: repointing `btpo_next` must slide the separator away in the
      same step (`pgWriteNextSibling`), `resetPageItems` must preserve it
      across all four whole-page rewrite paths, and the separator is now paid
      for out of page space (`pageHighKeyFootprint`, `bulkHighKeyReserve`).
      **Every pre-existing goopg index must be REINDEXed** — `BTREE_VERSION`
      could not carry the break (upstream requires 4). 1 ledger row.
- [x] **M0130-S11.3 — metapage** (est ~1 loop). **DONE (2026-08-10).**
      `BTreeMeta`/`parseMeta`/`writeMeta` deleted; block 0 is built by
      `initMetaPage` → `InitPGMetaPage` (`_bt_initmetapage`) at all four
      creation sites, so it is a PG-shaped page (16-byte special area,
      `BTP_META`) with the 48-byte `BTMetaPageData` at `PageGetContents` and
      `pd_lower` advanced past it. The root-pointer writers and
      `ReplayMetaSetRoot` are read-modify-write (the cleanup counters and
      `btm_allequalimage` belong to other writers); `readMeta` gates block 0 on
      `CheckPGBTPage` so the format break is a clean error instead of a silent
      garbage decode, and `Open` uses `_bt_getmeta`'s version range.
      **Pre-existing indexes must be REINDEXed.** `btm_allequalimage` is
      unconditionally true and the cleanup counters are never updated — 1
      ledger row.
- [x] **M0130-S11.4 — tuple shape** (est ~3 loops, LARGE). goopg `item` →
      `IndexTupleData` (8-byte header) + null bitmap + PG binary datums;
      internal-page downlinks into `t_tid`. Couples the index format to the
      type-codec layer. Decomposed into three loops:
  - [x] **slice 1 — the codec, additive** (2026-08-10).
        `internal/access/btree/pgtuple.go`: `FormPGIndexTuple`/
        `DeformPGIndexTuple` (`index_form_tuple`/`index_deform_tuple` over
        `heap_compute_data_size`/`fill_val`), the `itup.h` `t_info` accessors,
        the `(bi_hi, bi_lo)` `ItemPointerData` codec, `nbtree.h`'s
        alternative-TID overlay (pivot/posting predicates, `Set`/`GetNAtts`,
        downlink, tiebreaker heap TID) and `BTMaxItemSize` +
        `CheckPGBTItemSize` (`_bt_check_third_page`). No writer touched.
        Byte-compared against 8 of the PG-validated hand-rolled encoders in
        `internal/initdb/btree_index_bootstrap.go`
        (`TestPGIndexTupleMatchesBootstrapEncoders`) — those are an oracle,
        since a real PG 18.3 reads the bootstrap indexes they write. No
        in-line compression / external detoasting (TOAST_INDEX_HACK) and no
        posting-list writer — 1 ledger row.
  - [x] **slice 2 — flip the writers** (2026-08-10). `(item).marshal`/
        `parseItem`/`parseItemNoCopy` emit and read upstream `IndexTupleData`;
        the private `itemPrefixSize` body and the `item.keyLen` field are gone
        (the key length comes from `t_info`'s size). Downlinks live in `t_tid`
        via the new `downlinkItem` (= `BTreeTupleSetDownLink`), and the one
        encoder is exported as `PGBTItemRaw` so `internal/amcheck`'s fixtures
        stop hand-rolling their own. `posting.go` had to move in the same
        commit: goopg's posting flag was the high bit of the old leading
        `keyLen`, which is now `t_tid`'s `bi_hi` half, so posting tuples are
        rewritten onto upstream's `INDEX_ALT_TID_MASK` + `BT_IS_POSTING|nhtids`
        + posting-offset layout (`BTreeTupleSetPosting`, new in `pgtuple.go`),
        and `parseItem` rejects any alt-TID tuple. **Pre-existing indexes must
        be REINDEXed.** Deferred (1 ledger row): `index_form_tuple`'s MAXALIGN
        of the tuple size and `BTreeTupleSetPosting`'s MAXALIGNed posting
        offset — both destroy the opaque key's only length record until
        slice 3 makes it descriptor-derived.
  - [x] **slice 3a — pivot tuples** (2026-08-10). Internal-page downlinks and
        `P_HIKEY` separators are now real nbtree PIVOT tuples
        (`INDEX_ALT_TID_MASK` + natts in t_tid's offset half + downlink in its
        block half) built by the one new encoder `PGBTPivotRaw` — upstream's
        `_bt_truncate` output for goopg's current key shape. `parseItemBody`
        decodes pivots (translating t_tid's two halves once) instead of
        rejecting all alt-TID tuples; the rejection narrows to BT_IS_POSTING.
        The `item` struct carries an in-memory `pivot` flag so the
        parse/re-marshal round trip in the split, VACUUM and WAL-replay page
        rewrites cannot demote a pivot to a plain tuple, and the three
        "leftmost item adopts a nil key" sites rebuild through `downlinkItem`
        so the minus-infinity downlink stays a zero-attribute pivot. Guards:
        `pgpivot_tree_test.go` walks every page of an engine-built and a
        bulk-loaded tree asserting the invariants (mutation-verified),
        `pgitem_test.go` +4, and `PGBTPivotRaw(nil, child)` is byte-compared
        against initdb's PG-validated bootstrap encoder. Deferred (2 ledger
        rows): pivot natts is always 1 for a keyed separator (one opaque key
        blob — no real suffix truncation), and the tiebreaker-heap-TID pivot is
        not written.
  - [x] **slice 3b — comparison layer**. Key bytes become per-attribute binary
        datums, so `bytes.Compare` gives way to type-aware comparison. That is
        what makes the key length descriptor-derived, and with it: the two
        MAXALIGNs slice 2 deferred (tuple size, posting offset), real suffix
        truncation (`_bt_keep_natts` → pivot natts < nkeyatts), and retiring
        `MaxHighKeyLen`/`bulkHighKeyReserve` in favour of `BTMaxItemSize`.
        Itself milestone-sized (it is the slice that couples the on-disk format
        to the type layer), so decomposed into three:
    - [x] **3b-1 — descriptor + comparator, additive** (2026-08-10).
          `internal/access/btree/pgcompare.go`: `PGKeyAttr` /
          `PGIndexKeyDesc` (the physical `PGIndexAttr` plus the three ordering
          properties `_bt_compare` consults — the opclass comparator,
          `SK_BT_DESC`, `SK_BT_NULLS_FIRST`) and `ComparePGIndexTuples`,
          `_bt_compare`'s body for the tuple-vs-tuple case. Takes upstream's
          own seam out of "nbtree knows no types": the BTORDER_PROC comparator
          the caller installs, here a plain left-vs-right func (not upstream's
          flipped-argument convention), so DESC is one negation in one place.
          Three rules a naive attribute loop misses, all mutation-verified:
          truncated attributes are MINUS INFINITY (the shorter side sorts
          first — this is what orders the zero-attribute downlink with no
          special case), the heap TID is the final key attribute and an absent
          one is minus infinity too, and NULL ordering is per-attribute. A nil
          `Compare` means `CompareKeys` on purpose — that is what lets 3b-2
          migrate one type at a time instead of as a flag day. Additive: no
          writer builds a descriptor yet, nothing on disk moved. Guards:
          `pgcompare_test.go` (9). Not modelled (1 ledger row): collations,
          cross-type comparison, and posting-list tuples (rejected, not
          guessed).
    - [x] **3b-2 — thread the descriptor, retire `CompareKeys`**. Build the
          descriptor from the catalog (`pg_index.indoption` carries DESC and
          NULLS FIRST independently), pass it through `btree.Options`, convert
          the ~20 `CompareKeys` call sites, and flip the writer to
          `FormPGIndexTuple` over per-column datums **in the same commit**
          (sibling-path rule: a descriptor-derived reader against a
          blob-writing writer reads garbage). REINDEX-required break.
          Decomposed into three:
      - [x] **3b-2a — the opclass comparators** (2026-08-10).
            `internal/access/btree/pgcompare_types.go`: btree support-function-1
            for every type goopg indexes, over the datum's real PG binary image
            (little-endian native, the x86-64 PG 18.3 layout `encodeValuePG`
            already assumes). 3b-1 left `PGKeyAttr.Compare` with only its nil
            default = `bytes.Compare`, which is correct exactly while the keys
            are goopg's order-preserving encodings; the instant 3b-2b's writer
            stores real datums it is wrong for nearly every type, so this had
            to exist before the flip had anything correct to switch to.
            `btint2/4/8cmp` (signed LE), `btoidcmp` (**unsigned**),
            `btboolcmp`, `btcharcmp` (unsigned by upstream's explicit choice),
            `btfloat4/8cmp` (NaN largest and equal to itself; −0 = +0),
            `byteacmp`/`bttextcmp` (C collation), `bpcharcmp` (blank-padded —
            `bcTruelen`), `btnamecmp`, `uuid_cmp`, `timetz_cmp` (GMT-equivalent
            first) and `numeric_cmp` over the on-disk `NumericData`
            (−Inf < finite < +Inf < **NaN**, plus the short header's
            sign-extended 7-bit weight). date/timestamp/time reuse int4/int8 as
            upstream does. No error return by design (innermost descent loop,
            like `sk_func`): a length that does not match attlen falls back to
            `bytes.Compare` — total and deterministic, so a split terminates —
            and corruption stays amcheck's business. Additive; nothing builds a
            descriptor yet. Guards: `pgcompare_types_test.go` (18), 9 mutations
            caught. Deferred (1 ledger row): non-C collations, and the types
            with no comparator yet (arrays, enum, bit/varbit, inet/cidr/macaddr,
            interval, money, tsvector/tsquery, jsonb, range/multirange).
      - [x] **3b-2b — build the descriptor from the catalog** (2026-08-10).
            `internal/executor/pgindex_keydesc.go`: `buildPGIndexKeyDesc(idx
            *catalog.Index) (*btree.PGIndexKeyDesc, error)` — goopg's
            `_bt_mkscankey` minus the scan. Reads what pg_index records (key
            column types, `ColDescending`/`ColNullsFirst` = indoption — both
            EMPTY for the all-default ASC NULLS LAST case, so every read is
            bounds-checked, and both carried independently since `DESC NULLS
            LAST` is legal — `ColOpClasses` = indclass, `ColCollations` =
            indcollation) and projects the type OID through
            `userTypeAttrsForOID` → `PGIndexAttr` and
            `pgIndexComparatorForOID` → the 3b-2a comparator. Executor-side by
            necessity (`internal/access/btree` does not import `catalog`).
            **Conservative to the point of erroring**: non-btree AM,
            expression key, explicit opclass (no `pg_opclass` registry to
            resolve one), non-bytewise collation, array/enum/user type, or any
            type with no comparator → error, never a descriptor with a nil
            `Compare`. Nil means bytewise, which is correct only for the
            current order-preserving key encodings and silently mis-orders the
            tree the moment the writer stores real datums. Type resolution is a
            private built-in switch, NOT `buildUserPGAttributeRow`'s, whose
            `text` fallback would hand an enum column the text comparator while
            goopg orders enums by sort order. Guards:
            `pgindex_keydesc_test.go` (9). The writer flip moved to 3b-2c —
            see there.
      - [x] **3b-2c — flip the writer AND route every comparison through the
            descriptor** (REINDEX-required break). Split into 3b-2c-i (the
            seam, behaviour-preserving — **DONE 2026-08-10**) and 3b-2c-ii
            (plumbing A + codec seam B1 **DONE 2026-08-10**; the writer flip
            B2 still open) — see the two sub-items below. The
            writer flip was planned for 3b-2b; building the mapper showed it
            cannot land there, because the sibling-path rule is symmetric — a
            descriptor-derived reader against a blob-writing writer reads
            garbage, and equally a datum-writing writer against the surviving
            `CompareKeys` sites ORDERS garbage (real datums are not
            order-preserving under `bytes.Compare` for any type but
            bytea/text). So `encodeCompositeBTreeKey` /
            `encodeIndexKeyFromCols` / `encodeArbiterKey` →
            `FormPGIndexTuple` must land with the comparison rerouting. The
            seam comes first: the ~20 in-package `CompareKeys` sites compare
            *key payloads*, while `ComparePGIndexTuples` needs whole tuples
            (t_info's null bitmap, t_tid's natts/heap TID), so one
            `BTree`/bulk comparison method over tuple-shaped operands has to
            exist before either half moves. That is what finally retires
            `CompareKeys`. Gates: `scripts/tpch-spotcheck.sh` + the TPC-DS
            SF0.5 gate (re-pin after a REINDEX).
        - [x] **3b-2c-i — the seam** (2026-08-10). NO on-disk change, no
              REINDEX. `internal/access/btree/pgkeycmp.go`: `keyComparer`
              (one per-index comparer over an optional `*PGIndexKeyDesc`),
              carried by `Options.KeyDesc` → `BTree.cmp` →
              `(*BTree).keyCmp()`. All ~20 in-package `CompareKeys` sites now
              route through it: descent (`descendToLeaf`,
              `findChildBlockDirect`), both high-key overshoot tests, the
              insert-slot binary search (`insertItemSorted`), the
              rightmost-cache range check, the split-path page rewrite
              (`appendSorted`/`dedupConsolidate`), `Search`, `rangeScanPos`,
              and both bulk-load sorts + `deduplicateToRawItems`. The free
              helpers take the comparer as a parameter, so no caller can
              silently pick up the wrong index's order. `CompareKeys` survives
              only as the seam's nil-descriptor branch and as amcheck's /
              `bt_index_check`'s name for goopg's default order. `compare`
              returns NO error by design (it runs inside `sort.Search`
              predicates and the descent loop, upstream's `sk_func`
              constraint): an operand `ComparePGIndexTuples` refuses — a
              posting-list tuple, whose heap-TID tiebreak is ambiguous —
              falls back to bytewise, keeping the order total and
              deterministic so a split terminates. Guards:
              `pgkeycmp_test.go` (4), 3 mutations caught. Deferred (1 ledger
              row): `ApplyInsertRecord` (WAL redo) has no `BTree` handle and
              so passes the bytewise comparer explicitly — 3b-2c-ii must
              carry a per-relation descriptor lookup into the redo path.
        - [x] **3b-2c-ii-A — the plumbing** (2026-08-10). No on-disk change.
              `internal/executor/pgindex_btree.go`: the three choke points
              `openIndexBTree` / `createIndexBTree` / `bulkCreateIndexBTree`,
              each resolving `buildPGIndexKeyDesc` into `Options.KeyDesc`.
              The executor's **nineteen** direct `btree.Open` /
              `CreateWithXID` / `BulkCreateWithXID` calls are gone — the
              package now names a btree constructor only inside that one
              file, which is grep-enforceable and is the invariant 3b-2c-ii-B
              depends on. The descriptor is memoised per statement on
              `Context.pgKeyDescCache` (keyed by index OID; a present-but-nil
              entry caches the REFUSAL, because the callers are per-row index
              maintenance and re-deriving a refusal per row is the expensive
              case). `bulkBuildBTreeFull` gained an `*catalog.Index` param —
              not derivable from the relfilenode, since REINDEX CONCURRENTLY
              builds into a shadow relfile. `btree.PoolLogSplit` exported so
              an assembled `Options` cannot silently drop split WAL logging;
              `(*BTree).KeyDesc()` makes "the descriptor reached the tree"
              observable. Gate `pgIndexTupleKeys` is **false** → every
              descriptor nil → `CompareKeys` byte for byte; it is a var, not
              a const, so the plumbing is testable ahead of the flip
              (`pgindex_btree_test.go`, 5 tests). Indexes the resolver
              refuses (expression key / explicit opclass / non-bytewise
              collation / no comparator) get nil and keep the blob path.
        - [x] **3b-2c-ii-B1 — the item-codec seam** (2026-08-10). NO on-disk
              change, no REINDEX. The descriptor decides a third thing besides
              the ordering: what `item.key` IS — a header-less payload (blob)
              or the whole `FormPGIndexTuple` image (tuple), the only operand
              shape `ComparePGIndexTuples` can order. So `keyComparer` was
              renamed **`indexFormat`** and grew the codec
              (`internal/access/btree/pgitemcodec.go`): `marshal` / `parse` /
              `parseNoCopy` / `bodySize` / `itemEncodedSize` /
              `pageHasSpaceFor` plus the page helpers that moved onto it
              (`pageItems`, `pageItemsWithDead`, `pageHighKey`, `readPageItem`,
              `byteAwareSplitLoc`, `compactRawSize`, `itemsToRawItems`,
              `pageHasSpaceForBulk`, `snapshotPageItemsAsLog`). ~30 call sites
              now ask the tree's format instead of hard-wiring the blob layout.
              Ordering and layout are ONE object on purpose — one decision
              (`desc`), and a disagreement between them mis-ORDERS rather than
              failing to parse. Sites with a page but no index identity name
              `blobFormat` explicitly (greppable): the four exported page
              readers (amcheck's) and both redo entry points — exactly what B2
              must teach to resolve a per-index format. Guards:
              `pgitemcodec_test.go` (6), 2 mutations caught; the tuple branch
              is driven END TO END (3000 int4 keys, out-of-order across the
              sign boundary, scanned in exact `btint4cmp` order through splits
              and multi-level descent, with the on-page bytes asserted to BE
              the tuple — ordering alone does not catch a layout slip). Bug
              found and fixed: `ComparePGIndexTuples` panicked on an operand
              shorter than a tuple header, i.e. on a minus-infinity search key
              (`rangeScanPos(nil, …)`); it now errors so `compare` falls back
              to bytewise, where an empty key sorts first = minus infinity.
        - [x] **3b-2c-ii-B2-a — the key encoder** (2026-08-10). NO on-disk
              change, no REINDEX. `internal/executor/pgindex_tuplekey.go`:
              `pgIndexTupleKey` / `pgIndexTupleKeyFromRow` /
              `pgIndexKeyColumns` turn a row's key datums into the
              `FormPGIndexTuple` image — the converter no layer could produce
              before (3b-2b said how attributes are laid out and ordered;
              nothing turned a `Datum` into an attribute image). The bytes come
              from `encodeValuePG`, the HEAP's encoder, because upstream's
              `index_form_tuple` and `heap_form_tuple` share `heap_fill_tuple`
              and a second encoder would be the sibling-pair divergence again.
              A prefix (partial-key) search key is stamped as a pivot via
              `BTreeTupleSetNAtts` — without it `BTreeTupleGetNAtts` claims the
              index's full key count and `DeformPGIndexTuple` reads past the
              data area; with it the truncation rule gives "position at the
              first entry matching this prefix" for free. Nothing calls it on a
              production path yet. Guards: `pgindex_tuplekey_test.go` — a
              per-type ordering table over EVERY type the descriptor accepts
              (values chosen to break LE-bytewise, sign, IEEE-754, varlena
              headers and bpchar blank-padding), which also asserts the table
              disagrees with `bytes.Compare` somewhere, else it could not
              detect a regression to bytewise; plus the pivot/minus-infinity
              rule, the zero-TID search key, NULL + DESC/NULLS FIRST, the
              row→key-order projection, and TOAST/oversize refusal.
              **Discovery: goopg's stored image for `numeric` and `uuid` is NOT
              PostgreSQL's** — `encodeValuePG` writes the decimal string and
              the 36-char UUID as text varlenas, so `PGCompareNumeric` orders
              `-1000` above `0` and a `uuid` key would be a 16-byte window onto
              a 37-byte varlena. Both are HEAP-side divergences (a real PG 18.3
              already misreads those columns), so `buildPGIndexKeyDesc` refuses
              them (`pgIndexKeyImageIsPGFaithful`) and they keep the blob path.
        - [x] **3b-2c-ii-B2-b — the format-resolution sites** (2026-08-10). NO
              on-disk change, no REINDEX. All six `blobFormat` sites — the four
              exported page readers (`PageItemKeys`/`PageLeafItems`+
              `PageLeafEntries`/`PageDownlinks`/`PageHighKey`) and the two redo
              entry points (`ApplyInsertRecord`, `ReplayRemoveParentDownlink`)
              — are now methods on the exported `btree.IndexFormat`, so the
              format is an ARGUMENT the caller supplies. `IndexFormatFor(desc)`
              from a catalog descriptor, `(*BTree).Format()` from a live tree,
              zero value = blob (same nil-means-blob convention as
              `Options.KeyDesc`). `bt_index_check` resolves it for real from
              `ctx.pgIndexKeyDesc(idx)` and threads it through `btIndexVerify`
              → `btIndexLeftmostByLevel`. Guard: `pgpagereaders_test.go` pins
              both decoders on the same bytes. Two callers still cannot
              resolve, each named once (below).
        - [x] **3b-2c-ii-B2-b-ii — the redo path's descriptor**
              (2026-08-10). Took the preferred route: goopg's btree redo is now
              OFFSET-based like upstream's `btree_xlog_insert`, so redo needs no
              format and `internal/wal/recovery.go:redoBlobIndexFormat` is gone
              — **B2-c is unblocked**. `btree.ApplyInsertRecord` (parse +
              re-insert by key) → `btree.ApplyInsertRecordAt(page, raw,
              offnum)`, one `PageInsertItemRawAt` at the recorded physical
              offset; `LogBtreeInsertFunc`/`EncodeBtreeInsert` carry the offset
              (native header 14 → 16 bytes, `offnum == 0` rejected as a
              pre-slice record) and `EncodeBtreeInsertPG` stops hard-coding 0,
              which also closes the wal-pg-identical-stream A5 parity gap (a
              real-PG standby applies at `offnum`). `ReplayRemoveParentDownlink`
              became format-free by working on raw item bytes: survivors are
              re-added verbatim and both facts it needs live in the
              IndexTupleData header (`len(raw) > SizeOfIndexTupleData` =
              "still has key attrs", `BTreeTupleGetDownLink` = the child).
              Guard: `internal/access/btree/replay_offnum_test.go` — writer-page
              reproduction in both formats × both page shapes, plus the retired
              by-key body kept as an executable counter-example that
              demonstrably files a tuple-format item at the wrong slot.
        - [x] **3b-2c-ii-B2-b-iii — amcheck takes the format** (2026-08-10).
              The five `internal/amcheck` tiers (`VerifyBtreeItemOrderCmp`,
              `VerifyBtreeParentDownlinks`, `VerifyBtreeUnique`,
              `CollectBtreeLeafEntries`,
              `VerifyBtreeHeapAllIndexedRelation`) plus the shared
              `leftmostLeafBlock` descent take a `btree.IndexFormat` from their
              caller, exactly as `cmpKeys amcheck.KeyComparator` is threaded;
              `amcheck.blobIndexFormat` is gone and
              `operators_bt_index_check.go` passes its already-resolved
              `keyFmt` down. Guard:
              `internal/amcheck/verify_nbtree_tupleformat_test.go` (real
              400-key tuple-format tree with splits — whole index tuples out of
              the leaf walk, zero keys agreeing with the blob decode, clean
              item-order tier under format + descriptor comparator).
        - [x] **3b-2c-ii-B2-b-iv — the parent-downlink comparator**
              (2026-08-10). `amcheck.VerifyBtreeParentDownlinks` takes
              `cmpKeys amcheck.KeyComparator` next to the `keyFmt` B2-b-iii
              gave it (nil ⇒ `btree.CompareKeys`) and evaluates its down-link
              lower bound through it; `btIndexVerify` passes the comparator it
              already held. Matches upstream, where EVERY amcheck key
              comparison goes through the index's support function 1
              (`bt_child_check` → `invariant_l_nontarget_offset`,
              verify_nbtree.c:2500-2540), so opclass damage on a separator is
              now reported by the cross-level tier too and the flip compares
              key columns instead of whole tuples. Guard: section (d) of
              `verify_nbtree_tupleformat_test.go` — clean under the
              descriptor's comparator, reporting under a nil one. That test's
              tree also grew 400→1200 keys: 400 int4 tuples fit on ONE leaf
              page, so it had no internal level and the cross-level tiers were
              never exercised.
        - [x] **3b-2c-ii-B2-c-i — the prefix upper bound** (2026-08-10). NO
              on-disk change, no REINDEX. A range scan's two bounds are not
              symmetric once keys are tuples. A search key naming only the first
              k key attributes is a pivot, and `ComparePGIndexTuples` makes the
              shorter operand MINUS infinity beyond them — right for the LOW
              bound (the descent lands on the group's first member), wrong for
              the HIGH bound, where `compare(entry, hi) > 0` already holds for
              that same first member and the scan returns ZERO rows. The blob
              format hid this by faking plus infinity with bytes
              (`appendCompositeUpperPadding`'s 64 `0xFF`), which a tuple cannot
              use — `0xFF` is a malformed attribute image, not a large one — and
              upstream never invents a maximal key either (`_bt_check_compare`
              stops when the compared ATTRIBUTES exceed the bound). So the sense
              of a truncated bound is now a property of the comparison, one per
              end: `indexFormat.compare` is the low end (and descent / insert
              slot / split point), the new `indexFormat.compareHigh` is the high
              end, used by `rangeScanPos`' two `hi` tests. `desc == nil` ⇒
              `compareHigh` IS `CompareKeys`, byte for byte, which is what let it
              land ahead of the flip. Guard:
              `internal/access/btree/prefix_highbound_test.go` (blob
              equivalence; the asymmetry — one pair reading `>0` under `compare`
              and `0` under `compareHigh`; a full-attribute bound agreeing with
              `compare` including the heap-TID tiebreak; and a 1200-entry
              two-column tree scanned across leaf-page boundaries with a prefix
              pivot as BOTH bounds, group complete AND exclusive of the next).
              Mutation-checked: reverting `rangeScanPos` to `compare` turns the
              30-row group into 0 rows.
        - [x] **3b-2c-ii-B2-c-ii — the upper-bound funnel** (2026-08-10). NO
              on-disk change, no REINDEX. B2-c-i gave the SCAN a high end that
              reads a truncated bound as plus infinity; this gives the PROBES
              one place to decide what to hand it. Six sites — index scan
              (equality + range), index-only scan (equality + range), bitmap
              index scan, and the storage UPDATE-by-index path — each
              open-coded `appendCompositeUpperPadding(key)`, i.e. each
              independently asserted that a prefix upper bound is spelled with
              64 `0xFF` bytes, which is true of exactly ONE of the two formats.
              All six now call `(*Context).compositeUpperBound(idx, key)`,
              which resolves the same `pgIndexKeyDesc` the tree took: padded
              blob bound for `desc == nil`, the prefix pivot UNCHANGED
              otherwise. Gate off ⇒ byte-for-byte unmoved; gate on ⇒ the flip
              no longer touches these six files. Side effect worth naming: the
              sites' `len(Index.Columns) > 1` guard degrades from a correctness
              condition to a cheap skip, since under the tuple format widening
              is a no-op and a full-attribute bound compares identically under
              `compareHigh` and `compare`. An index the resolver refuses keeps
              the padding even with the gate on — the dual-format property
              asserted, not assumed. Guards:
              `internal/executor/pgindex_upperbound_test.go` (blob equality +
              no aliasing of the caller's key, which is simultaneously the LOW
              bound; the tuple branch with no `0xFF` run; the undescribable
              index; and a source scan pinning `compositeUpperBound` as the
              helper's only caller — mutation-checked by reverting the bitmap
              site, reported by file:line). The scan matters because a seventh
              site added later fails as wrong ROWS, never as a compile error.
        - [x] **3b-2c-ii-B2-c-iii — the probe-key funnel** (2026-08-10). NO
              on-disk change, no REINDEX; the low-end twin of B2-c-ii. The same
              six scan sites built the key they POSITION with (equality probe /
              range bound) by calling `encodeBTreeKeyForColumn` per attribute
              and CONCATENATING — ten call sites each asserting the blob
              format's whole key layout. A tuple-format page key is one
              `FormPGIndexTuple` image, not a concatenation. All ten now call
              `(*Context).indexProbeKey(idx, parts)`: concatenation for
              `desc == nil`, `pgIndexTupleKey` with a ZERO `ItemPointer`
              otherwise (heapkeyspace minus infinity, so an equality probe still
              lands before every real entry with the same key attributes — what
              the blob path got by having no TID). A short probe is a pivot
              stamped with its own natts, the low-end mirror of B2-c-i's plus
              infinity. Under the tuple format there is NO fallback: a
              `pgIndexTupleKey` refusal (TOAST pointer, over-size key) errors
              rather than emitting a blob key that would scan the wrong range;
              refused indexes never get there (`desc == nil`). The funnel also
              takes its columns from `pgIndexKeyColumns` and checks the caller's
              against them — blob tolerated a non-leading probe (matched
              nothing), a pivot silently means "the first N attributes". Guards:
              `internal/executor/pgindex_probekey_test.go` (blob = the
              concatenation; tuple = `pgIndexTupleKey`, natts 2, and a
              1-attribute probe that is a 1-natts pivot and NOT a byte prefix of
              the full key; undescribable index stays blob; non-leading and
              over-long probes rejected; source scan over the three scan files
              pinning `indexProbeKey` as the only scan-side encoder,
              mutation-checked by reverting the bitmap site).
        - [x] **3b-2c-ii-B2-c-iv — the row-key funnels** (2026-08-10). NO
              on-disk change, no REINDEX; the writer-side counterpart of
              B2-c-iii. The finding: `encodeIndexKeyFromCols` has served FOUR
              roles since M0100-0005 — the key an entry is STORED under, the key
              a uniqueness/exclusion probe POSITIONS with, a value fingerprint
              compared with `bytes.Equal` (`indexKeyColumnsChanged`), and a value
              fingerprint hashed into an SSI bucket tag
              (`ssiRecordHashIndexInsert`). A blob key is TID-free so all four
              are the same bytes; a tuple key embeds the heap TID, so the stored
              key needs the row's real TID, the probe key needs the ZERO TID
              (minus infinity — otherwise a duplicate scan starts after some of
              its own matches), and the two fingerprints must stay TID-free or
              every UPDATE reports "key changed" and SSI writers hash into the
              wrong bucket. Landed `(*Context).indexEntryKey` +
              `(*Context).indexRowProbeKey` over one `indexRowKey`, seven call
              sites routed (3 entry, 4 probe), the shared projection factored out
              as `indexRowKeyValues`, and the spec-insert key cache bypassed when
              a descriptor exists. Guards:
              `internal/executor/pgindex_rowkey_test.go` (blob entry == probe ==
              the old encoder; tuple entry != probe with `probe < entry` under
              `ComparePGIndexTuples` and identical deformed attributes; NULL key
              still `nil, nil`; undescribable index keeps blob; source scan over
              the two fully-routed files, mutation-checked).
        - [x] **3b-2c-ii-B2-c-v — the build path's key, order and duplicate
              test** (2026-08-10). NO on-disk change, no REINDEX; the last
              writer funnel. The bulk build has a property the runtime writers
              do not: it SORTS its entries and then decides uniqueness by
              comparing neighbours, and both steps were blob-format assertions.
              A bytewise sort (`string(key)`) is meaningless for a tuple image —
              a PG datum is order-preserving under `bytes.Compare` for no type
              but bytea/text — so entries would be filed where no `_bt_compare`
              descent looks for them; and `bytes.Equal` on tuple images can
              NEVER report a duplicate, because the heap TID is inside the image
              and distinct by construction, so a unique build over duplicated
              data would succeed and produce a unique index containing
              duplicates. Upstream keeps both questions in one comparator and
              answers them in this order — `comparetup_index_btree`
              (tuplesortvariants.c:1668) raises 23505 when the key attributes
              all compare equal, THEN falls through to the ItemPointer tiebreak.
              Landed `(*Context).indexBuildEntryKey` (blob ⇒
              `encodeCompositeBTreeKeyWithExprs` verbatim, tuple ⇒
              `pgIndexTupleKey` with the row's real heap TID),
              `btree.ComparePGIndexTupleKeyAttrs` (the full comparison minus the
              TID tiebreak) and `sortBuildEntriesFindDuplicate`, which folds the
              deleted `sortBulkEntriesByKey` + `bytesEqual` into one
              format-aware place; `collectBTreeEntries` now takes the index it
              is building. `backfillBTree` is unrouted dead code and its comment
              now says so. Guard:
              `internal/executor/pgindex_buildkey_test.go` (blob verbatim under
              both a zero and a real TID; tuple key carries the TID, is not a
              pivot, orders duplicates by TID yet compares EQUAL on key
              attributes; NULL key column unchanged in both formats;
              undescribable index keeps blob; 1 sorts before 256 where the
              bytewise order is the opposite; the duplicate test fires;
              scoped source scan over operators_ddl.go). Mutation-checked
              (forcing the blob branch, and restoring the TID tiebreak in the
              duplicate test, each fail by name).
        - [x] **3b-2c-ii-B2-c-vi — posting-list dedup groups by KEY
              attributes** (found by B2-c-v; must land with or before the flip).
              LANDED 2026-08-10: `indexFormat.compareKeyAttrs` (nil desc ⇒
              `CompareKeys` byte for byte) now closes the run in
              `deduplicateToRawItems`. The posting LAYOUT moved with it, since
              a run that forms is a run that gets marshalled: `marshalPosting`
              / `parsePostingRaw` are `indexFormat` methods with
              `postingOffsetFor` naming the split (blob key at `[8:]`
              unchanged; tuple key at `[0:MAXALIGN(len)]` per
              `_bt_form_posting`), a tuple-format parse returns the plain leaf
              tuple the posting stands for (first TID restamped, alt-TID bit
              cleared) so the round trip closes, and the new
              `indexFormat.postingItems` centralises the four page readers'
              expansion with a PER-TID key stamp. Guard:
              `internal/access/btree/pgposting_format_test.go` (5 structural
              tests, mutation-checked).
              `deduplicateToRawItems`
              (`internal/access/btree/bulkload.go:518`) groups adjacent bulk
              entries with `f.compare(a, b) == 0`, which under the tuple format
              includes the heap-TID tiebreak — no two entries of a heapkeyspace
              tree are ever equal, so NO posting list is ever formed and a
              duplicate-heavy index grows to one item per TID. Upstream's
              `_bt_load` groups by key attributes (`_bt_keep_natts_fast`,
              nbtsort.c / nbtutils.c). Fix: give `indexFormat` a
              `compareKeyAttrs` (nil desc ⇒ `CompareKeys`, else
              `ComparePGIndexTupleKeyAttrs`) and group with it. Invisible to a
              row-count gate — it changes index SIZE, not rows — so it needs a
              size/structure assertion, not a spotcheck.
        - [x] **3b-2c-ii-B2-c-vii — the arbiter-key funnel** (2026-08-10). NO
              on-disk change, no REINDEX. `encodeArbiterKey` built ONE key that
              the upsert path both PROBED the arbiter tree with and INSERTED
              into it — sound under the blob format (a blob key has no TID, and
              the reuse is what keeps a side-effectful arbiter expression from
              being evaluated twice, including `applyInsert`'s Phase-B key,
              computed before the heap write), a missed conflict under the tuple
              format (the entry carries the row's heap TID, the probe the zero
              TID that is minus infinity). Landed: `Context.arbiterProbeKey` /
              `arbiterEntryKey` over one `arbiterKey` (blob branch =
              `encodeArbiterKey` verbatim), the nine `operators_upsert.go` call
              sites routed by role, `applyInsert` rebuilding the entry key from
              the probe key once the TID exists (tuple format only), arbiter
              ordinals reconciled with the index's key attributes BY NAME (they
              address the upsert's table, the index may be resolved on another),
              and the three `encodeExprIndexKey` fallbacks made blob-only.
              Guard: `internal/executor/pgindex_arbiterkey_test.go` (6 tests,
              mutation-checked: entry TID dropped, name reconciliation dropped).
        - [x] **3b-2c-ii-B2-c-viii — the fingerprint funnel** (2026-08-10). NO
              behaviour change at all, no on-disk change, no REINDEX. The
              counterpart of B2-c-iii..vii: once every TREE-KEY producer had a
              role name that switches format, what was left on the raw blob
              encoders was a different kind of caller — a FINGERPRINT, compared
              with (or hashed alongside) another fingerprint of the same index
              and never handed to a btree. Landed:
              `internal/executor/pgindex_fingerprint.go` with whole-key
              `indexKeyFingerprint` (`indexKeyColumnsChanged`,
              `ssiRecordHashIndexInsert`) and per-column
              `indexColumnFingerprint` (the three NULLS NOT DISTINCT sites),
              neither taking a `*Context`, a descriptor or an ItemPointer, so
              neither can acquire a heap TID by accident. **Named invariant:**
              after the flip goopg computes a key TWO ways for a describable
              index — the tuple image for the tree, the blob concatenation for
              the fingerprints — so `encodeIndexKeyFromCols` /
              `encodeBTreeKeyForColumn` SURVIVE the flip. Routing any of the six
              costs wrong behaviour, never an error (every UPDATE re-probing
              every unique index; an SSI writer hashing into a bucket no reader
              holds; the NND heap scan admitting a second NULL-keyed row).
              Discovery: the SSI hash bucket pairs the WRITER's fingerprint with
              the READER's *scan search key*, so it holds only while a hash
              index is undescribable — `buildPGIndexKeyDesc`'s access-method
              refusal is load-bearing for SSI, and is now guarded as such.
              Guard: `internal/executor/pgindex_fingerprint_test.go` (6 tests,
              incl. a function-scoped source scan; mutation-checked: one NND
              site reverted, the access-method refusal removed).
        - [x] **3b-2c-ii-B2-c — THE FLIP** (2026-08-10). REINDEX-required.
              `pgIndexTupleKeys` is now **true**: every index
              `buildPGIndexKeyDesc` describes is written and read as PG index
              tuples, and every index it refuses keeps the blob path — so a
              tree's key format is a per-INDEX catalog property with nothing on
              disk recording it (the metapage is byte-faithful `BTMetaPageData`,
              version must stay 4, so there is no field to stamp; hence
              REINDEX-required). The eight funnels had made every key PRODUCER
              format-aware; the flip uncovered three CONSUMERS that were not:
              `(*BTree).Search` asked FULL-key equality (the in-image heap TID
              made it unsatisfiable, so every unique probe reported "no such
              key") and descended left of a group starting at a page boundary —
              now `compareKeyAttrs` plus a `_bt_stepright`-style right step;
              `compareHigh` weighed the TID, putting every real entry above a
              bound naming its exact key, so an equality scan returned ZERO rows
              — a bound is a KEY bound, now key-attributes-only (the LOW end
              keeps the tiebreak, where minus infinity means "first duplicate");
              and the index-only scan, the one reader that runs the funnels
              backwards, decoded a tuple image with the blob running-offset walk
              — now `pgIndexTupleKeyDatums`, the inverse of `pgIndexTupleKey`.
              A fourth was found by the amcheck port: `checkunique` compared
              bytewise and had silently STOPPED DETECTING duplicates (an
              under-report by a corruption checker); it now runs under
              `IndexFormat.CompareKeyAttrs`. Test work split by kind: the
              byte-for-byte blob guards are still live code and pin the gate off
              via `withBlobIndexKeys`, while the DDL type-acceptance suites were
              asserting the format only incidentally and now route through the
              engine's own `openIndexBTree` / `indexProbeKey`.
              Gates: units PASS; tpch-spotcheck PASS (Q12 rows=2, Q13 rows=35);
              pgbench smoke PASS (its `select only` arm is an index scan on a
              freshly built tuple-format PK at 12.8k TPS); **TPC-DS SF0.5
              PASS=95 ERROR=0 MISMATCH=0 CKMISMATCH=0, plans identical** — after
              REINDEXing all 24 SF0.5 PKs (46s). That REINDEX exposed the loop's
              real find: the first sweep returned 42 ERRORs that were NOT this
              slice — every index scan on BOTH bench clusters failed
              `corrupted page at block 0: special size 0, want 16` identically
              on a gate-OFF rebuild, and the last green sweep predates the whole
              S11.2/S11.3 series. The clusters had carried those REINDEX-required
              breaks un-remediated for four slices, because nothing re-runs a
              REINDEX on a long-lived bench cluster and `tpch-spotcheck.sh`'s
              Q12/Q13 are seq-scan plans. TPC-H stays un-remediated (`REINDEX`
              in db `tpch` hits the per-DB scoping gap), so SF=1 index behaviour
              is ungated. Ledger rows filed for all of it, plus the NULL-keyed-row
              divergence and the missing "needs REINDEX" detection.
    - [x] **3b-3 — collect the deferrals**. The two MAXALIGNs, `_bt_keep_natts`
          suffix truncation, and `MaxHighKeyLen`/`bulkHighKeyReserve` →
          `BTMaxItemSize`.
        - [x] **3b-3a — MAXALIGNed item placement** (2026-08-10). NOT
              REINDEX-required. Slice 2 filed two MAXALIGNs under one reason —
              a blob key's length is only `size - SizeOfIndexTupleData`, so
              padding destroys it — but the reason covers only one of them.
              `PageAddItemExtended` rounds the ALLOCATION
              (`upper -= MAXALIGN(size)`) while `ItemIdSetNormal` records the
              EXACT size, so the pad lands between items and `lp_len` still
              yields the key length. `storage.PageAddItemRaw`,
              `PageInsertItemRawAt` and `PageReplaceItemRaw`'s grow branch now
              allocate `maxAlign8(len(raw))` — the helper the heap path has
              used since M0106-0010, so the two halves of the page layer
              finally agree. The replace path's in-place branch deliberately
              does not reuse an item's own padding (a pre-slice page has none;
              growing into it clobbers the neighbour). The budget moved with
              the writer or root-0040 re-opens inverted:
              `indexFormat.itemEncodedSize` charges `MaxAlign(bodySize)`, and
              `pageHighKeyFootprint` + the bulk loader's raw-append check
              match it. Guard `TestRawItemPlacementIsMaxAligned` pins BOTH
              halves — aligned `lp_off`/`pd_upper`, UNROUNDED `lp_len`, no
              clobbered neighbour — because pinning alignment alone would let
              a later cleanup round `lp_len` and corrupt every blob key.
              Gates: btree/storage/amcheck + units PASS; pgbench smoke PASS.
        - [x] **3b-3b — the tuple-INTERNAL MAXALIGNs** (2026-08-10). NOT
              REINDEX-required (an unrounded posting still parses; see below).
              **The filed blocker asked the wrong question.** Both MAXALIGNs
              this item named were already honoured the moment the format
              split landed, because each applies to the format that can
              express it: `index_form_tuple`'s size rounding is
              `FormPGIndexTuple`'s `size = MaxAlign(hoff + dataSize)`
              (`pgtuple.go`), and `BTreeTupleSetPosting`'s posting offset is
              `indexFormat.postingOffsetFor`'s `MaxAlign(len(key))`
              (`posting.go`). "Blocked until every index is descriptor-bearing"
              was true only of applying them to a BLOB key, which is not what
              upstream does — and never will be reachable, since an expression
              key, an explicit opclass, a non-bytewise collation or a
              non-PG-faithful stored image all resolve to the blob format
              permanently (`buildPGIndexKeyDesc`). Ledger row.
              What WAS missing is a third MAXALIGN the item did not name:
              `_bt_form_posting`'s **total** — `newsize = MAXALIGN(keysize +
              nhtids * sizeof(ItemPointerData))`, with `Assert(newsize ==
              MAXALIGN(newsize))` behind it (nbtdedup.c). A six-byte TID array
              leaves the tuple unaligned even when its key material is not, so
              goopg's exact `postingOffset + n*6` diverged from upstream on
              every posting it wrote. That divergence is only REACHABLE with a
              real PG in the picture, which is exactly what M0130 is about:
              after a failover the promoted PG writes MAXALIGNed postings into
              these indexes, and goopg's `postingBounds` rejected the padding
              outright (`postingOffset+n*6 != size`) — a clean parse failure on
              every deduplicated leaf entry PG had touched. Now
              `indexFormat.postingSizeFor` rounds (tuple format only — the blob
              posting offset is unaligned by construction, so rounding its
              total would rewrite on-disk bytes for no upstream property), and
              `postingBounds` tolerates a tail of at most seven bytes while
              still rejecting a full unexplained MAXALIGN unit and an array
              that runs past the declared size. Old unrounded postings keep
              parsing, which is why no REINDEX is needed. Guards
              `TestPostingBoundsToleratesAlignmentPaddingOnly` /
              `TestPostingBlobFormatSizeStaysExact` +
              `TestPostingTupleFormatLayoutAndRoundTrip` (rounded total).
              Gates: btree/amcheck/storage + units PASS; tpch-spotcheck PASS
              (Q12=2, Q13=35); pgbench smoke PASS.
        - [x] **3b-3c — `_bt_truncate` suffix truncation** (2026-08-10). NOT
              REINDEX-required (an untruncated separator is still a legal one).
              `indexFormat.truncateSeparator`
              (`internal/access/btree/pgtruncate.go`) keeps only the attributes
              that distinguish `lastleft` from `firstright` (`_bt_keep_natts` +
              `index_truncate_tuple`), applied at every separator producer: the
              split path and both bulk-loader levels, LEAF levels only — an
              internal separator is already a truncated pivot, which upstream
              copies verbatim. The split path also stopped re-deriving the
              parent downlink from `rightItems[0]`: `_bt_insert_parent` builds
              it from the left page's high key, and a parent stating the
              untruncated key routes descents to a boundary the level below no
              longer draws. The correctness half is `_bt_truncate`'s second
              branch: when every key attribute is equal the separator keeps
              `lastleft`'s heap TID (`BT_PIVOT_HEAP_TID_ATTR`); without it a
              TID-less pivot is minus infinity in the implicit last key
              attribute, so every left-page entry sharing that key compared
              GREATER than its own page's high key — mutation-checked, a point
              descent for the first of 1500 duplicates returned the WRONG heap
              TID. `indexFormat.marshal` now preserves the flag when it
              re-stamps a pivot's natts. Guard
              `internal/access/btree/pgtruncate_test.go`. Gates: btree/amcheck/
              storage + units PASS; tpch-spotcheck PASS (Q12=2, Q13=35);
              pgbench smoke PASS.
        - [x] **3b-3d — `_bt_check_third_page` replaces `MaxHighKeyLen`**
              (2026-08-10). NOT REINDEX-required (nothing on disk changes
              shape). goopg bounded the wrong object: `MaxHighKeyLen = 256`
              capped the SEPARATOR and nothing capped a leaf row, so an
              over-wide index row was admitted and only failed later, at the
              split that had to turn it into a high key. `CheckPGBTThirdPage`
              (`pgtuple.go`) is upstream's rule — bound the ROW at a third of a
              page — run where upstream runs it: `BTree.Insert` (`_bt_doinsert`;
              the one door `tryInsertNoSplit` and `insertIntoBlock` share) and
              both bulk-loader levels (`_bt_buildadd`). The split path's old
              length test becomes the same check on the resulting pivot at the
              INTERNAL bound: a leaf row is charged `BTMaxItemSize` because
              3b-3c's `_bt_truncate` may append a tiebreaker heap TID to a
              separator derived from it, and the level above is charged the
              8-byte-looser `BTMaxItemSizeNoHeapTid` so it can accept that grown
              pivot. `bulkHighKeyReserve`'s worst-case 268 bytes per page became
              an EXACT reserve — goopg's loader holds the whole sorted run, so
              it forms the next boundary's separator (`separatorAt(i+1)`,
              carried in `pendingSep`) and reserves its body, which upstream's
              streaming `_bt_buildadd` cannot do and pays for by moving the last
              tuple to the new page instead. Guard
              `internal/access/btree/pgthirdpage_test.go`. 1 ledger row: posting
              tuples are exempt from the gate because goopg's dedup has no
              `_bt_dedup_pass` `maxpostingsize` cap. Gates: btree/amcheck/
              storage + units PASS; tpch-spotcheck PASS (Q12=2, Q13=35);
              pgbench smoke PASS.
- [x] **M0130-S11.5 — `RM_BTREE` WAL** (est ~4 loops). PG-faithful
      `XLOG_BTREE_*` emit/replay per `nbtxlog.c`, so a PG standby can replay
      goopg index maintenance and not merely read a basebackup snapshot.
      Design: `docs/design/0130-0012-rm-btree-wal-content-parity.md`.
      **The gap is narrower than "the records are goopg-native": the ENVELOPE is
      already PG's** — `rmgr_map.go` maps every btree kind onto `RM_BTREE_ID`
      with the right `nbtxlog.h` opcode and `pg_assembled_emit.go` frames them
      through `assembleXLogRecord`. What differs is the CONTENT: only
      `INSERT_LEAF` carried the struct upstream declares; split / vacuum /
      newroot carried NO main data and shipped full-page images. That is not
      merely less faithful — PG runs the rmgr redo function whether or not a
      backup image is present, so e.g. `btree_xlog_newroot`'s unconditional
      `XLogRecGetData` cast makes an FPI-only record UNREPLAYABLE by the engine
      it is shaped for. They worked only because goopg's own replay had a
      default arm that restored the images. One record per slice:
  - [x] **S11.5a — `XLOG_BTREE_NEWROOT`** (2026-08-10). NOT REINDEX-required
        (nothing on disk changes shape). `EncodeBtreeNewRootPG` now emits
        upstream `_bt_newroot`'s record: main data
        `xl_btree_newroot{rootblk, level}`, block 0 the root WILL_INIT with its
        item area as `_bt_restore_page` block data, block 1 the left child
        (redo clears its incomplete-split flag), block 2 the metapage WILL_INIT
        with a 28-byte `xl_btree_metadata`. `level` and the items come from the
        root page and the metadata from the metapage, so the caller cannot
        describe the record inconsistently with the pages it logs — the only
        new hook parameter is `leftChildBlk`, and block 1 is MANDATORY at
        level > 0 because `XLogReadBufferForRedo` PANICs on an unregistered
        block id rather than reporting it, so the encoder refuses that
        combination. `internal/access/btree/pgnewroot.go` holds
        `_bt_restore_page`'s producer AND consumer in one file: the payload is
        an UNTAGGED run of MAXALIGNed index tuples in DESCENDING offset order,
        framed only by each tuple's own `t_info` size, so a producer/consumer
        disagreement mis-BUILDS the page rather than failing to parse. It also
        builds the payload from the line pointers instead of slicing
        `[pd_upper, pd_special)` as upstream does — goopg inserts at a computed
        physical offset (`PageInsertItemRawAt`), which shifts line pointers
        while leaving the data area in allocation order. All of it format-free,
        for `ApplyInsertRecordAt`'s reason: recovery holds a relfilenode and no
        catalog to resolve a key descriptor from. Metapage replay is
        `_bt_restore_meta` (rebuild, not read-modify-write, `pd_lower` advanced
        past the struct); `ReplayMetaSetRoot` survives only for the
        goopg-native record, which carries just `(root, level)`. VACUUM's
        `resetToEmptyRoot` (an empty LEAF root) has no upstream `_bt_newroot`
        counterpart but is exactly what upstream's REDO handles at level 0 — no
        item restore, no block 1 — which is why goopg can log it as a newroot.
        Guards: `internal/wal/btree_newroot_pg_test.go` (shape incl. an
        explicit "no block carries an FPI" assertion, the missing-child
        refusal, a replay reproduction at matching OFFSETS, same-LSN
        idempotency, the leaf-root variant) and
        `internal/access/btree/pgnewroot_test.go`; mutation-checked (ascending
        item order, dropped block-1 limb). Gates: btree/wal/storage/amcheck/
        initdb + units PASS; pgbench smoke PASS (commit hook). 1 ledger row.
  - [x] **S11.5b-1 — `XLOG_BTREE_SPLIT_R`** (2026-08-10). NOT REINDEX-required.
        `EncodeBtreeSplitPG` now carries upstream's `xl_btree_split{level,
        firstrightoff, newitemoff, postingoff}` — the struct `btree_xlog_split`
        casts before it looks at any block, and which the previous image-only
        form omitted entirely. Block 1 (the new right sibling) is now CONTENT,
        not an image, because redo rebuilds that page from scratch on every
        replay (`XLogInitBufferForRedo` + `_bt_pageinit` + `_bt_restore_page`,
        return code ignored) and would overwrite a restored image with an empty
        item area — the mirror of S11.5a's missing-main-data trap. Block 2 (the
        relinked old sibling) carries nothing: redo derives its new back-link
        from block 1's tag. The right page's OPAQUE is not carried either — redo
        builds it from `xlrec->level` and the record's own block tags — so
        `btree.SplitRightPageOpaque` is the single definition, `splitPage` now
        stamps it (dropping the stale-from-birth `BTP_HAS_GARBAGE` inheritance,
        as upstream `_bt_split` does), and the encoder REFUSES a right page that
        does not match, since the divergence would otherwise be silent. Block 0
        (the left half) stays a full-page image, which is upstream-LEGAL rather
        than a shortcut: PG's redo reaches its incremental left-half rebuild only
        under `BLK_NEEDS_REDO`, and an image takes `BLK_RESTORED` and skips it
        along with all three offsets — upstream's own comment says so. Replay:
        `replayDecodedXLogBtreeSplit` (upstream's block order, per-limb pd_lsn
        idempotency); a block 0 with no image is a real-PG record and returns an
        explicit "not implemented" rather than leaving the left half pre-split.
        Guards: `internal/wal/btree_split_pg_test.go` (shape incl. the "block 1
        is not an image" assertion, the rightmost no-block-2 variant, three
        right-opaque mutations each rejected by name, a replay reproduction at
        matching OFFSETS + same-LSN idempotency). Obsolete
        `TestEncodeBtreeSplitPGFPIReplay` deleted (pinned the removed property).
        Gates: btree/wal/storage/amcheck/initdb + units PASS; pgbench smoke PASS
        (commit hook). 2 ledger rows.
  - [x] **S11.5b-2 — the split record's INCREMENTAL left half** (2026-08-10).
        NOT REINDEX-required. Block 0 is now upstream's block-0 data — the new
        item when it landed on the left, then the page's new high key — so a
        split record is two tuples instead of a page. S11.5b-1 filed this as
        BLOCKED on goopg's split not being upstream's (`splitPage` appends the
        new item, runs `dedupConsolidate` over the merged list and refills BOTH
        halves, so the left half can hold posting tuples that were never on the
        original page) and concluded it needed the dedup unbundled into its own
        record first. That read the requirement one level too strong: the three
        offsets do not have to describe every split goopg CAN perform, only the
        one in hand, and whether they do is a question about pages the encoder
        already holds. `btree.DescribeSplitLeft` reconciles the two halves' data
        items against the pre-split page's plus the new item — the single splice
        position IS `newitemoff`, the halves' boundary IS `firstrightoff`, the
        side it landed on selects `_SPLIT_L` over `_SPLIT_R` — and
        `btree.CheckSplitLeft` replays that description against a COPY of the
        pre-split page, comparing items, high key and opaque with what the
        primary wrote. Only a clean reproduction goes out incrementally;
        S11.5c's `CheckVacuumDelete` discipline, so the dedup pass, a dropped
        LP_DEAD item and the ROOT-flag disagreement (upstream's `_bt_split`
        clears BTP_ROOT on the left half, goopg's `clearRootFlag` runs a step
        later) are CAUGHT rather than enumerated. The primary pays one page copy
        per split — taken immediately before `resetPageItems`, only when a WAL
        hook is wired — to stop paying a page per split in the stream.
        `LogBtreeSplitFunc`/`LogSplitFunc` grew `prePage` + `newItem`, both nil
        meaning "log the image" (the bulk / pre-runtime callers). Replay took
        upstream's left arm (`btree.ReplaySplitLeftPage`), reading
        `newitemonleft` from the record's INFO byte because the block-data tuple
        run is untagged, and refusing `postingoff != 0` — a record only a real
        PG primary produces — rather than replaying it without `_bt_swap_posting`.
        Guards: `internal/access/btree/pgsplitleft_test.go` (offset derivation
        incl. upstream's "newitem goes at the end" arm, framing mutations, four
        refusals) with `TestRealTreeSplitsAreDescribable` as the PREMISE test —
        3000 inserts through a real tree, every non-root split must describe AND
        reproduce, without which the encoder could fall back to an image on
        every split and every other guard would still pass — and
        `internal/wal/btree_split_left_pg_test.go` (record shape, the
        `_SPLIT_L`/`_SPLIT_R` opcode, three image fallbacks, a replay
        reproduction at matching OFFSETS + same-LSN idempotency, which matters
        more here than under an image since the arm reads the page it rewrites).
        Gates: btree/wal/storage/amcheck/initdb + units PASS; tpch-spotcheck
        PASS (Q12=2, Q13=35); pgbench smoke PASS. 3 ledger rows.
  - [x] **S11.5b-3 — block 3 on an INTERNAL split** (2026-08-10). NOT
        REINDEX-required. An internal page is never inserted into for its own
        sake — the only thing that lands on one is a separator pushed up by a
        split ONE LEVEL DOWN, whose page is still flagged BTIncompleteSplit at
        that moment. Upstream clears that flag inside `_bt_split`'s own critical
        section (`cpageop->btpo_flags &= ~BTP_INCOMPLETE_SPLIT`) and registers
        the child as backup block 3 under `if (!isleaf)`; `btree_xlog_split`
        does the mirror image FIRST, before it locks anything else. goopg had
        neither half: the flag clear was a separate page record run by the
        CALLER after the parent insert returned, and the split record named no
        child — which is not "less detailed" but fatal, since
        `XLogReadBufferForRedo` PANICs on an unregistered block id rather than
        reporting it (S11.5a's block-1 trap again). Now: `insertIntoBlock`
        carries upstream's `cbuf` as `childBlk`, threaded from the three places
        a separator is pushed up (`splitPage`'s recursion, `finishSplit`,
        `createNewRoot`'s lost-the-race fallback) and InvalidBlockNumber for a
        leaf tuple, the only other way in; the split path pins the child while
        still holding this level's latches (the DESCENT direction, so it cannot
        deadlock against a reader), clears the flag, logs block 3 with no data
        (redo re-derives the mutation from the page) and stamps the record LSN;
        `clearIncompleteSplit` returns without writing when the flag is already
        clear, so the caller's call is a no-op after a cascading parent split
        and still does the work after a no-split parent insert.
        `EncodeBtreeSplitPG` refuses BOTH violations of upstream's `!isleaf`
        condition — a level > 0 record with no child, and a leaf record carrying
        one (a block PG's redo never reads). Guards: block-3 shape / both
        level-gate refusals / a replay reproduction asserting the child comes
        out unflagged at the record's LSN
        (`internal/wal/btree_split_pg_test.go`), plus the writer side in
        `internal/access/btree/btree_test.go` — 4000 wide-key inserts drive a
        real internal split, and an internal split NOT occurring is a failure
        rather than a skip, so the assertion cannot pass vacuously. Gates:
        btree/wal/storage/amcheck/initdb + units PASS, btree -race PASS; pgbench
        smoke PASS (commit hook). 1 ledger row (the no-split parent insert's
        half of the same clear — upstream's XLOG_BTREE_INSERT_UPPER block 1).
  - [x] **S11.5c — `XLOG_BTREE_VACUUM`** (2026-08-10). NOT REINDEX-required.
        Both forms now carry upstream's `xl_btree_vacuum{ndeleted, nupdated}`;
        the incremental one adds the deleted offset numbers as block-0 data with
        NO image. This was the one FPI-only record that was not outright
        unreplayable — `btree_xlog_vacuum` dereferences `xlrec` only inside its
        `BLK_NEEDS_REDO` arm, which an applied image skips — but it still lied
        to every reader that does not replay it, starting with `pg_waldump`'s
        `btree_desc` printing ndeleted/nupdated off the end of a zero-length
        main-data area. Which form is emitted is decided by ASKING THE TWO PAGES
        (`btree.CheckVacuumDelete` replays the offsets against the pre-vacuum
        page and compares items, high key and opaque with what VACUUM wrote),
        not by enumerating cases at the emit site: goopg's VACUUM refills the
        page from a parsed item list, so it coincides with `PageIndexMultiDelete`
        except when the page carries POSTING LISTS (expanded per TID, survivors
        re-marshalled as separate tuples — upstream instead rewrites the tuple in
        place via `xl_btree_update`, the `nupdated` half goopg never emits), when
        the page went EMPTY (VACUUM also stamps `BTDeleted|BTHalfDead`, which no
        vacuum redo sets), or when the record is reused for the dedup-recovery
        CONSOLIDATION. Mismatch ⇒ full-page image, which is upstream-legal for
        block 0's reason (`BLK_RESTORED` skips the deletion). New
        `internal/access/btree/pgvacuum.go`: ReplayVacuumDelete (upstream's
        PageIndexMultiDelete + the unconditional garbage-hint clear; it rebuilds
        the item area, so a surviving LP_DEAD mark is lost — an unlogged hint),
        CheckVacuumDelete. Replay `replayDecodedXLogBtreeVacuum` refuses
        nupdated > 0 rather than dropping the updates. Guards:
        `internal/wal/btree_vacuum_pg_test.go`,
        `internal/access/btree/pgvacuum_test.go`, and the capture hook in
        `btree_vacuum_wal_test.go` now runs CheckVacuumDelete on the offsets
        VacuumIndexPages itself computes and fails if no emission named any.
        Gates: btree/wal/storage/amcheck/initdb + units PASS; pgbench smoke PASS
        (commit hook). 1 ledger row.
  - [x] **S11.5d-1 — `XLOG_BTREE_MARK_PAGE_HALFDEAD`** (2026-08-10). NOT
        REINDEX-required. Page deletion is TWO records upstream; goopg's single
        native `RecordKindBtreeUnlinkPage` covers the union of both. This slice
        lands phase 1's PG form. The record goopg already tagged
        `RM_BTREE`/`0xB0` carried 16 bytes of `{leafblk, flagsAfter}` and NO
        registered blocks — and upstream's `btree_xlog_mark_page_halfdead` calls
        `XLogInitBufferForRedo(record, 0)` unconditionally, so an unregistered
        block id PANICs the standby (S11.5b-3's shape again: fatal, not lossy).
        It also has zero live emit sites — the `LogBtreeMarkPageHalfDead` hook
        is wired end-to-end and never called, because VACUUM bundles the
        half-dead transition into the vacuum record's opaque trailer.
        Now: `EncodeBtreeMarkPageHalfDeadPG` emits the 20-byte
        `xl_btree_mark_page_halfdead{poffset, leafblk, leftblk, rightblk,
        topparent}` (the 2 alignment bytes after `poffset` are wire format),
        block 0 the leaf WILL_INIT with no data, block 1 the subtree parent;
        `leftblk`/`rightblk` are read off the page being logged, not accepted
        from the caller. Two things upstream's redo DEFINES that goopg's page
        model lacked: (a) a half-dead page IS its contents — one dummy
        `SizeOfIndexTupleData` high key whose `t_tid` block half is the subtree
        top parent, which `_bt_unlink_halfdead_page` reads to find the next page
        down, so phase 2 is impossible without it; (b) the parent mutation is a
        RETARGET (point `poffset` at the right neighbour's child, delete the
        neighbour) not a delete-at-`poffset`, so the deleted key range is
        absorbed RIGHTWARD, the opposite direction from goopg's
        `ReplayRemoveParentDownlink`. `ReplayHalfDeadParent` /
        `ReplayMarkHalfDeadLeaf` implement upstream's; goopg's own stays put
        until S11.5d-3 switches the primary's mutation with it (ledger row).
        Guards: `internal/wal/btree_halfdead_pg_test.go` (+3) and
        `internal/access/btree/replay_halfdead_test.go` (+3, both
        `P_FIRSTDATAKEY` shapes so the PHYSICAL offset cannot regress into a
        data-slot index).
  - [x] **S11.5d-2 — `XLOG_BTREE_UNLINK_PAGE`** (2026-08-10). Phase 2, the
        other half of the pair S11.5d-1 opened; same trap in the same shape
        (goopg's native 41-byte `RecordKindBtreeUnlinkPage` is framed under this
        record's `RM_BTREE`/`0x80` header, so a standby casts it to
        `xl_btree_unlink_page` and then PANICs in
        `XLogInitBufferForRedo(record, 0)`). Now: `EncodeBtreeUnlinkPagePG`
        emits the 36-byte `xl_btree_unlink_page{leftsib, rightsib, level,
        safexid, leafleftsib, leafrightsib, leaftopparent}` — both alignment
        holes are wire format, and the size is `offsetof(leaftopparent) +
        sizeof(BlockNumber)` = 36, NOT `sizeof(struct)` = 40 — with block 0 the
        target `WILL_INIT` no data, block 1 the left sibling only when there is
        one, block 2 the right sibling UNCONDITIONALLY (so a rightmost target is
        refused: upstream's redo reads block 2 without testing `rightsib`, which
        is consistent because `_bt_pagedel` never deletes a rightmost page),
        block 3 the half-dead leaf `WILL_INIT` for an internal target, block 4
        the metapage on the `_META` (`0x90`) variant. Every structural field is
        read off a page, S11.5a's discipline; `leaftopparent` comes out of the
        leaf's DUMMY HIGH KEY — the tuple S11.5d-1 discovered a half-dead page
        is defined by — and block 3 rebuilds it with the same
        `ReplayMarkHalfDeadLeaf` phase 1 uses, which is exactly what makes the
        two records compose over a subtree of arbitrary depth. New
        `internal/access/btree/pgpagedel.go`: `ReplayUnlinkTargetPage`
        (`_bt_pageinit` + `BTPageSetDeleted` — a deleted page is ALSO defined by
        its contents: no line pointers, `pd_lower` covering one
        `BTDeletedPageData`, `pd_upper` closed against `pd_special`),
        `PGDeletedPageSafeXid`, and PG-level `ReplayUnlinkLeftSibling` /
        `ReplayUnlinkRightSibling` — the legacy `ReplaySetSibling*` round-trip
        the opaque through the `BT*` flag word, which has no counterpart for
        `BTP_HAS_FULLXID` and silently drops it, turning the safexid into
        garbage for `BTPageIsRecyclable`. `replayDecodedXLogBtreeUnlinkPage` +
        dispatch arm apply blocks 1/0/2/3/4 in upstream's LOCK order, each limb
        pd_lsn-idempotent with an FPI fallback. Guards:
        `internal/wal/btree_unlinkpage_pg_test.go` (+4),
        `internal/access/btree/replay_pagedel_test.go` (+3).
  - [x] **S11.5d-3a — the primary adopts the retarget-and-delete parent
        mutation** (2026-08-10). The half S11.5d-1 filed as a ledger row: goopg's
        primary deleted the target's downlink outright, absorbing the deleted key
        range LEFTWARD, where upstream retargets that item at the RIGHT
        neighbour's child and deletes the neighbour's item. Same input, different
        parent page — so no PG-shaped mark-halfdead record could be emitted.
        Three call sites did the old mutation (`applyParentDownlinkRemoval`, the
        FPI-path `removeDownlinkFromParent`, and redo's parent limb); all three
        now route through ONE shared `ReplayParentRetargetByChild`, which is the
        point rather than tidying — the tree a vacuum produces must not depend on
        whether a WAL emitter happened to be wired, and this is a sibling set
        that must not be able to drift. The lookup is by CHILD BLOCK identity;
        redo now ignores the record's `ParentRemoveSlot` entirely, since that
        value was always advisory (the primary re-located by identity from
        M0122-0010 on) and an advisory field is exactly what a PG-shaped record
        may not carry. With it comes upstream's one structural refusal
        (`ErrParentRightmostChild`, `_bt_lock_subtree_parent`): a page whose
        downlink is its parent's LAST item cannot be deleted, tested BEFORE any
        mutation and before the emitter branch so WAL and FPI refuse identically
        and no half-relinked page is ever left behind; on the leaf path the
        refusal reverts VACUUM's phase-1 marking (`abandonHalfDeadLeaf`), because
        a leaf left flagged while its parent still points at it is invisible to
        `liveSibling` and eligible for `recycleBlock` — a block handed to an
        unrelated split while still reachable. The refusal RETIRES a mechanism:
        an internal page can no longer reach zero items through a downlink
        removal at all (its last item IS its rightmost child), so
        AI-20260706-201855-001's "empty non-root internal page" is structurally
        unreachable rather than repaired after the fact by
        `maybeCascadeEmptyInternal`, and that regression test now asserts the
        stronger invariant directly. Guards:
        `internal/access/btree/parent_retarget_test.go` (+4, incl. an end-to-end
        VacuumIndexPages run proving the parent's last child is left
        empty-but-live, unflagged, still downlinked, tree readable and writable).
        Gates: btree/amcheck/wal/storage/initdb PASS; units PASS; btree -race
        PASS; pgbench smoke PASS (commit hook). 2 ledger rows.
  - [x] **S11.5d-3b — rewire the page-deletion emit PROTOCOL** (landed
        2026-08-10). goopg walked the sibling chain UNLATCHED, emitted the unlink
        record with what it found, then RE-DERIVED each link again under the
        write pin (the AI-20260709-010336-082 corruption fix — a split on another
        connection's `*BTree` can splice a live page in, and stamping the stale
        walk stomped that split's relink). The write was right and the RECORD was
        wrong: every link field was advisory, and `btree_xlog_unlink_page`
        rebuilds the sibling pages FROM the record with nothing to re-derive
        from. `acquireUnlinkPins` now holds parent → left → target → right across
        compute/emit/write. Left→right is `_bt_split`'s own direction; the parent
        going FIRST is goopg-specific — since S11.5b-3 an internal split pins a
        lower-level page while holding this level's latches, so goopg latches
        top-down where upstream latches bottom-up. Re-derivation is replaced by
        VALIDATION (re-read the two ends and the target under the latches, retry
        the walk if a link moved — the same self-correction, moved to before the
        record instead of after it), with a bounded give-up
        (`errUnlinkChainUnstable`) that abandons the deletion exactly as the
        rightmost-child refusal does, and an outright refusal for a dead run that
        loops back onto the target (latching one block twice would deadlock the
        goroutine against itself). The rightmost-child refusal now reads the
        LATCHED parent. Dead helpers removed (`applyOpaqueMutation`,
        `applyParentDownlinkRemoval`, the two `read*FlagsAfterUnlink`). Guards:
        `internal/access/btree/unlink_protocol_test.go` (+2 — latch-held-at-emit
        asserted from inside the emitter hook via the new `Slot.TryRLock`, plus
        record-equals-page for every logged link/flag; and the cyclic-dead-run
        refusal). Gates: btree/wal/storage/initdb/amcheck PASS; btree -race PASS;
        units PASS; pgbench smoke PASS (commit hook). 2 ledger rows.
  - [x] **S11.5d-3b-2 — swap in the two PG records** (landed 2026-08-10).
        `unlinkEmptyLeaf` now emits upstream's PAIR inside the S11.5d-3b latch
        section — `EncodeBtreeMarkPageHalfDeadPG` then `EncodeBtreeUnlinkPagePG`
        — retiring the native `RecordKindBtreeUnlinkPage` producer and with it
        the last "documented reason to stay native"
        (`wal-pg-identical-stream/IMPLEMENTATION-TODO.md` A8-unlinkpage). The
        primary writes what redo writes, through the redo functions themselves:
        phase 1 REBUILDS the leaf as a half-dead page carrying the dummy
        top-parent high key (`ReplayMarkHalfDeadLeaf`; goopg deletes one page at
        a time, so the link is InvalidBlockNumber) and retargets the parent
        (`ReplayParentRetargetByChild`), phase 2 REBUILDS the target as an empty
        `BTP_DELETED|BTP_HAS_FULLXID` page (`ReplayUnlinkTargetPage`) and
        relinks the siblings (`ReplayUnlink{Left,Right}Sibling`). That sharing
        matters more here than elsewhere: both records are WILL_INIT with NO
        block data, so the standby rebuilds these pages from 20 and 36 bytes of
        main data alone and any field the primary computed its own way would
        diverge with nothing in the record to catch it. One indirection was
        needed — the phase-2 encoder reads leftsib/rightsib/level off the target
        PAGE, but goopg relinks the nearest LIVE siblings (a vacuum marks a whole
        adjacent run dead before unlinking any of it), so the emit site builds
        the POST-mutation image with `ReplayUnlinkTargetPage` first, encodes
        from it, and copies those same bytes onto the pinned page. Three shapes
        upstream never produces are refused instead of logged unreplayable (no
        parent downlink; a rightmost target — redo reads block 2
        unconditionally; a STANDALONE internal page — upstream only deletes
        leaf-rooted subtrees, and S11.5d-3a had already made goopg's cascade
        unreachable), each leaving the page empty-but-live. `ParentRemoveSlot`
        is gone from the storage request. Guards:
        `internal/wal/btree_pagedel_producer_test.go` (NEW — runs a real
        VacuumIndexPages through the real encoders and decodes the output, the
        one thing the encoders' hand-built unit tests cannot do) plus the
        extended `unlink_protocol_test.go` (latch check over BOTH records,
        phases paired one-for-one, whole target opaque compared against the
        logged image). Gates: btree/wal/storage/initdb/amcheck/vacuum PASS;
        btree -race PASS; units PASS; pgbench smoke PASS (commit hook). 2 ledger
        rows (torn phase-1/phase-2 deletion is now possible and goopg cannot
        resume it; no multi-level subtree deletion).
  - [x] **S11.5d-3c — the safexid recycle horizon** (landed 2026-08-10).
        `unlinkEmptyLeaf` stamps a real safexid — read where upstream reads it
        (`_bt_unlink_halfdead_page`: `safexid = ReadNextFullTransactionId()`)
        from the new `storage.Pool.SetBtreeRecycleHorizon` hook, wired in
        `initdb.Open` off `mvcc.Manager.NextXID`/`OldestXmin` — into both the
        record and the page image, so `BTDeletedPageData` finally has a runtime
        producer. The allocation side is the half that makes it mean anything:
        a free-list block is now a CANDIDATE, and `popRecyclableBlock` pins it,
        tests the new `btree.PGPageIsRecyclable` (upstream `BTPageIsRecyclable`,
        nbtree.h:290-318) and on failure puts it **back**, extending the
        relation instead — `_bt_allocbuf`'s shape. What this prevents is not
        wasted space: goopg used to hand the block to the very next split, so a
        scan that had already read the downlink landed on a page filled with
        another key range's tuples. Two deliberate divergences, both ledgered:
        epoch-0 widened 32-bit XIDs compared with unsigned `<` (no wraparound-
        safe `FullTransactionIdPrecedes`), and no pending-FSM — the free list is
        per-`BTree` and in-memory, so a block still tombstoned at shutdown leaks
        rather than being rediscovered. With no horizon source wired (bare-pool
        unit tests; the legacy non-WAL deletion paths, which stamp no safexid)
        the pre-slice behaviour stands unchanged. Guards:
        `internal/access/btree/recycle_horizon_test.go` (predicate over the
        three page shapes; allocator refuses-and-restores then takes the block
        once the horizon moves; ungated fallback; and a real `VacuumIndexPages`
        putting the horizon's value in record AND page — the one thing the
        redo-side tests cannot see, since they read `0` as "recyclable now").
        Doc: `docs/design/0130-0012-rm-btree-wal-content-parity.md` §S11.5d-3c.
- [x] **M0130-S11.6 — unblock S10 blocker #10** (landed 2026-08-10).
      `pg_class.relhasindex` for a user table is no longer hardcoded false, so a
      real PG 18.3 on a goopg cluster finally calls `RelationGetIndexList` /
      `ExecOpenIndices` instead of silently planning seq scans and silently
      skipping index maintenance for its own post-failover INSERTs. It is NOT
      `len(cat.IndexesOnTable(tbl)) > 0`: since the S11.4 flip the key format is
      a per-INDEX property with nothing on disk recording it, and a blob-format
      tree is a structurally VALID nbtree (`_bt_checkpage` accepts it) whose keys
      PG would order with the wrong comparator — wrong rows, inserts filed where
      goopg's descent never looks. `relhasindex` is per-RELATION and
      `RelationGetIndexList` reads every pg_index row once set, so
      `pgClassRelhasindex` is all-or-nothing: true only when EVERY index on the
      table is descriptor-bearing. The second half is that it has to be
      RE-derived: goopg writes the table's pg_class row at CREATE TABLE, when no
      index exists, so `resyncTableClassHeapRowForIndexSet` runs from
      `syncIndexToCatalogHeap` (upstream `index_create` → `index_update_stats` →
      `heap_inplace_update`) — in BOTH directions, since adding an undescribable
      index must take the flag back off. **Discovery: `pg_index.indcollation` was
      InvalidOid for every implicit collation** (goopg wrote it only for an
      explicit `COLLATE`), which makes PG fail EVERY scan and EVERY insert on a
      collatable index with `42P22: could not determine which collation to use`
      — invisible until PG actually opened one. `IndexKeyColumnCollationOID` now
      supplies the column's own collation (upstream `ComputeIndexAttrs`), and the
      restart reload compares against it before calling a decoded OID explicit
      (upstream's `pg_get_indexdef_worker` rule), else a restart would invent a
      `COLLATE "default"`. **`TestE2E_PGStandbyFullCycle` PASSES end to end** —
      blockers #10 and #12 closed, AI-20260810-011258-003 with them — and now
      asserts the three facts that failed independently on the way here:
      relhasindex set on the promoted PG, a forced-index lookup finding the row
      PG itself inserted, and the same lookup finding a row goopg wrote before
      the failover. The `pg_amcheck`-over-a-goopg-index standing gate already
      existed (`TestPort_PgAmcheckBtreeIndexCheck`,
      `TestPort_PgAmcheckAllTables`) and still passes. Guards:
      `internal/executor/pgindex_relhasindex_test.go` (6 cases incl. mixed and
      gate-off). Doc: `docs/design/0130-0011-nbtree-pg-on-disk-format.md` §S11.6.
      2 ledger rows (expression-key indcollation; DROP INDEX leaves the flag).

## M0132 — Explicit transactions across the extended query protocol (filed 2026-08-12, user directive)

**Priority: PROMOTED (user directive 2026-08-13) — next after M-NIGHTLY.** The `## Current Priority` banner at the top of this file names M0132 first after M-NIGHTLY's standing filing obligation; work M0132 tasks immediately after M-NIGHTLY's regression fixes, ahead of M0131 and M0130's remaining items.

Milestone: `docs/milestones/0132-extended-protocol-explicit-transactions.md`. Authoritative decomposition: `docs/design/0132-extended-protocol-explicit-transactions.md`. Core design: `docs/design/0132-0001-extended-protocol-explicit-txn-state-machine.md`. Supersedes (on landing) the `design`-status sketch `analysis/perf-optimize3-dash/08-improvement-designs/09-extended-protocol-explicit-txn.md` (2026-07-14) and discharges `.ralph/deferral_ledger.md:347`, the 2026-07-14 row deferring it because "a partial land would silently skip INITIALLY DEFERRED constraints".

**The bug, verified at HEAD `28de7c44` (2026-08-12).** goopg's extended query protocol (Parse/Bind/Execute) begins and commits its own transaction on **every** `Execute`, and treats the client's `BEGIN`/`COMMIT`/`ROLLBACK` as accepted-but-ignored command tags — `dispatch_extended.go:110-112` returns a bare `CommandTag` for a `*planner.Transaction` node, `:134-139` unconditionally begins a fresh READ COMMITTED transaction on an offset proc slot, `:361-365` unconditionally commits it. `connTx *connTxState` is already threaded into `executeExtendedQueryViaExecutor` (`:30`) but is read only for `NonSuperuserRole` (`:36`, `:46`, `:238`, `:250`) and statement logging (`:68`, `:71`); `connTx.Begin`/`InExplicit`/`Tx`/`End` have **zero** call sites in `dispatch_extended.go` or `extended.go`. The simple path models the block (`dispatch.go:227-229` reuses `connTx.Tx()`; `:2696-2984` is the 289-line verb arm, promotion at `:2701`).

**Why it is a correctness milestone, not a perf one.** `ROLLBACK` over the extended protocol **does not roll back** — a `BEGIN` → `UPDATE` → `UPDATE` → `ROLLBACK` sequence has already durably committed both updates when the `ROLLBACK` arrives, which returns a success tag. Atomicity across statements is lost (a failure at statement *k* leaves `1..k-1` committed); `ReadyForQuery` reports `I` where PG reports `T` (the byte comes from `connTxState.wireStatus`, `conn_tx.go:415`, installed once per connection at `server.go:1526` as `w.TxStatusFn` — an *observable protocol* divergence drivers gate on); and isolation levels above READ COMMITTED are unreachable (`:139` hardcodes RC; the comment at `:119-123` says SERIALIZABLE "cannot apply").

**The shape real drivers emit is MIXED, and worse.** pgx v5 (`conn.go:515`, "Always use simple protocol when there are no arguments") and lib/pq (`conn.go:901`) send argument-less statements — including `BEGIN`/`COMMIT`/`ROLLBACK` — down the **simple** path while parameterised DML goes down the extended one. So the dominant real-world shape is a block opened on the simple path with its DML executed on the extended path, producing **two live transactions on one connection**: the block held on the connection's own slot (`dispatch.go:227-229`) while each `Execute` begins and commits its own on the offset slot. The client's writes land in the auto-committing one and the `ROLLBACK` discards the empty one. Consequences: S8 (mixed) is a **primary** slice, not an edge case; acceptance bar 2b covers the driver shape; and this is a better hypothesis for S7's `mvcc: unknown transaction` aborts than "the offset scheme collides" alone.

**CORRECTION to doc 09 that reshapes S4 — there is no `execCommit` route to adopt.** Doc 09 §2/§4.1 (and the first draft of this filing) said the extended COMMIT should route through `transactionOp.execCommit`. The simple path does not use it: `dispatch.go:2803-2807` states "the simple-query dispatch bypasses transactionOp.execCommit, so the checks queued on the session during INSERT/UPDATE/DELETE must be run here BEFORE TxnMgr.Commit". The deferred FK/UNIQUE/EXCLUDE passes are **inline** at `:2818-2828` (`RunDeferredFKChecks` under `FreshSnapshot`, then `RunDeferredUniqueChecks`, then `RunDeferredExclusionChecks`), the SSI pre-commit check inline after, and the arm returns at `:2980` without reaching the operator build at `:2985`. The bypass was never removed — 0119-0004 *added* the inline sequence. The extended COMMIT must inherit that sequence, which it gets **for free iff S2 is a genuine extraction**; S4 is therefore the proof obligation on S2, not independent wiring.

**LAND-TOGETHER RULE (non-negotiable): S2+S3+S4+S5 in ONE commit.** Two independent arguments, both "the half-landing opens a hole that does not exist today". (a) Without S4, a block whose COMMIT reaches `TxnMgr.Commit` by any route other than the extracted helper skips the deferred sequence and commits a block violating an `INITIALLY DEFERRED` constraint — today each statement commits individually so its constraints are checked individually. (b) Without S5, an error mid-block leaves the block open and un-failed — there is **no** `connTx.Fail()` call site on the extended path (its only two are `dispatch.go:950` and `:1019`) — so post-error `Execute`s keep running in the same live transaction and `COMMIT` commits them; PG aborts everything.

**Adjacent divergences recorded but NOT closed here (ledger rows required):** (i) **COPY ignores an open block on BOTH protocols** — `copy.go:157-167`: "The COPY always runs in its own auto-commit transaction regardless of whether the client has opened an explicit BEGIN block", using the same `(ProcNum + halfSize) % ConnSlotCount` offset that S7 exists to retire (`dispatch_extended.go:136` cites copy.go as its model), so `BEGIN; COPY …; ROLLBACK` does not roll back today, simple path included; (ii) **`SET LOCAL ROLE` semantics change silently under S2** — `SnapshotLocalRoleIfNeeded` (`conn_tx.go:390-396`) returns early when `!active`, so the extended path's calls (`dispatch_extended.go:252`, `extended.go:693`, `:712`) record no restore point and `End()` never reverts; setting `active` changes that with no slice claiming it.

- [x] **M0132-S1 — Verification + characterisation tests (no behaviour change)** (est ~1 loop). Confirm and record the three doc-09 corrections: (a) its S1 "thread `connTxState`" is **already done** (`dispatch_extended.go:30`), so the first *code* slice is the state machine; (b) there is **no `execCommit` route** (see the correction paragraph above); (c) `Sync` (`server.go:1761-1765`) is **already correct** — it clears `syncRequired` and writes `ReadyForQuery` without touching transaction state, exactly PG's behaviour for an explicit block, so the tempting "end the transaction at Sync" edit would silently re-break every block. Add the tests that must FAIL against today's HEAD and become the acceptance bar: block-spans-Executes, rollback-actually-rolls-back, **the mixed driver shape (bar 2b)**, status-byte, aborted-block. A milestone whose first slice cannot produce a red test has not reproduced the bug. Gates: `go test ./internal/server/`. **DONE 2026-08-13.** Bar landed as `internal/server/extended_txn_block_test.go` — 8 tests behind one const, `m0132ExtendedBlocksLanded = false`: each runs its scenario unconditionally and asserts either the PG outcome (const true) or today's divergence (const false), so the bar is committable ahead of the fix AND cannot survive it — when S2–S5 land the divergence arms fail and force the flip. Green at HEAD; 6 of 8 red when flipped (the two no-divergence guards stay green). Corrections (a)/(b)/(c) all confirmed by measurement; recorded in `docs/design/0132-0001-…` §7. **Two SIMPLE-path aborted-block gaps discovered and filed (2 ledger rows, both belong to S5's scope):** (i) a PLAN-time error does not fail the block — `connTx.Fail()` is only reached from the executor-error path, so `BEGIN; INSERT INTO <missing table>;` leaves the block live and `ReadyForQuery` reverts to `T`; the `E` right after the error is `wireStatus`'s `afterError` argument, not persisted state; (ii) a constant `SELECT 1` bypasses the 25P02 gate that correctly rejects table-touching statements. **S5 must close both** or it will copy the same placement.
- [x] **M0132-S2 — `BEGIN`/`COMMIT`/`ROLLBACK` drive the real block state machine** (est ~2 loops; design `0132-0001` §3 D1). Replace the short-circuit at `dispatch_extended.go:110-112` by **EXTRACTING** the simple path's verb arm (`dispatch.go:2696-2984`, 289 lines) into one `applyTransactionVerb(ctx, connTx, txNode, autoCommitPtr) (tag, err)` helper both dispatchers call. Extraction — not mirroring — is what makes S4 free: the arm covers the COMMIT-in-failed-block→ROLLBACK rule (`:2782-2800`), the inline deferred sequence (`:2818-2828`), the SSI pre-commit check, enum-DDL undo, the `Begin/EndLocalTransaction` hooks and the pending enum/composite/range-type queues, and a hand-written extended COMMIT would have to re-derive a sequence its author may not know exists. Two asymmetries the helper absorbs: the simple path *promotes* an already-begun transaction (`:2701`) while the extended path reaches the verb before beginning anything, so the transaction is an argument; and the simple arm writes its own wire frames, so the helper returns the tag and each caller writes it in its own protocol's shape. The refactor must be proven behaviour-preserving for the simple path BEFORE the new extended behaviour is switched on. Free consequence: `ReadyForQuery` starts reporting `T`/`I` (shared `w.TxStatusFn`) — but NOT `E`, which needs S5. Gates: `go test ./internal/server/ ./internal/executor/`, D-002 isolation. **Lands with S3+S4+S5.** **STEP 1 DONE 2026-08-13 (the extraction, simple path as its only caller — nothing observable changed, so the land-together rule is untouched; D1 itself requires the refactor be proven green BEFORE the extended behaviour is switched on).** `internal/server/txn_verb.go`: `(*Server).applyTransactionVerb(ctx, connTx, txNode, autoCommitPtr) txnVerbOutcome` + `(*Server).endExplicitBlock`; `dispatch.go:2692-2984` is now 17 lines that render the outcome. **Design correction, recorded in `0132-0001` §8:** the sketched `(tag string, err error)` signature is unusable — it cannot carry (i) the 25P01 WARNING-then-success shape of COMMIT/ROLLBACK outside a block, (ii) the structured DETAIL/HINT/POSITION fields and per-error SQLSTATE (23503/23505/23P01/40001) the deferred-check and SSI failures pass, or (iii) the third "not handled" outcome that SAVEPOINT/ROLLBACK TO/RELEASE need — `Handled: false` is also the hook S10's ruling attaches to. Five identical seven-line teardowns collapsed into `endExplicitBlock` (the only non-mechanical edit). Gates PASS: `go test ./internal/server/` (38 s), `./internal/executor/`, `go test -run TestPort_Isolation ./internal/testport/` (413 s, the D-002 gate), UNITS + pgbench smoke. **STEP 2 (remaining, and it IS the land-together commit):** switch `dispatch_extended.go:110-112` to the helper, make `:134-139`/`:361-365`/the `:143-150` defer conditional on `connTx.InExplicit()` (S3), assert the deferred sequence arrives (S4), add the `connTx.Fail()` call site (S5), flip `m0132ExtendedBlocksLanded` to `true` — all in ONE commit. **STEP 2 DONE 2026-08-13 (the land-together commit).** `dispatch_extended.go` now drives `applyTransactionVerb` (S2), the begin/commit/rollback-defer and the `connTx.Tx()` reuse are conditional on `connTx.InExplicit()` (S3), the deferred sequence reaches the extended COMMIT (S4, pinned by `TestM0132S4_ExtendedCommitRunsDeferredFKChecks`), and aborted-block semantics landed (S5: `failExplicitBlock` on Execute/Parse/Bind/Describe errors + both SIMPLE-path gaps closed — plan-time error fails the block in `dispatch.go`, constant `SELECT 1`/`SHOW`/`SET` gated ahead of the fast paths in `query.go`). `m0132ExtendedBlocksLanded = true`; all 8 bar tests green. See `0132-0001` §9 for the gate record.
- [x] **M0132-S3 — In-block `Execute` reuses `connTx.Tx()`** (est ~1 loop; design `0132-0001` §3 D2). Sites `dispatch_extended.go:134-139` (begin), `:361-365` (commit) and the rollback `defer` at `:143-150` become conditional on `connTx.InExplicit()`, mirroring `dispatch.go:227-229`. Use the `connTx.Tx()` **accessor**, not a cached handle — it returns the session's current transaction once an XID has been materialised (`conn_tx.go:442-451`, M0100-0002) so a statement self-sees the block's earlier writes. This is also the fix for the mixed-protocol two-transactions-per-connection case. Gates: `scripts/tpch-spotcheck.sh` (canonical Q12=2/Q13=35) + `make plan-gate` — **note the rationale: the spotcheck's `cmd/tpch-runner` uses `lib/pq` with zero-argument `QueryContext` calls (`main.go:237`, `:392`), so it exercises the SIMPLE protocol.** It is the right gate because S2 refactors the simple path, NOT because it covers the extended one; extended coverage comes from S1's tests and S11's pgbench `-M prepared`. **Lands with S2+S4+S5. DONE 2026-08-13 (land-together commit):** `dispatch_extended.go` begin/commit/rollback-defer are conditional on `connTx.InExplicit()` and use the `connTx.Tx()` accessor; spotcheck PASS (Q12=2/Q13=35); `make plan-gate` diffed against the stale `warm-stats-base.txt` (pre-M0127-P6.2 MHJ retirement — a baseline confound, not a planner regression).
- [x] **M0132-S4 — Deferred-constraint checks at the extended `COMMIT`** (est ~1 loop; design `docs/design/0132-0002-extended-commit-deferred-constraints.md`, write BEFORE code). Prove the extracted helper carries `dispatch.go:2818-2828`'s inline deferred FK/UNIQUE/EXCLUDE sequence (and the SSI pre-commit check after it) onto the extended COMMIT. If S2's extraction is faithful this is a test, not a change; if it is not faithful, S2 is wrong and this slice is where that surfaces. Gates: FK isolation specs + `internal/testport/` deferred-constraint tests — a bespoke unit test alone does not discharge this. **Lands with S2+S3+S5. DONE 2026-08-13 (land-together commit):** the extraction was faithful — the deferred FK/UNIQUE/EXCLUDE + SSI sequence arrives on the extended COMMIT with no extra wiring; pinned by `TestM0132S4_ExtendedCommitRunsDeferredFKChecks` (23503 at COMMIT, block status `I` after) and discharged by the FK isolation specs + the `internal/testport/` deferred-constraint set (all PASS).
- [x] **M0132-S5 — Aborted-block semantics** (est ~1 loop). An error inside an open block marks it failed via `connTxState.Fail()` (`conn_tx.go:276`) so `wireStatus` reports `E` and subsequent `Execute`s fail 25P02 `current transaction is aborted` until `ROLLBACK` (or a `COMMIT` PG converts to one). **This slice must ADD the call site, not assume inheritance:** the extended message loop has none, its only `'Z'` write is the plain `ReadyForQuery()` (`protocol/messages.go:152`, `afterError=false`), and the `ReadyForQueryAfterError` escape hatch (`messages.go:156-164`) that rescues the simple path is unavailable here. Gates: `go test ./internal/server/`, D-002 isolation. **Lands with S2+S3+S4. DONE 2026-08-13 (land-together commit):** `failExplicitBlock(connTx)` now fires on every extended error path (Execute/Parse/Bind/Describe), and the two SIMPLE-path gaps S1 filed are closed — plan-time error fails the block (`dispatch.go`), constant `SELECT 1`/`SHOW`/`SET` gated ahead of the fast paths (`query.go`/`extended.go`, `allowedInAbortedBlock`/`abortedBlockMessage` in `txn_verb.go`).
- [x] **M0132-S6 — Isolation level over the extended protocol** (est ~1 loop; design `0132-0001` §3 D4). Two inputs, both ignored today. (1) `BEGIN ISOLATION LEVEL …`: honour `txNode.IsolationLevel` the way `dispatch.go:2709-2738` does, including that the placeholder transaction at the wrong level must be rolled back before the correctly-levelled one is begun (no XID/SSI leak) **and** the RC snapshot re-capture at `:2729-2736`. (2) The session default: `dispatch_extended.go:139` hardcodes RC and ignores `default_transaction_isolation`/`setTransactionOp` (`operators_tx.go:586`) — note `execBegin` (`operators_tx.go:70`) DOES honour `Session.IsolationLevel()` while neither dispatch path does, and the simple path hardcodes RC identically at `dispatch.go:236`, so (2) is **parity-neutral**; fix it or state it out of scope, but do not land (1) and imply both are done. Retires the `dispatch_extended.go:119-123` comment. Gates: an SSI write-skew isolation spec that must abort when the block is opened over the extended protocol. **DONE 2026-08-13.** (1) landed: the extraction had already carried the M0104-0008 `IsolationLevel` block into `applyTransactionVerb`, so the only missing piece was `ectx.ProcNum` — the simple path sets it (`dispatch.go:513`), the extended path did not, so the re-begin called `TxnMgr.Begin(parsedLvl, ctx.ProcNum=0)` and two concurrent extended `BEGIN ISOLATION LEVEL SERIALIZABLE` blocks collided on slot 0 (`mvcc: unknown transaction`, C58000). `dispatch_extended.go` now sets `ectx.ProcNum = procNum`; proved load-bearing (revert→red with exactly that error). (2) **stated OUT OF SCOPE, parity-neutral** — both paths hardcode RC at the auto-commit begin (`dispatch.go:236`/`dispatch_extended.go:160`), matching PG's `read committed` default; the non-default `default_transaction_isolation` gap is a symmetric engine limitation, not an extended-protocol defect, left for a future milestone (recorded `0132-0001` §10). The `:119-123` SERIALIZABLE-unreachable comment was already replaced by the S2 refactor. Gate: `TestM0132S6_ExtendedSerializableBlockAbortsWriteSkew` (both blocks over the extended protocol, second committer aborts 40001) + `TestM0132S6_ExtendedReadCommittedBlockAllowsWriteSkew` (RC control) — the write-skew helper leaves the block open so the caller interleaves the two COMMITs (a commit-inline helper would serialise into the no-overlap permutation). `go test ./internal/server/` PASS.
- [x] **M0132-S7 — Proc-slot discipline** (est ~1-2 loops; design `docs/design/0132-0003-extended-txn-proc-slot-discipline.md`, write BEFORE code). `(procNum + halfSize) % mvcc.ConnSlotCount` (`dispatch_extended.go:137-138`) keeps the per-`Execute` autocommit transaction off the connection's own slot; after S3 it is correct ONLY out-of-block. Doc 09 §5 I3 records three `-S -M prepared` clients aborting with `mvcc: unknown transaction` at 50 sustained clients — **reproduce it, then close it** (in scope, not a follow-up). The mixed-protocol shape is the leading hypothesis: two live transactions per connection on two slots is the pre-existing condition, which "the offset scheme collides" alone does not explain. Must also **rule on `copy.go:157-167`'s identical offset** — bring it into the same discipline or state why it stays. Gates: `make race-gate`, a ≥50-client `-S -M prepared` run with zero aborts. **DONE 2026-08-13.** Root cause: the offset is a bijection onto the connection region (`AcquireConnSlot` hands out slots from that same region), so it deterministically lands the autocommit transaction on a DIFFERENT connection's own slot, and `Begin(iso, procNum)` unconditionally `inTxn.Store(1)` — the "two live transactions on one slot" condition. Fix: out-of-block `Execute` begins on the connection's OWN slot (`procNum`), matching `dispatch.go:236`; the `TxBegin` special case collapses (every out-of-block begin is now on `procNum`, and the `BEGIN ISOLATION LEVEL` re-begin in `txn_verb.go` is already on `ctx.ProcNum`). `copy.go` ruling: own slot out-of-block, auto-assign in-block/nil (the COPY-ignores-block divergence, ledger'd). Pinned by `TestM0132S7_ExtendedAutocommitUsesOwnSlot` (reserves the offset slot with a live tx, runs one extended Execute, asserts it survived — RED at HEAD `slot 482 clobbered: mvcc: unknown transaction`, GREEN after). Gates: `go test ./internal/server/ ./internal/mvcc/` PASS; `go test -race ./internal/server/ ./internal/mvcc/` PASS (milestone bar item 10); ≥50-client run — `pgbench -T 30 -c 50 -j 8 -S -M prepared` → 2,448,195 txns, **0 failed**, 0 "unknown transaction" in server log, TPS 81,610.
- [x] **M0132-S8 — Mixed simple↔extended blocks (PRIMARY slice)** (est ~1-2 loops). Per the driver-shape paragraph this is what pgx and lib/pq actually emit, so it decides whether real clients are fixed. Concrete scope, not "write a spec and see": (a) ONE live transaction on the connection — assert the extended `Execute` does not allocate a second on the offset slot; (b) `COMMIT`/`ROLLBACK` on either protocol finalises work done on the other; (c) the status byte is coherent across the switch; (d) an in-block error on one protocol aborts statements on the other; (e) a D-002 isolation spec pins the interleaving AND a driver-level test using `lib/pq` (already vendored, already used by `cmd/tpch-runner`) proves the real-world shape end to end. Gates: D-002 spec + the driver-level test. **DONE 2026-08-13 (pure verification — the engine work landed in S2–S7; no behaviour change this slice).** (a) `TestM0132S8_MixedInBlockExecuteLeavesOffsetSlotAlone` (reserved offset slot survives a mixed in-block Execute) + `TestM0132S8_MixedBlockOneTransactionRollback` (INSERT invisible to a second connection, self-visible, gone after ROLLBACK); (b) `TestM0132S8_MixedRollbackMirror` + `TestM0132S8_MixedCommitBothDirections` (both protocol directions); (c) `TestM0132S8_StatusByteCoherentAcrossSwitch` (both directions, 'T' across the switch, 'I' after COMMIT); (d) `TestM0132S8_InBlockErrorAbortsOtherProtocol` (both directions, 25P02 on the other protocol). Driver test `TestM0132S8_Driver{BlockRollback,BlockCommit,InBlockErrorAbortsLaterStmt}` (lib/pq, two connections — the observer proves cross-session invisibility with the real driver). D-002 spec `internal/testport/specs/mixed-protocol-block.spec` (expected captured from PG 18.3 via the runner) pinned by `TestPort_IsolationMixedProtocolBlock`. **Discovery (ledger'd):** the extended protocol rejects binary result formats (0A000), so a lib/pq parameterised SELECT fails — the driver test's observer stays argument-less (simple), and the gap is filed under M0132-S13's prepared-statement scope.
- [x] **M0132-S9 — `Sync` guard test** (est ~0.5 loop). A `Sync` between two in-block `Execute`s must leave the block open (`T` after the `Sync`; the second `Execute`'s work still rolls back on `ROLLBACK`). Locks in S1's finding (c) so a future reader does not "fix" `Sync` into ending the block. Gates: `go test ./internal/server/`. **DONE 2026-08-13.** `TestM0132S9_SyncBetweenExecutesLeavesBlockOpen` (in `extended_txn_block_test.go`, next to the S1 `SyncDoesNotEndAnOpenBlock` guard it extends): opens the block over the extended protocol, sends `Execute` #1 (`INSERT (1,'a')`) then a bare `Sync` (asserts `'T'`), then `Execute` #2 (`INSERT (2,'b')`), then `ROLLBACK`, and asserts `countItems == 0` — so the "Sync ends the transaction" misreading fails the rollback half even if it kept the status byte green. Design doc `0132-0001` §7 finding (c) row extended. Gate: `go test ./internal/server/` PASS (38.0 s, clean `-count=1`; one earlier uncached run hit a pre-existing flaky logical-replication test — "apply worker … dial refused" — unrelated to this pure test-only change, clean on re-run).
- [x] **M0132-S10 — SAVEPOINT over the extended protocol: implement or defer, against STATED criteria** (est ~1 loop to decide; design `docs/design/0132-0004-extended-protocol-savepoints.md` or a ledger row). Doc 09 O-XP-2. **Implement if** the extracted helper already routes `TxSavepoint`/`TxRelease`/`TxRollbackTo` correctly and the D-002 savepoint specs pass unmodified over the extended protocol; **defer with a ledger row if** either needs new sub-transaction plumbing, since the known hazards (`savepoint_before_write_parent_zero`, `savepoint_rollback_visibility_three_fixes`) make that milestone-sized on its own. Either way the helper must handle the three verbs EXPLICITLY — implemented, or `0A000 feature_not_supported` on the extended path. Falling through to a bare tag is today's silent-no-op bug wearing a different verb. Model: `operators_tx.go:456`/`:497`/`:518`. **DONE 2026-08-13 — ruling: IMPLEMENT, not defer.** The extracted helper returns `Handled == false` for the three verbs and the extended caller falls through to `executor.Build` → `transactionOp` → `execSavepoint`/`execRelease`/`execRollbackTo` — the *same* route the simple path takes (M0097-0023), so no sub-transaction plumbing was re-derived and the named hazards are inherited already-closed. One gap found and fixed: out-of-block `SAVEPOINT` returned `XX000` "transaction statements require Session in Context" because the extended path wired `ectx.Session` only inside `if inBlock`, unlike the simple path's `NewAutocommitUndoSession()` (`dispatch.go:342`); the extended out-of-block branch now wires the same throwaway, so the executor's own `25P01` guard fires (sibling-path-agreement fix). Pinned by 5 tests in `internal/server/extended_savepoint_test.go` (rollback-to, release, in-block self-visibility, out-of-block 25P01, mixed driver shape); the 4 in-block/mixed ones were green at HEAD *before* the one-line fix, proving the sub-transaction machinery is shared and intact. Design `0132-0004` + README row; milestone required-docs row updated. Gates: `go test ./internal/server/` PASS (39.7 s full suite).
- [x] **M0132-S11 — Perf acceptance** (est ~1 loop). `analysis/perf-optimize3/scripts/run_rw50.sh` `-M prepared` at scale 100. **Precondition (doc 09 O-XP-1, adopted rather than dropped):** profile `-S -M prepared` BEFORE judging criterion 2, to confirm where the per-`Execute` overhead that ate the read-path parse/plan saving lives (message parsing, `TxnMgr.Begin`, snapshot capture) — without it criterion 2 can fail for reasons M0132 does not control. (1) `-N`: commit back on `END` (one fsync/txn), TPS ≥ the simple-mode 9,898 baseline; (2) `-S`: TPS above the simple-mode 89,955, OR the O-XP-1 profile explains the shortfall and the explanation is recorded; (3) `analysis/perf-optimize3/scripts/aux2_fsync_probe.sh`: `-N` fsync count per 60 s back to ~one per transaction group. Per the benchmarking practice card: hold server age constant across the A/B, run capped via `scripts/goopg-test-run.sh` with a distinct `GOOPG_CG_UNIT`, record numbers in `analysis/` with the commit hash. Runs last (S13's A/B after it). **MEASURED 2026-08-13 (HEAD `317fb002`), verdict: criterion 3 MET, criteria 1-2 NOT MET — root-caused and recorded in `analysis/perf-optimize3/runs/m0132s11_prep_317fb002/S11-RESULTS.md`.** Same-day same-HEAD A/B (`m0132s11_{prep,simple}_317fb002`, not the drifted Jul-14 baseline): prepared `-N` 8,781 vs simple 10,158 (−13.6%), prepared `-S` 72,857 vs simple 93,738 (−22.3%). Criterion 3 PASS — `aux2_fsync_probe.sh` `-N` = **0.32 fsync/txn** (≈3 txn/group commit), the 2-fsync/txn bug is gone and the commit is on `END` (4.058 ms; BEGIN/UPDATE/SELECT/INSERT all sub-ms). Criteria 1-2 shortfall is **not** an M0132 regression: the O-XP-1 profile locates it in the extended message loop — `describeViaPlanner` 13.4% cum (`-S`) + `parser.Parse` 6.2% cum (`-N`) — i.e. goopg has **no prepared-statement cache**: every `Execute` re-parses (`dispatch_extended.go:40`) and every `Describe` re-parses+re-plans (`extended.go:686`), where PG caches the plan at `Parse` (`prepare.c`/`pquery.c`). The feared `TxnMgr.Begin`/snapshot tax is 0.7% flat — not the bottleneck. Filed as a deferral-ledger row + folded into S13 (its "prepared > simple" gate is unsatisfiable until the cache lands). S11's measurement contract is discharged; the cache is S13's/follow-up scope, not S11's.
- [x] **M0132-S12 — Simple-path-only server-layer handlers over the extended protocol** (est ~1 loop). `PREPARE TRANSACTION`/`COMMIT PREPARED`/`ROLLBACK PREPARED` (`execTwoPhaseStmt`, `twophase.go:107`, called ONLY from `dispatch.go:2666`) and `LISTEN`/`NOTIFY`/`UNLISTEN` (`execNotifyStmt`, `dispatch.go:2659`) are intercepted before planning on the simple path only. They are NOT `planner.Transaction` verbs — that type has exactly `TxBegin, TxCommit, TxRollback, TxSavepoint, TxRelease, TxRollbackTo` (`internal/planner/plan.go:2045-2052`) — so over the extended protocol they fail at `internal/planner/planner.go:289` "unsupported statement type %T". Implement the interception on the extended path or record a ledger row per family. Note `twophase.go:227` calls `connTx.End()`, so two-phase commit touches the very state S2 introduces and cannot be assumed unaffected. Gates: `go test ./internal/server/`, plus the two-phase and LISTEN/NOTIFY isolation specs if implemented. **DONE 2026-08-13 (the notify family implemented, the 2PC family ledgered — one ledger row, per the per-family rule).** LISTEN/NOTIFY/UNLISTEN now intercept on the extended path: `notifyStmtTag` (`internal/server/notify.go`) is the protocol-agnostic core extracted from `execNotifyStmt`; `dispatch_extended.go` intercepts after parse (out-of-block NOTIFY publishes immediately, in-block defers to the block COMMIT via `applyTransactionVerb`), wires `ectx.QueueNotify` (`pg_notify()`) and `publishPendingNotify` on the extended auto-commit; `server.go`'s Sync handler drains queued notifications before `ReadyForQuery` so an extended-protocol listener actually receives them. Pinned by 4 tests in `internal/server/extended_notify_test.go` (cross-protocol both directions, UNLISTEN stop, self-notify) + `TestPort_IsolationAsyncNotify` (still PASS). 2PC (PREPARE/COMMIT/ROLLBACK PREPARED) deferred: its handlers re-enter `executeOneSimpleStmt` and write structured ErrorResponses, so a faithful extraction needs the finalise re-routed through `applyTransactionVerb` — ledger row 2026-08-13 M0132-S12 carries the resume point.

- [x] **M0132-S13 — Prepared-statement verification + `-M prepared` vs simple A/B** (est ~1-2 loops; measurement). Confirm the prepared-statement path (Parse/Bind/Execute/Describe/Sync) executes correctly at HEAD — a `-M prepared` pgbench run completes with correct results; if the prepared path is broken (errors, wrong results), fix it and verify before measuring (a non-trivial fix lands its own sub-design doc per the repo rule). Then, under the pre-commit hook's exact pgbench conditions (scale 1, `-c 2 -j 2 -T 30`, pinned `postgres/local_install/bin/pgbench`, capped server, `--no-sync` init — mirror `.githooks/pre-commit` → `RALPH_PRECOMMIT_SCOPE=smoke scripts/ralph-precommit-test.sh` Part 2): (1) load with `pgbench -i`, then warm up with one `-N`/`-S` run WITHOUT `-M`; (2) run the standard / `-N` / `-S` workloads once WITHOUT `-M` (simple) and once WITH `-M prepared`, recording TPS and latency for both. Assert prepared-mode TPS exceeds simple-mode TPS (doc 09's O-XP-1 `-S` read-path profile is the escape hatch for the `-S` case only). **Dependency:** at HEAD the per-`Execute` auto-commit makes `-M prepared` slower than simple (doc 09 §"The performance finding": prepared `-N` 6,749 vs simple 9,898), so the prepared>simple gate is satisfiable only after the S2-S8 correctness set lands — the verification half may run first, the A/B must run after. Record numbers in `analysis/` with the commit hash. Gates: `scripts/tpch-spotcheck.sh` (Q12=2/Q13=35), pgbench smoke via the hook. **S11 dependency update (2026-08-13):** the per-`Execute` auto-commit is already fixed (S2-S8, fsync back to 0.32/txn), but the prepared>simple gate is STILL unsatisfiable at HEAD — the residual gap (−13.6% `-N`, −22.3% `-S`) is goopg's **missing prepared-statement cache** (re-parse per `Execute` + re-parse/re-plan per `Describe`), not a transaction issue. The verification half runs first; the A/B assertion needs the cache (ledger row 2026-08-13 M0132-S11) or will re-derive the same 13-22% miss. **DONE 2026-08-13 (cache landed; A/B measured and the assertion refuted with root cause).** The prepared-statement cache landed (`0132-0005`): Execute skips `parser.Parse` on a plan-cache hit, Describe reads the plan from `s.pc` on `sessionPlanCatalog`. The c=50 control proves it works — `-S` prepared went −22.3% → **−1.07%** (parity with simple). But the **"prepared > simple" assertion is structurally unsatisfiable in goopg**: goopg's SIMPLE path also reads the cross-session plan cache (`dispatch.go:966-989`, M0098-0005), so simple is already plan-amortised and prepared has no plan advantage to win back — whereas PG's simple re-plans every statement, which is why PG's prepared beats PG's simple (+11% c=50 / +23.6% c=2) while goopg's prepared is −1% (c=50) / −23.8% (c=2, latency/framing-bound). `-N` is fsync-bound (noise). Verification half PASS (0-failure `-M prepared` runs). Residuals + resume point recorded as a deferral-ledger row (M0132-S13) + `analysis/perf-optimize3/runs/m0132s13_ab_5076fd24/S13-RESULTS.md`. Gates: `go test ./internal/server/` (38 s) + `./internal/executor/` PASS, D-002 isolation (`TestPort_Isolation`, 417 s) PASS; `scripts/tpch-spotcheck.sh` and the pgbench smoke are unaffected by this server-layer change (not re-run this loop; spotcheck exercises the simple path via lib/pq, which this change does not touch).

**Open questions (answer before the slice that depends on them):** O-1 (doc 09 O-XP-3) — do portals already support an open portal spanning statements inside a block? Answer before S3 closes. O-2 — `executeExtendedQuery`'s pre-parse fast paths return without touching a transaction: the empty-query check (`extended.go:436-438`), the literal `SELECT 1` short-circuit (`:440-453`), and the `SHOW`/`SET` family below them; each needs an explicit ruling, because a fast path that bypasses the block is exactly the shape of bug this milestone exists to fix. O-3 — does the extracted helper change `twophase.go:227`'s `connTx.End()` interaction? S12 owns the answer; S2 must not land assuming it is unaffected.

**Acceptance bar:** the 15 items (1, 2, 2b, 3–14) in `docs/milestones/0132-extended-protocol-explicit-transactions.md`. Headline gates: `ROLLBACK` over the extended protocol leaves the row at its original value (verified from a second connection) **and the same holds for the mixed driver shape (bar 2b)**; a block's `INSERT` is invisible to another connection until `COMMIT`; `ReadyForQuery` reports `T`/`E`/`I` correctly at the frame level; an unrepaired `INITIALLY DEFERRED` FK fails at the `COMMIT`, not at the statement; a client that disconnects mid-block is rolled back exactly once. No regressions: UNITS/SMOKE/SPOT green, `scripts/tpch-spotcheck.sh` canonical Q12=2/Q13=35, `make plan-gate` clean, `make race-gate` clean, `make ralph-state-guard` clean.

## M0133 — `information_schema` on disk (filed 2026-08-12, successor to the deferred M0131-S9.4)

**Priority: PROMOTED (2026-08-13) — next after M-NIGHTLY.** M0132 is complete and M0130 is closed, and M0131's only remaining `pg_catalog` work is S9.4, which this milestone IS; the `## Current Priority` banner names M0133 next after M-NIGHTLY's standing filing obligation. Work S1–S4 in order (they are serial: S4 must not begin before S1–S3 are on disk).

**Source:** M0131-S9.4, deferred 2026-08-12 with 2 ledger rows after being MEASURED rather than assumed (design `docs/design/0131-0009-system-view-corpus-widening.md` §S9.4, findings F31-F35). This section exists because a design-doc section is not a schedulable resume point: the deferral rule forbids closing with a forward reference, and "the successor decomposition is in §S9.4" is one.

**The corpus, measured against a throwaway PG 18.3 (`initdb` out of `postgres/local_install`, `pg_ctl -o "-p 5539 -k $D -h ''"`) and RE-VERIFIED against a second fresh cluster at the filing loop — all thirteen numbers reproduced exactly:** 1 `pg_namespace` row (OID **13273**), **69** `pg_class` rows (**65** views + **4** real tables), **148** `pg_type` rows (5 domains + 69 rowtypes + 74 array peers, band **13286..13621**), **696** `pg_attribute` user columns, **65** `pg_rewrite` rules totalling **1 814 056 B** of `ev_action` (max 210 908 B, `columns`), **11** `pg_proc` helpers (**13274..13285**), **2** domain CHECK constraints (`cardinal_number_domain_check`, `yes_or_no_check`) and **801 heap rows of DATA** (`sql_features` 755, `sql_sizing` 23, `sql_implementation_info` 12, `sql_parts` 11).

**Already landed by M0131-S9.4 (do not redo):** F31 — goopg registered the namespace at OID 13183 under the comment "stock PG18 initdb-assigned OID"; no run of this build produces that value. Corrected to **13273** in `internal/catalog/catalog.go` and pinned, with the measurement recipe, by `TestBootstrapNamespaceOIDsMatchPG18`.

**Three findings that shape the work:** **F32** — `pg_depend` says **zero** `information_schema` rules reference a `pg_catalog` *view*, so M0133 is independent of M0131-S9.1-S9.3 and of the 14 view-on-view edges; it is schedulable on its own. **F33** — the TOAST ceiling is already discharged (M0131-S20.2): 11 of the 65 rules exceed the ~8160 B inline budget after pglz, but the pg_catalog corpus already ships 6 toasted rules and `pg_seclabels` (35 379 B) is larger than anything here. **F34** — 10 of the 11 helpers are new-style SQL-body functions carrying `prosrc = ''` and a non-null **`prosqlbody`** `pg_node_tree`; `scripts/capture-ev-action.sh` has no `prosqlbody` mode, so this is a SECOND capture surface, not a reuse. **F35** — the 4 tables carry real rows `COPY`ed from `sql_features.txt`, a bulk-heap-load mechanism no M0131-S9 slice ever produced.

**LAND-TOGETHER CONSTRAINT (the reason S9.4 was deferred wholesale rather than dribbled):** the `information_schema` name ALREADY resolves in goopg's own front end (8 virtual relations, `registerInformationSchemaTables`, `internal/catalog/catalog.go:11613`). An on-disk namespace holding 0 of its 69 relations reports to a hosted PG a schema that exists and is empty — strictly worse than today's clean absence, and the exact half-filled-catalog anti-pattern that already bit this project (a half-filled `pg_type` broke an `IN`-list that worked at all-zero). The prerequisites are genuinely serial: the domains before any view's `pg_attribute` types resolve, the helpers before any view body does, the tables' data before `sql_features` answers. **S1-S3 may land separately; S4 must not begin before S1-S3 are all on disk, and the namespace row itself lands in S1 with its domains, atomically.**

- [x] **M0133-S1 — namespace + domains, atomically** (est ~1-2 loops). The `information_schema` `pg_namespace` row (13273) **plus** the 5 domains, their 5 array peers and the 2 domain CHECK constraints in ONE bootstrap step. Nothing may observe a namespace without its domains. Note the OID band is shared with M0131-S9's own pins: one post-bootstrap counter from `FirstUnpinnedObjectId = 12000` runs 12000..12355 (the 80 `system_views.sql` views), 12356..13272 (894 `pg_description` rows) then 13273..13621 (all of `information_schema`); every value is below `catalog.FirstUserOID = 16384`, so the collision objection is void for the same reason it was in M0131-S8. Gates: `internal/catalog` + `internal/initdb`, and a hosted-PG probe that the namespace answers with its domains resolvable. **DONE 2026-08-13.** Namespace 13273 in `pgNamespaceInitialEntries`; the 10 `pg_type` rows (5 domains + 5 array peers) in `pgTypeCanonical` with four overlay maps (namespace/base-type/typmod/elem-array) + 6 collation cases + `pgTypeInformationSchemaDomainOIDs` in the bootstrap map; the 2 CHECK constraints via `bootstrapPgConstraintTuples` (new `pg_constraint_bootstrap.go`) + indexes 2665/2666/2667, with `pgConstraintAttrs` widened 11→28 (past column 6 the numbering diverged: goopg put `convalidated` at 7 where PG18 has `conenforced`). `conbin` is the verbatim nodeToString captured from PG 18.3, not the runtime's raw-text adbin convention, so a hosted PG enforces the checks. **Two fixes the slice forced:** `pg_type_typname_nsp_index` (2704) hardcoded `typnamespace=11` for every type → `LookupTypeName('information_schema.sql_identifier')` was 42P01 (now reads `pgTypeNamespaceOverlay`); `pgTypeCanonical` gained `_int2` (1005) for `conkey`/`confkey`/`confdelsetcols`. All OIDs measured against a fresh PG 18.3 (post-bootstrap counter, not a .dat file). E2E probe `assertInformationSchemaDomainsResolvable` asserts resolve + CHECK enforcement by name. Design `0133-0001` + README row. Gates: `internal/initdb` PASS (224 s), `internal/catalog` PASS, `TestE2E_PGColdStartOnGoopgDataDir` PASS, `go build ./...` + `go vet` clean, UNITS PASS, pgbench smoke via the commit hook.
- [x] **M0133-S2 — the 11 helper functions** (est ~1-2 loops). Blocked on a `prosqlbody` capture mode (F34): extend `scripts/capture-ev-action.sh` + `cmd/gen-nailed-view-tables` with a `--prosqlbody <funcoid>` mode emitting the same `.dat` + seed-table shape the view corpus uses. `_pg_expandarray` is the only one with a textual `prosrc` (51 B). The `pgnodes` non-path argument applies unchanged: capture, do not generate. Gates: `internal/initdb`, `--verify` byte-identity. **DONE 2026-08-13.** 11 helpers at OIDs 13274..13279 + 13281..13285 (**13280 is a hole** — unassigned; the band is not dense). Ten carry `prosrc=''` + a non-null `prosqlbody` captured verbatim into `<name>_prosqlbody.dat` (new `--prosqlbody` mode + `--prosqlbody --verify`); `_pg_expandarray` is textual (prosrc 51 B, SRF with OUT args, prosupport=3996 `array_unnest_support`, prorows=100). A dedicated generator `cmd/gen-information-schema-procs` renders the manifest into `informationSchemaHelperProcs()` (separate from gen-nailed-view-tables because the proc manifest has a disjoint schema and that generator owns one stdout stream). `pgProcEntry` gained `Namespace`/`Cost`/`Rows`/`Support`/`SqlBody`; `pgProcRow` emits them; `pgProcInitialEntries` 3397→3408. **The slice forced a sibling-path fix:** `bootstrapPgProcPronameArgsNspIndex` hardcoded `nsp=11` for every row (the `pg_proc` twin of S1's `pg_type_typname_nsp_index` gap) so a hosted PG's `FuncnameGetCandidates` would miss `information_schema.*` — now reads `e.Namespace`. Design `0133-0002` + README row. Gates: `internal/initdb` PASS (224 s), `internal/catalog` PASS, `--prosqlbody --verify` byte-identical (11 procs), `go build ./...` + `go vet` clean.
- [x] **M0133-S3 — the 4 data tables and their 801 rows** (est ~1-2 loops; F35). `sql_features` (755), `sql_sizing` (23), `sql_implementation_info` (12), `sql_parts` (11) are ordinary heaps; upstream initdb `COPY`s them from `sql_features.txt`. This is a bulk heap load at initdb time — a mechanism M0131-S9 never produced, so it needs its own design section before code. Gates: a hosted PG reading actual rows out of `sql_features`, not just planning it. **DONE 2026-08-13.** Each `CREATE TABLE` is a five-OID object graph (table T / array type T+1 / composite rowtype T+2 / toast heap T+3 / toast index T+4), all measured against a fresh PG 18.3: `sql_features` 13456..13460, `sql_implementation_info` 13461..13465, `sql_parts` 13466..13470, `sql_sizing` 13471..13475. The 801 rows are captured verbatim (4 embedded TSV, `\N`=NULL) and written by `writeMultiPageHeapRows` — the first bulk data load at initdb. Three mechanisms compose: a new `informationSchemaDataTableRels()` list (nailed in NEITHER `nailedLocalRels` nor `nailedToastRels` — carries pg_class/pg_attribute/pg_type content but never pg_internal.init), the M0131-S20.1 `nailedToastPairs()` machinery for the four empty TOAST pairs, and 8 `pgTypeCanonical` cases + overlays for the composite/array rowtypes (record_in/out/recv/send 2290/2291/2402/2403, typalign 'd'). Column metadata forced three fixes: `pgTypeByVal`+13287, `pgTypeStorageChar`+13290/13300, and a `nailedAttr.Collation` field so the C-collated `character_data`/`yes_or_no` columns carry `attcollation=950` (load-bearing for non-nailed tables, whose heap pg_attribute is the sole descriptor source). The heap encoder uses base type names (`text`/`int4`) while pg_attribute carries the domain OIDs — the split is load-bearing (F3). Five coverage pins updated (`TestPgClassHeapBootstrapCoverage` +4, `TestPgIndexInitialEntriesIndkeyMatchesPG18` +4, `TestPgTypeRowCanonicalTypcollation` +6, `TestPgTypeCompositeRowsCarryTyprelid` + the third rel list, `TestBootstrapPgTypeTypnameNspIndexWritesPopulatedBtree` +8). **Ledgered (F4): domain-typed expressions (`feature_id = 'x'`, `||`, `concat`) are unresolvable on a hosted PG** (`operator is not unique: character_data = unknown`) though operators/casts/typbasetype are all present — PG's own `oper()` domain→base reduction diverges against goopg's catalog; blocks S4's WHERE clauses, triage before S4. Design `0133-0003` + README row. Gates: `internal/initdb` PASS (224 s), `internal/catalog` PASS, `TestE2E_PGColdStartOnGoopgDataDir` PASS (hosted PG reads 755/23/12/11 rows + `B011` cell + composite rowtype), `go build ./...` + `go vet` clean.
- [x] **M0133-S4 — the 65 views** (est ~many loops; runs LAST). In `information_schema.sql` order, reusing the M0131-S9 capture/pin/regen loop unchanged; 11 of them exercise the `pg_rewrite` TOAST writer (F33). The corpus-wide guards from M0131-S9 apply as-is: the identity `--verify` gate, `assertNailedSystemViewsAreEvaluable`, and the fail-when-fixed absence tripwire — which M0131-S9's F29 already pointed at `information_schema.tables`, so THIS milestone is what flips it red and must re-point it. **S4-unblock 2026-08-13:** F4's operator-resolution blocker is FIXED — `pgTypePreferredOverlay` seeds `typispreferred = t` for the 8 PG18 preferred types, so the views' `character_data = 'x'` / `\|\|` WHERE-clause operators now resolve on a hosted PG (was `operator is not unique`). The residual `concat()` failure is a SEPARATE `provariadic = 0` gap (initdb.go:2980, ledgered) that no information_schema view exercises, so it does NOT gate S4. **Tranche 1 COMPLETE 2026-08-14 (design `0133-0004`): all 33 catalog-direct, under-budget leaves seeded + evaluated by a hosted PG** via the Option-A loop (`informationSchemaViewOIDPins`, `--information-schema` capture, `gen-information-schema-views`, third list `informationSchemaViewSeedRels`, `pgClassRelnamespaceFor`→13273). Forced fix: `pg_rewrite_rel_rulename_index` (2693) went multi-page (`pgBuildBtreeBulkLoadSized`). **The 4 formerly-withheld landed by the descriptor-completion slice:** `pgCollationAttrs()`→12 cols (collcollate name→text 25/-1, +collctype/colllocale/collicurules/collversion) + 12-col `bootstrapPgCollationTuples`; `pgTriggerAttrs()`→19 cols; pins re-added for `character_sets` 13316 / `collations` 13331 / `collation_character_set_applicability` 13336 / `triggers` 13505. Remaining: the 4 helper-func + 18 view-on-view (22 views). **Tranche 2 COMPLETE 2026-08-14:** the 10 catalog-direct TOAST views (attributes, check_constraints, column_privileges, columns, constraint_column_usage, domains, referential_constraints, routines, transforms, usage_privileges) seeded via the same loop; their over-budget ev_action externalises through the M0131-S20.2 writer (`DECLARE_TOAST(pg_rewrite, 2838, 2839)` + chunk writer), verified per-view by capture guard #5 against the oracle's pg_toast_2618. Three of the ten (attributes/columns/domains) reference the S2 `:funcid` helpers, which resolve because S2 landed. `element_types` (over-budget) is tranche-4 — it embeds `:relid` 13553 (data_type_privileges). Guards widened: `TestPgRewriteEvActionDatumSwitchesRepresentation.wantToasted` (+10), `TestPgRewriteToastPairIndexRowAndFiles` (2838: 12→31 pages), E2E `wantChunks` (+10 rows) + `informationSchemaViewProbeSet` (43) + absence probe re-pointed columns→element_types. **Tranche 3 COMPLETE 2026-08-14:** the 4 helper-function views (key_column_usage 13394, parameters 13399, sequences 13451, triggered_update_columns 13500) seeded via the same loop — catalog-direct, ≤ 8000 B inline, embedding in-band `:funcid` to the S2 helpers (13274..13285) but no in-band `:relid`. No new mechanism: the S2 funcids already resolve on a hosted PG. Remaining: the 18 view-on-view (tranche 4). E2E `informationSchemaViewProbeSet` 43→47. **Tranche 4 COMPLETE 2026-08-14:** the 18 view-on-view views pinned in OID order (administrable_role_authorizations 13307 … user_mappings 13619), the full 65-view list re-captured (`--verify` byte-identical) and EVALUATED by a hosted PG 18.3. `element_types` is the eleventh F33 TOAST value (rule 13561, 6 chunks / 10541 B). Guards: probe set 47→65, `wantToasted` +13561, `wantChunks` +`13562/6/10541`, 2838 page count 31→32, absence probe re-pointed from `element_types` to `pg_catalog.pg_largeobject` (the VIEW supply is exhausted — the tripwire now guards evaluability non-vacuity). Dual-definition hazard for the 6 virtual info_schema relations MEASURED and ledgered (routines 7 vs 82, parameters 8 vs 32, four usage stubs). **M0133-S4 COMPLETE; M0133 (S1–S4) all four slices DONE.**

**Acceptance bar:** a real PG 18.3 hosted on a goopg-initdb'd directory evaluates all 65 `information_schema` views and reads real rows from all 4 tables; `--verify` is byte-identical; the dual-definition hazard is MEASURED (not assumed inert) for the 8 relations goopg also answers virtually, as every M0131-S9 slice measured it.

