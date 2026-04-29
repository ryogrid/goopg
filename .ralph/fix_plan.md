# goopg Fix Plan

The roadmap below is derived from `.ralph/specs/GOAL_AND_REQUIREMENTS.md`. The
"Definition of Done (Initial Milestone)" in §10 of the spec is the target;
items here decompose that target into agent-sized chunks. Pick the topmost
unchecked item unless a dependency forces a different order.

## Milestone 0007 — WAL segment preallocation & fdatasync

See `docs/milestones/0007-wal-segment-preallocation.md` for the full
DoD. Decomposed into the two design-doc seams the milestone calls
out (`0007-0001`, `0007-0002`); pick the topmost unchecked item.

- [x] WAL segment preallocation: zero-fill new segments to
      `SegmentSize` + fsync at creation; directory fsync; EOS
      sentinel for the trailing zero-fill so recovery terminates
      cleanly. Design doc
      `docs/design/0007-0001-wal-segment-preallocation.md`.
      (landed 2026-04-29: `wal.Config.Preallocate` flips on the
      preallocator. `state.openSegment` zero-fills new files via
      `preallocateSegment` (64-KiB-chunk WriteAt loop + fsync) and
      fsyncs the WAL directory entry. The encoded zero header
      (`len=0 && crc=0`) is now the EOS sentinel: `Writer.Append`
      rejects empty payloads (`ErrEmptyPayload`); `ReadAll` /
      `decodeRecord` callers honour `isZeroHeader` to stop on
      first zero header. `detectWritePos`'s legacy
      "size-of-last-segment" formula is replaced by
      `scanLastSegmentEnd` which walks the last segment
      record-by-record to find the actual write position —
      handles both short legacy segments and full-size
      preallocated tails. The new `wal_init_zero` GUC
      (`ContextPostmaster`, default `on`) flows through
      `cmd/goopg start` → `initdb.Open(OpenOptions{WALInitZero})`
      → `wal.Config.Preallocate`. New tests:
      `TestPreallocatedSegmentIsFullSize`,
      `TestPreallocatedSegmentRecoversCleanly`,
      `TestAppendRejectsEmptyPayload`. `fdatasync` switch
      (0007-0002), `wal_recycle`, eager next-segment lookahead,
      counters / observability, and pgbench latency measurement
      deferred.)

- [x] `fdatasync` on the commit path: replace
      `f.Sync()` in `flushUpTo` with platform-aware `fdatasync`
      (Linux) / `fsync` fallback. Keep full `fsync` at segment
      creation, post-creation directory flush, and segment
      removal. Design doc
      `docs/design/0007-0002-fdatasync-commit-path.md`.
      (landed 2026-04-29: Build-tagged `dataSync(f *os.File)
      error` helper. `internal/wal/sync_linux.go` calls
      `unix.Fdatasync(int(f.Fd()))` from `golang.org/x/sys/unix`
      (already a transitive dep), mirroring upstream's
      `pg_fdatasync` from
      postgres/src/backend/storage/file/fd.c.
      `internal/wal/sync_other.go` falls back to `f.Sync()` on
      every non-Linux platform — preserves the durability
      contract at the cost of paying for inode metadata
      updates `fdatasync` would have skipped. `flushUpTo` now
      calls `dataSync(f)` per dirty segment instead of
      `f.Sync()`. Full `fsync` is preserved in
      `preallocateSegment` and the directory-fsync after
      segment creation — both need durable metadata. The
      `wal: fdatasync %s` error prefix in the loop is now
      accurate. The pgbench latency measurement, the
      `wal_sync_method` GUC selector, and a segment-removal
      directory fsync are deferred.)

## Milestone 0008 — Logical replication

See `docs/milestones/0008-logical-replication-support.md` for the
full DoD. Decomposed into the five design-doc seams the milestone
calls out (`0008-0001` … `0008-0005`); pick the topmost unchecked
item.

- [x] Logical replication slot foundation + `pg_replication_slots`
      view. Design doc
      `docs/design/0008-0001-logical-decoding-pipeline.md`.
      (landed 2026-04-29: `wal.SlotKind` grows `SlotLogical`;
      `wal.Slot` grows `Plugin` / `Database` / `CatalogXmin` —
      all JSON-tagged with `omitempty` so physical-slot state
      files stay byte-identical and pre-M0008 files round-trip
      cleanly with the new fields zero-valued. New typed
      constructor `Slots.CreateLogical(name, plugin, database,
      startLSN)`; `Slots.Create` accepts `SlotLogical` (the
      pre-M0008 hard-reject is dropped). New
      `pg_catalog.pg_replication_slots` virtual view in
      `internal/initdb/replication_views.go` backed by the
      `*wal.Slots` registry, registered in `initdb.Open` next to
      `pg_stat_replication` / `pg_stat_wal_receiver`. Column
      shape mirrors upstream PG 18.x; columns goopg doesn't
      track yet (`temporary` / `xmin` / `safe_wal_size` /
      `two_phase` / `active_pid`) emit empty / `f` / `0`. WAL
      retention via `MinRestartLSN` picks up logical slots
      automatically — `Slot` shape is shared, no retention
      code change needed. New tests: `TestCreateLogicalSlot`,
      `TestCreateLogicalRequiresPluginAndDatabase`,
      `TestPhysicalSlotJSONUnchangedAcrossM0008`,
      `TestPgReplicationSlotsViewRendersBothKinds`. Reorder
      buffer, snapshot builder, decoder loop, and per-slot
      catalog-xmin retention in vacuum/pruning are all
      deferred to subsequent loops in this milestone.)

- [x] Reorder buffer + decoder orchestration skeleton: per-xact
      queueing, commit-time drain, abort-drop, plus the
      `OutputPlugin` interface that pgoutput will implement.
      Continues in
      `docs/design/0008-0001-logical-decoding-pipeline.md`.
      (landed 2026-04-29: `internal/wal/reorder.go` —
      `ReorderBuffer{Append, Commit, Abort, Active,
      OldestBeginLSN}` keyed by `storage.TransactionID`,
      single-decoder / non-goroutine-safe by design (the decoder
      loop is sequential; wrap in a mutex if it ever moves to a
      goroutine pool). `Change{Kind, LSN, Rel, Block, LineSlot,
      OldTuple, NewTuple}` covers Insert/Update/Delete; xid==0
      Append is rejected to avoid conflating distinct xacts.
      `internal/wal/decoder.go` — `OutputPlugin` interface
      (`Begin(xid, commitLSN)` / `Change(c)` / `Commit(xid,
      commitLSN)`) plus `Decoder.ApplyChange/ApplyCommit/
      ApplyAbort`; `ApplyCommit` drives the plugin in
      `Begin → Change* → Commit` order, unknown xids are no-ops
      (handles catalog-only filter-everything xacts), and
      `ErrNoPlugin` flags a commit-with-changes against a
      decoder configured with no plugin. New tests pin the
      contract: TestReorderBufferCommitDrainsInOrder,
      TestReorderBufferAbortDropsChanges,
      TestReorderBufferIsolatesXacts,
      TestReorderBufferOldestBeginLSN,
      TestReorderBufferRejectsInvalidXID,
      TestDecoderApplyCommitDrivesPlugin,
      TestDecoderAbortSkipsPlugin,
      TestDecoderUnknownCommitIsNoop,
      TestDecoderRequiresPlugin. WAL classifier remains
      deferred — needs new `RecordKindXactCommit` /
      `RecordKindXactAbort` markers + per-record xid plumbing
      so the decoder can be driven from a `RecordIterator`. The
      snapshot builder is also deferred.)

- [x] WAL classifier hookup: introduce `RecordKindXactCommit` /
      `RecordKindXactAbort` records and a `Classify(decoder,
      record)` function that drives `Decoder.Apply*` from any
      record stream. Continues in
      `docs/design/0008-0001-logical-decoding-pipeline.md`.
      (landed 2026-04-29: New WAL record kinds
      `RecordKindXactCommit` (8) and `RecordKindXactAbort` (9)
      with 5-byte `kind|xid` payloads. `EncodeXactCommit`,
      `EncodeXactAbort`, `DecodeXactMarker` round-trip the xid.
      `ApplyRecord` treats both kinds as physical-recovery
      no-ops — they exist purely so the M0008 logical decoder
      can drive its reorder buffer; the existing per-record
      idempotency in HeapInsert/Delete/Vacuum/Btree records
      already brings storage to a consistent state.
      `internal/wal/classifier.go::Classify(d, r)` walks one
      decoded `Record` and dispatches into the `*Decoder`:
      HeapInsert routes by xmin parsed from the encoded tuple
      header (offset 0..3); HeapDelete routes by the xmax
      already in the record payload — no wire-format change to
      the existing logical change records. XactCommit/XactAbort
      route to the corresponding Decoder method; vacuum/btree/
      page-image/checkpoint records are silently skipped. Tests:
      TestClassifyHeapInsertRoutesByXmin,
      TestClassifyHeapDeleteRoutesByXmax,
      TestClassifyAbortDropsXact,
      TestClassifyIsolatesConcurrentXacts,
      TestClassifySkipsNonTxRecords,
      TestEncodeDecodeXactMarker. Wire-layer emission of
      EncodeXactCommit/EncodeXactAbort at executor txn
      boundaries remains deferred — the classifier works against
      synthetic record streams in tests but sees no markers in
      live workloads until the executor wires them in.)

- [x] Wire-layer emission of `EncodeXactCommit` /
      `EncodeXactAbort` at executor transaction boundaries.
      Continues in
      `docs/design/0008-0001-logical-decoding-pipeline.md`.
      (landed 2026-04-29: `mvcc.Manager` grew `XactMarker`
      enum (`XactCommit`/`XactAbort`) and
      `SetXactMarkerLogger(fn func(xid, kind) error)`. Both
      `Commit` and `Rollback` invoke the logger under the
      manager lock before the active-set delete; a logger
      error surfaces back through Commit/Rollback so a
      WAL-append failure stops the txn from finishing and the
      caller can retry. `initdb.Open` installs a hook that
      `walWriter.Append`s `EncodeXactCommit(xid)` /
      `EncodeXactAbort(xid)` — single seam covers every server
      path (simple-query, extended-query, COPY) without
      per-call-site changes. Tests:
      TestXactMarkerLoggerCalledOnCommit/OnRollback,
      TestXactMarkerLoggerErrorAbortsCommit (mvcc),
      TestOpenWiresXactMarkerHook (end-to-end via
      `wal.ReadAll`). The classifier from loop 3 now sees
      real markers in live workloads.)

- [x] Long-lived classifier loop: a goroutine per logical
      slot that consumes a `RecordIterator` and drives a real
      `OutputPlugin`, advancing the slot's `ConfirmedFlushLSN`
      on each commit. Continues in
      `docs/design/0008-0001-logical-decoding-pipeline.md`.
      (landed 2026-04-29: `internal/wal/slot_decoder.go`
      defines `SlotDecoder.Run(ctx)` — owns a
      `*RecordIterator` anchored at the slot's `RestartLSN`
      and a `*Decoder` driving the `OutputPlugin`; loops
      `iter.Next` → `Classify(decoder, rec)` until ctx is
      cancelled or the writer closes. On every record whose
      kind is `RecordKindXactCommit`, the slot's
      `ConfirmedFlushLSN` advances to `rec.EndLSN` so a
      restart resumes from the correct anchor without
      replaying acked transactions. Construction rejects
      non-logical slots. Tests:
      TestSlotDecoderRunDrivesPluginThroughCommit (end-to-end
      with a live writer, async loop, `xid=42`
      insert/insert/commit observed by a thread-safe capture
      plugin, `ConfirmedFlushLSN` advances to commit EndLSN),
      TestSlotDecoderRejectsPhysicalSlot. The snapshot
      builder skeleton stays deferred — needed before a real
      consumer can interpret tuple bytes against schema.)

- [x] Snapshot builder skeleton: slot-creation-time HISTORIC
      snapshot for the logical decoder so plugins can
      interpret tuple bytes against the catalog state in
      effect at slot creation. Continues in
      `docs/design/0008-0001-logical-decoding-pipeline.md`.
      (landed 2026-04-29: new
      `catalog.InMemory.AllTables()` accessor returns deep
      copies of every non-virtual user table in OID order.
      `internal/wal/snapshot.go` introduces:
        - `RelationDef{Schema, Name, OID, Columns}` and
          `ColumnDef{Name, Type, NotNull, Ordinal}` — the
          immutable per-relation snapshot.
        - `CatalogSnapshot` — per-RelOid map; `Lookup(rel)`
          resolves by RelOid (stable across renames); `Len()`
          for observability.
        - `BuildCatalogSnapshot(c)` — captures the current
          catalog state. Mutations after capture cannot leak
          through; the `Drop + recreate` test pin guarantees
          this.
        - Virtual catalog views skipped (they re-register on
          startup).
        - `SlotSnapshot{Catalog, MVCC}` bundles the two
          frozen views a plugin needs at slot start.
      `wal.SlotDecoder` grows a `Snapshot SlotSnapshot` field
      and a `NewSlotDecoderWithSnapshot(...)` constructor; the
      original `NewSlotDecoder` stays for tests that don't
      need schema awareness. Plugins read
      `decoder.Snapshot` once pgoutput (0008-0002) wires the
      consumption path. Tests:
      TestBuildCatalogSnapshotFreezesShape (mutation after
      capture doesn't bleed through),
      TestBuildCatalogSnapshotSkipsVirtualTables,
      TestSnapshotLookupMissingRelation,
      TestNewSlotDecoderWithSnapshotAttachesIt. Full upstream
      `SnapBuild` state machine, schema-change replay across
      slot lifetime, and the per-slot catalog-xmin retention
      hook in vacuum / pruning remain deferred. With this
      slice 0008-0001 has the foundation a real pgoutput
      plugin (0008-0002) can build against.)

- [x] `pgoutput` output plugin: B / C / R / I / D message
      framing wire-compatible with upstream PG 18.x. Replica-
      identity handling. Design doc
      `docs/design/0008-0002-pgoutput-plugin.md`.
      (landed 2026-04-29: `internal/wal/pgoutput.go::PgOutput`
      implements the `OutputPlugin` interface and emits
      pgoutput v1 wire-shapes:
        - B: kind | final_lsn | commit_time | xid (21 bytes).
        - C: kind | flags=0 | commit_lsn | end_lsn |
              commit_time (26 bytes).
        - R: rel_oid | nspname\\0 | relname\\0 | replident |
              nattrs | per-attr (flags | name\\0 | type_oid |
              typmod). Lazy-emitted once per session via an
              `emittedRel` set.
        - I: rel_oid | 'N' | tuple-body.
        - D: rel_oid | 'K' | nliveatts=0 (v0 HeapDelete
              carries no pre-image; apply worker resolves
              the row by (rel, block, slot) lookup).
      Tuple body parsing mirrors
      `executor/codec.go::DecodeRow` byte-for-byte (duplicated
      because `executor` depends on `wal`); supports int4 /
      int8 / bool / text / varchar / numeric / timestamp /
      date. `pgoTypeOIDFor` maps v0 catalog type names to
      upstream `pg_type` OIDs. Replica identity reports DEFAULT
      uniformly (catalog tracking lands with 0008-0003);
      every column flagged as part of REPLICA IDENTITY
      DEFAULT so apply workers' row-resolution path works for
      tables with primary keys. `U` UPDATE intentionally
      deferred — v0 executor emits UPDATE as paired
      HeapDelete + HeapInsert; reorder-buffer fold is its own
      slice. Tests pin the byte shapes for B / C / R / I / D
      plus the relation-once-per-session and unknown-rel-skip
      contracts. With this slice the pipeline can finally
      emit upstream-compatible bytes; the next M0008 work is
      0008-0003 (publication / subscription DDL + catalog).

- [x] Publication / subscription catalog substrate + the five
      system views (first slice of 0008-0003). Design doc
      `docs/design/0008-0003-publication-subscription-ddl.md`.
      (landed 2026-04-29: `internal/catalog/pubsub.go::PubSub`
      in-memory registry with `Publication{Name, OID,
      AllTables, PublishInsert/Update/Delete, Tables}` and
      `Subscription{Name, OID, Conninfo, Publications,
      Enabled, SlotName}`. `Create*`/`Drop*`/`Lookup*`/
      `Publications()`/`Subscriptions()` accessors return deep
      copies; insertion order preserved via name-sorted
      iteration. SlotName defaults to subscription name when
      empty (matches upstream). `Runtime.PubSub` field plumbed
      through `initdb.Open`. Five virtual views registered:
      `pg_publication`, `pg_publication_rel`,
      `pg_publication_tables`, `pg_subscription`,
      `pg_subscription_rel`. Column shapes mirror upstream PG
      18.x; columns goopg doesn't track yet (`pubowner`,
      `pubviaroot`, `subbinary`, `substream`,
      `subtwophasestate`, `prqual`, `prattrs`) emit empty / `f`
      / `{}`; `pg_subscription_rel` emits zero rows (tablesync
      state lives in apply worker). Tests:
      TestPubSubCreatePublicationByTable,
      TestPubSubCreatePublicationAllTables,
      TestPubSubDuplicatePublicationName,
      TestPubSubDropPublication,
      TestPubSubCreateSubscription,
      TestPubSubDuplicateSubscriptionName,
      TestPgPublicationViewRendersRows,
      TestPgSubscriptionViewRendersRows. The catalog substrate
      is now in place; an operator's `\dRp` against goopg
      returns a clean (empty) row set. Parser surface,
      executor wiring, slot provisioning, and persistence land
      in subsequent loops.)

- [x] Publication / subscription parser surface +
      executor / catalog wiring (continues 0008-0003).
      (landed 2026-04-29: New keywords `publication`,
      `subscription`, `connection`, `for`, `tables` (option
      names `publish` / `enabled` / `slot_name` stay plain
      identifiers so they don't collide with column
      references like `pg_stat_replication.slot_name`). New
      AST nodes `CreatePublicationStmt`, `DropPublicationStmt`,
      `CreateSubscriptionStmt`, `DropSubscriptionStmt`. Parser
      dispatch via `parseCreate` / `parseDrop`; new
      `parseCreatePublicationTail`, `parseCreateSubscriptionTail`,
      `parseDropPubSubTail`, and a shared
      `parsePubSubWithList` for `WITH (k = v, ...)` (handles
      string lits, idents, ints, keywords as values; uses the
      `TokenOperator '='` shape since `=` is an operator).
      Planner routes the four new stmts into `*planner.DDL`.
      Executor `ddlOp` grows `execCreatePublication` /
      `execDropPublication` / `execCreateSubscription` /
      `execDropSubscription` that call into
      `Context.PubSub.Create*`/`Drop*`. `executor.Context.PubSub`
      added; threaded from `server.Config.PubSub` (which
      `cmd/goopg start` populates from `Runtime.PubSub`) via
      `dispatch.go` and `dispatch_extended.go`. Tests:
      TestParseCreatePublicationForTable,
      TestParseCreatePublicationForAllTables,
      TestParseCreatePublicationWithPublishOption,
      TestParseDropPublicationIfExists,
      TestParseCreateSubscription, TestParseDropSubscription
      (parser); TestDDLCreatePublicationEndToEnd,
      TestDDLCreateSubscriptionEndToEnd,
      TestDDLDropPublicationIfExists (executor end-to-end).
      With this slice an operator can run
      `CREATE PUBLICATION p FOR TABLE items WITH (publish =
      'insert,delete')` and the change is visible via the
      five virtual catalog views from the prior loop.)

- [x] Apply worker + initial table sync — first slice
      (pgoutput decoder + design doc). Subscriber-side worker
      scaffolding, TCP transport, tablesync state machine,
      DELETE/UPDATE row resolution still deferred. Design doc
      `docs/design/0008-0004-apply-worker-and-tablesync.md`.
      (landed 2026-04-29: `internal/wal/pgoutput_decoder.go`
      delivers `DecodeMessage(payload) (*DecodedMessage,
      error)` — the inverse of 0008-0002's encoder. Output
      types: `DecodedMessage{Kind, XID, CommitLSN, EndLSN,
      CommitTime, Relation, RelOID, NewTuple, OldTuple}`,
      `DecodedRelation{OID, Schema, Name, Replident,
      Columns}`, `DecodedAttr{Flags, Name, TypeOID, TypeMod}`,
      `DecodedColumn{Status, Bytes}`. Reader cursor enforces
      big-endian framing and surfaces `ErrTruncatedMessage` on
      short payloads. Per-kind parsers cover `B` (21 bytes:
      kind | final_lsn | commit_time | xid), `C` (26 bytes:
      kind | flags=0 | commit_lsn | end_lsn | commit_time),
      `R` (rel_oid | nspname\\0 | relname\\0 | replident |
      natts | per-attr fields), `I` (`'N'` action + tuple
      body), `D` (`'K'`/`'O'` action + tuple body). Tuple
      bodies handle `'n'` (NULL), `'t'` (text with
      length-prefix), and `'u'` (unchanged TOAST) status
      bytes. Tests:
      TestPgoutputDecoderRoundTripBegin,
      TestPgoutputDecoderRoundTripCommit,
      TestPgoutputDecoderRoundTripRelationAndInsert (pins
      end-to-end encoder→decoder for a 2-column int4+text
      table; tuple body decodes to `[t:"42", t:"alpha"]`),
      TestPgoutputDecoderRoundTripDelete (empty K body),
      TestPgoutputDecoderRejectsTruncated. With this slice
      the encoder/decoder symmetry is complete and the apply
      worker has its byte-stream reader.)

- [x] ApplyWorker per-event apply path (continues 0008-0004).
      (landed 2026-04-29: `internal/executor/applyworker.go`
      delivers `ApplyWorker{cat, pool, txnMgr, relations,
      currentTx, inXact}` plus `ApplyMessage(m
      *wal.DecodedMessage) (uint64, error)`:
        - B opens a local txn via
          `txnMgr.Begin(ReadCommitted)`; a stray B inside an
          open xact triggers a defensive rollback before
          opening the new one.
        - R caches the remote `DecodedRelation` keyed by
          remote OID and resolves the local
          `*catalog.Table` via `LookupTable({Schema, Name})`.
        - I parses each pgoutput text-format column to a
          Datum (int4 / int8 / bool through
          `parsePgoutputText`; everything else falls back to
          KindString) and writes the row through the same
          `writeHeapRow` helper INSERT uses — so apply lands
          in the same heap-tuple frame and triggers the same
          WAL change records.
        - D no-ops in v0: pgoutput emits an empty K body so
          the row to stamp xmax on isn't identifiable.
          Tracked as a wire-format extension follow-up.
        - C commits the local txn and returns the remote
          commit_lsn so the caller can advance
          confirmed_flush_lsn. A C with no preceding B is
          tolerated as a no-op (catalog-only
          filter-everything xact).
      `SafeRollback()` is the deferrable cleanup that
      prevents open-xid leaks when the streaming driver
      bails. Tests:
      TestApplyWorkerInsertsRowFromPgoutputStream pins the
      end-to-end encoder→decoder→apply path: a publisher's
      `B → R → I → C` for a 2-column int4+text row results
      in the subscriber's local `items` table containing
      exactly one row with id=7, label="alpha" (verified via
      SeqScan). TestApplyWorkerCommitOutsideXactIsNoop and
      TestApplyWorkerInsertWithoutRelationFails pin the
      protocol guards. With this slice the apply path works
      end-to-end against in-process byte streams; TCP
      transport, slot-start handshake, and tablesync state
      machine remain the next M0008 work.)

- [x] TCP transport for the pgoutput stream + slot-start
      handshake (continues 0008-0004).
      (landed 2026-04-29:
      `internal/server/logicalreceiver.go::LogicalReceiver`
      mirrors WalReceiver's structure but feeds each `'w'`
      CopyData payload through `wal.DecodeMessage` →
      `*executor.ApplyWorker.ApplyMessage`. Config:
      `LogicalReceiverConfig{PrimaryAddr, User, SlotName,
      Publications, StartLSN, ProtoVersion=1, Apply,
      StatusInterval, DialTimeout}`.
      `DialLogicalReceiver(ctx, cfg)` performs TCP dial + v3
      startup with `replication=database` + `START_REPLICATION
      SLOT name LOGICAL <lsn> ("proto_version" '1',
      "publication_names" 'p1,p2')` issued via
      `buildStartLogicalReplicationCommand`.
      `Run(ctx)` spins a frame-reader goroutine and dispatches
      via `handleFrame` → `handleCopyData` →
      `wal.DecodeMessage` → `Apply.ApplyMessage`; on every
      commit, the receiver caches the commit LSN as
      `applyLSN` so subsequent standby-status frames advance
      the publisher's `confirmed_flush_lsn`. `sendStatus`
      emits `'r'` standby-status CopyData on every
      StatusInterval tick (default 10s) and on
      reply-requested keepalives. Apply errors trigger
      `Apply.SafeRollback()` so the in-progress xact never
      leaks. Tests:
      TestBuildStartLogicalReplicationCommandShape pins the
      exact wire-level SQL string (including the option
      block); TestBuildStartLogicalReplicationCommandNoPublications
      pins that empty Publications drops the
      `publication_names` option entirely (matches libpq's
      "all visible publications" fallback);
      TestLogicalReceiverHandleCopyDataAppliesPgoutputAndAdvancesLSN
      drives `handleCopyData` directly with B → R → I → C
      pgoutput bytes wrapped in `'w'` CopyData payloads and
      confirms the receiver's `ApplyLSN()` advances to the
      commit LSN. Real publisher-side wiring (the walsender
      recognising logical slots and emitting pgoutput bytes
      through `'w'` frames in place of WAL bytes) is the next
      M0008 piece.)

- [x] Publisher-side walsender support for logical slots
      (continues 0008-0004).
      (landed 2026-04-29:
      `internal/server/replication.go::parseStartReplicationArgs`
      grew Mode + Options fields and now handles
      `START_REPLICATION SLOT name LOGICAL X/X ("key" 'value'
      [, ...])`. Mode={PHYSICAL,LOGICAL}; LOGICAL requires SLOT.
      Option-block parser splits libpq's quoted-pair syntax
      respecting single-quote-quoted commas via
      `splitStartReplicationOptionList`. Empty Options and
      missing keyword still default to PHYSICAL so existing
      callers don't break. `replyStartReplication` dispatches
      LOGICAL into the new
      `internal/server/logicalwalsender.go::runLogicalWalsender`,
      which:
        - Snapshots the catalog at session start via
          `wal.BuildCatalogSnapshot` so pgoutput resolves
          relations against a stable shape.
        - Wraps the FrameWriter in
          `walsenderPgoutputAdapter`, an `io.Writer` that
          turns each pgoutput message (one Write call per
          Begin/Commit/Change) into one `'w'` CopyData
          frame with monotonic startLSN / endLSN — exactly
          the shape the subscriber's LogicalReceiver
          consumes.
        - Builds a `wal.PgOutput` against the snapshot +
          adapter, then drives it through
          `wal.NewSlotDecoderWithSnapshot` so the slot's
          `ConfirmedFlushLSN` advances on every commit.
        - Spawns a receive-side goroutine that consumes
          standby-status CopyData frames from the standby
          (mirrors the PHYSICAL path) so the subscriber's
          ack also drives retention.
        - Cleanly returns on ctx cancel / CopyDone /
          Terminate / wal.ErrClosed; other errors flow
          through `writeStreamingError`.
      New tests:
      TestParseStartReplicationArgsLogical (full
      command + option block round-trip);
      TestParseStartReplicationArgsLogicalRequiresSlot
      (LOGICAL without SLOT rejected);
      TestParseStartReplicationArgsPhysicalStillWorks
      (existing PHYSICAL grammar still parses);
      TestWalsenderPgoutputAdapterWrapsAsCopyData (each
      Write becomes one `'w'` CopyData frame with monotonic
      LSNs that round-trip through DecodeReplicationMessage).
      With this slice the publisher and subscriber both speak
      the LOGICAL wire shape end-to-end. The remaining gap is
      that the publisher-side walsender's pgoutput stream
      currently emits whatever the slot decoder sees — there's
      no publication-membership filter yet (FOR TABLE t1, t2
      doesn't restrict the wire). Hooking the publication-
      filter into the pgoutput writer is a small follow-up.)

- [x] Publication-membership filter on the publisher's
      logical walsender (continues 0008-0003 / 0008-0004).
      (landed 2026-04-29: `wal.RelationFilter` interface
      and `PgOutput.SetFilter` gate every Change emission
      against publication membership *and* per-kind
      publish-flag rules. Filter-rejected changes emit
      nothing — neither `R` nor `I`/`D`. Server-side
      `publicationFilter` (in `logicalwalsender.go`) is
      built from the slot's `publication_names` option:
      iterates the subscribed publications, ORs publish-
      flag bitsets across every publication that covers a
      relation, and keeps a fast-path for FOR ALL TABLES
      publications. Unknown publication names are silently
      skipped (lenient v0 behavior; upstream rejects them
      at CREATE SUBSCRIPTION time). `relQualifiedName`
      renders rels as "schema.name" so the lookup is
      byte-equal with what `catalog.PubSub.Tables` stores.
      `splitPublicationNames` parses libpq's
      'p1,p2,p3' option value with whitespace trimming +
      empty-entry drops. `runLogicalWalsender` installs
      the filter via `SetFilter` when both the option list
      is non-empty and the runtime has a PubSub registry;
      missing either path leaves the filter nil so the
      old "ship everything" behaviour is preserved.
      Tests: TestPgOutputFilterSuppressesEmission and
      TestPgOutputFilterPerKind in the wal package pin the
      filter contract on PgOutput;
      TestPublicationFilterAllowsByTable,
      TestPublicationFilterAllTables,
      TestPublicationFilterRespectsPublishFlags,
      TestPublicationFilterUnionAcrossPublications,
      TestPublicationFilterUnknownPublicationSkipped,
      TestSplitPublicationNamesTrimsAndDropsEmpty in the
      server package pin the publication-derivation rules
      and the `publication_names` parser. With this slice
      a subscriber's `CREATE SUBSCRIPTION s CONNECTION ...
      PUBLICATION p` actually only receives changes from
      `p`'s tables — the wire matches what publication
      DDL configured.)

- [x] Tablesync state machine — catalog substrate
      (`pg_subscription_rel.srsubstate`). (landed 2026-04-29:
      `catalog.PubSub` gained a `SubscriptionRel { SubID,
      RelOID, State, LSN }` row type with state constants
      `SubRelStateInit="i"`, `SubRelStateDataCopy="d"`,
      `SubRelStateSyncDone="s"`, `SubRelStateReady="r"` and
      a monotonic-only validity map: `i→{i,d,s}` (the i→s
      shortcut covers tables that were empty at slot start),
      `d→{d,s}`, `s→{s,r}`, `r→{r}` — every reversal and
      every illegal jump returns
      `ErrInvalidSubRelStateTransition`. `AddSubscriptionRel`
      seeds (subName, relOID) at state `i`;
      `AdvanceSubscriptionRel` validates the transition and
      `max()`-merges LSN so concurrent paths cannot rewind
      sync progress. `DropSubscription` clears
      `subRels[name]` so a re-CREATE under the same name
      doesn't inherit stale rows. Accessors:
      `LookupSubscriptionRel`,
      `SubscriptionRels(subName)`, `AllSubscriptionRels()`.
      `internal/initdb/replication_views.go` wires the
      `pg_subscription_rel` virtual view to render rows
      from `AllSubscriptionRels()` (`srsublsn` formatted
      via `formatLSN`, blank when LSN==0). Tests:
      TestSubscriptionRelStateMachineHappyPath (i→d→s→r
      with LSN bumps), TestSubscriptionRelStateMachineRejectsBackwards
      (i→r illegal jump and r→d reversal both rejected),
      TestSubscriptionRelInitToSyncDoneShortcut,
      TestSubscriptionRelLSNMonotonic (smaller LSN ignored),
      TestSubscriptionRelDropSubscriptionClearsRels,
      TestAddSubscriptionRelDuplicate. Built and full
      `go test ./...` green. The catalog/SQL surface for
      tablesync is now ready; the actual TCP-driven COPY
      and the apply-worker hookup that drives transitions
      remain as the next two M0008 slices below.)

- [x] Tablesync initial COPY transport — wire-shape driver.
      (landed 2026-04-29: `internal/server/tablesync.go`
      ships `RunTableSync(TableSyncConfig)` that drives a
      publisher → subscriber `COPY <rel> TO STDOUT`
      exchange against a `protocol.FrameReader` /
      `FrameWriter` pair. The exchange: send simple
      `Query("COPY <rel> TO STDOUT")`; await
      `CopyOutResponse` (and on receipt advance
      `pg_subscription_rel.srsubstate` from `i` → `d`,
      idempotent if already at `d`); loop reading
      `CopyData` frames, splitting each payload on `'\n'`
      and forwarding terminated COPY-TEXT lines to a
      `LineWriter` interface (the subset of
      `executor.CopyFromExecutor` we need —
      `PushLine(line []byte) error` plus
      `RowsInserted() int64`, defined in the server
      package to keep the import direction one-way); on
      `CopyDone` drain the trailer
      (`CommandComplete` + `ReadyForQuery`, with
      tolerance for an EOF after `CopyDone` that some
      publishers emit) and advance `d` → `s`.
      `ErrorResponse` mid-stream is unwrapped via the
      'M'/'C' fields and surfaced to the caller; an
      out-of-shape frame at any point yields a typed
      error and leaves the row at whatever state it
      reached. The state-machine row is seeded
      idempotently with `AddSubscriptionRel`
      (`ErrSubscriptionRelExists` is fine — a previous
      attempt may have advanced past `i`). LSN is left at
      0; the apply-worker integration that drives `s` →
      `r` based on commit LSN is a separate slice.
      Tests in `internal/server/tablesync_test.go`:
      TestRunTableSyncHappyPath (3-row exchange, exact
      byte-for-byte round-trip of COPY-TEXT lines + Query
      frame inspection + final state==s),
      TestRunTableSyncMultiRowFrame (multiple rows in a
      single CopyData frame still produce one PushLine
      per row), TestRunTableSyncAdvancesIToDOnCopyOutResponse
      (state advances to `d` even before any CopyData,
      so an interrupted sync is observable in catalog),
      TestRunTableSyncRowApplyErrorLeavesStateAtD
      (PushLine error stops the sync and keeps state at
      `d`), TestRunTableSyncResumesFromExistingRow
      (a row pre-existing at `d` is not reset to `i`,
      and the second attempt drives to `s`),
      TestRunTableSyncRejectsUnexpectedFrame
      (RowDescription before CopyOutResponse → typed
      error, state stays at `i`). Built and full
      `go test ./...` green. The remaining gap is the
      caller wiring: a per-subscription manager that
      dials the publisher, walks
      `SubscriptionRels(subName)` looking for non-`r`
      rows, and invokes `RunTableSync` for each. That
      lives in the next bullet alongside the apply-worker
      `s` → `r` driver.)

- [x] Apply-worker tablesync integration. (landed
      2026-04-29: `executor.ApplyWorker` gained an optional
      tablesync context (`SetSubscriptionContext(ps,
      subName)`) and two new behaviours on top of the
      existing B/R/I/D/C dispatch. (1) Per-change gating in
      `applyInsert`: when the worker has a subscription
      context and the rel's `pg_subscription_rel.srsubstate`
      is anything other than `r`, the INSERT is skipped
      silently — the tablesync worker is responsible for
      seeding that data via the COPY path, and applying it
      again here would double-write. The local OID is
      resolved through `cat.RelFileNode(local).RelOid` so
      the gate keys off the same identifier the tablesync
      transport used. A rel with no `subRels` row is
      treated as ungated (apply normally) so existing
      tests that don't model tablesync keep passing. (2)
      Per-commit promotion in `applyCommit` (renamed loop
      now also runs after a commit-only sequence with no
      open xact): `promoteSyncedRels` walks
      `SubscriptionRels(subName)` and, for every row at
      state `s` whose recorded sync-end LSN ≤ the just-
      observed commit LSN, advances it to `r` via
      `AdvanceSubscriptionRel`. v0's tablesync transport
      records LSN=0 (no per-snapshot handoff yet), so the
      first commit observed promotes the rel — conservative
      but correct given COPY happens on the same publisher
      timeline as the streaming apply. Mirrors upstream
      worker.c's `process_syncing_tables_for_apply` and
      `should_apply_changes_for_rel`. Tests in
      `internal/executor/applyworker_test.go`:
      TestApplyWorkerSkipsChangesForRelInTablesync (rel at
      `d` causes INSERT to be dropped; SeqScan sees zero
      rows; state stays at `d`),
      TestApplyWorkerPromotesSyncDoneToReadyOnCommit (rel
      at `s` with LSN=0xCAFE; commit at 0xFEED promotes to
      `r`; a subsequent INSERT then applies because the
      rel is `r`),
      TestApplyWorkerCommitWithoutPromotionLeavesUncrossedRelAtS
      (rel at `s` with LSN=0xFFFFFFFF; commit at 0x100
      doesn't promote — apply hasn't crossed the sync
      boundary yet). Existing tests
      (TestApplyWorkerInsertsRowFromPgoutputStream,
      TestApplyWorkerCommitOutsideXactIsNoop,
      TestApplyWorkerInsertWithoutRelationFails) still
      green via the no-context default. Helpers
      `pgoutputBIRC` and `driveStream` factor the encoder
      and chunker boilerplate so the new tests stay
      readable. Built and full `go test ./...` green. With
      this slice the apply path correctly cooperates with
      tablesync: rels in copy phase are filtered, and
      crossing the sync boundary promotes them so streaming
      takes over without double-applying.)

- [x] Tablesync caller wiring — per-subscription manager.
      (landed 2026-04-29: `internal/server/tablesync_manager.go`
      ships `RunTableSyncManager(ctx, cfg)` which walks
      `SubscriptionRels(subName)`, skips rows already at
      state `r`, and for every other row drives one
      initial-COPY exchange via `RunTableSync`. The
      manager is structured around three injected
      callbacks so it stays free of TCP/dial logic and
      remains directly testable with in-memory frame
      buffers: `ResolveRel(relOID) → "schema.name"` (turns
      a tracked OID into the qualified relname the
      publisher will see on its `COPY <name> TO STDOUT`),
      `OpenConn(ctx, relOID) → ConnPair{Reader, Writer,
      Close}` (one fresh authenticated query connection
      per rel; closer always called before moving on),
      and `OpenWriter(ctx, relOID) → WriterPair{Writer
      LineWriter, Close}` (one per-rel apply target;
      typically wraps `*executor.CopyFromExecutor` once
      the DDL substrate to translate
      `pg_subscription_rel.srrelid` into a local
      `*catalog.Table` lands). Per-rel errors land on
      the returned `[]TableSyncResult` (one entry per
      visited rel, with `RelOID`, `Relname`,
      `InitialState`, `FinalState`, `Rows`, and `Err`);
      the manager continues to the next rel rather than
      aborting, mirroring upstream's behaviour where one
      stuck table doesn't block the rest. Function-level
      error is reserved for systemic missing-config
      cases. Streaming `s → r` promotion is left to the
      apply-worker (which already does the right thing
      on commit-LSN crossover). Tests:
      TestRunTableSyncManagerWalksUnsyncedRels (sub
      with three rels at i/d/r; manager touches only
      the first two and both reach s while the r row is
      untouched, including no callback invocations for
      it), TestRunTableSyncManagerOneFailureDoesNotAbortRest
      (OpenConn fails for one rel; siblings still reach
      s; failed rel keeps state i),
      TestRunTableSyncManagerUnresolvedRelnameSkips
      (ResolveRel ok=false aborts that rel before
      OpenConn fires), TestRunTableSyncManagerNoUnsyncedRelsReturnsEmpty
      (all-r subscription returns empty results and
      never invokes any callback). Built and full
      `go test ./...` green. Plumbing the production
      composition (LogicalReceiver opens the per-rel
      query conn, executor.CopyFromExecutor opens the
      per-rel writer, and the manager's results feed
      observability) is the next slice — once
      logical-replication observability lands in
      `0008-0005`, the manager hookup will compose
      naturally with both.)

- [x] Logical-replication observability — `pg_stat_subscription`
      substrate. (landed 2026-04-29:
      `internal/wal/subscriber_mon.go` ships the in-process
      `Subscribers` registry mirroring the publisher-side
      `Senders`/`Receivers` pattern. `SubscriberWorkerType`
      enum covers `leader` (one per active subscription) and
      `tablesync` (one per non-`r` rel); `parallel` reserved
      for a future loop. Each `Subscriber` carries identity
      (subID/subname/workerType/pid/leaderPID/relOID, set
      once at Register) plus atomic-backed LSN counters
      (`receivedLSN`, `latestEndLSN`) and timestamp pairs
      (`lastMsgSendTime`, `lastMsgReceiptTime`,
      `latestEndTime`) so the high-frequency advance path is
      lock-free per entry. `AdvanceReceivedLSN(lsn)` and
      `MarkMessage(now, endLSN)` are monotonic-clamped via
      `advanceLSN` — stale frames cannot rewind progress.
      `Snapshot()` returns rows sorted by
      `(subname, worker_type, relid, pid)` so a repeated
      SELECT against a quiet subscription returns identical
      bytes. `LatestEndTime` stays at Go's zero (rendered
      blank) until the first `MarkMessage` with a non-zero
      endLSN — the snapshot path explicitly guards against
      the `time.Unix(0,0)` epoch sentinel that would
      otherwise pollute the view with a 1970 timestamp.
      `internal/initdb/replication_views.go::registerStatSubscriptionView`
      installs `pg_catalog.pg_stat_subscription` with the
      upstream PG 18.x columns (`subid`, `subname`,
      `worker_type`, `pid`, `leader_pid`, `relid`,
      `received_lsn`, `last_msg_send_time`,
      `last_msg_receipt_time`, `latest_end_lsn`,
      `latest_end_time`); `leader_pid`/`relid` blank on
      leader rows, `latest_end_time` blank until first
      MarkMessage. Wired into `initdb.Open` next to the
      existing replication views; `Runtime.WalSubscribers`
      exposes the registry to apply/tablesync workers.
      Tests: TestSubscribersRegisterUnregisterRoundTrip,
      TestSubscriberWorkerTypeDefaults,
      TestSubscriberLSNMonotonic,
      TestSubscribersSnapshotSorted,
      TestSubscriberMarkMessageUpdatesTimestamps (registry
      contract); TestStatSubscriptionRendersRegisteredSubscribers
      (view rendering for a leader + tablesync pair, blank-
      column rules, sort order, post-Unregister vanish).
      Design doc
      `docs/design/0008-0005-logical-replication-observability.md`
      added and indexed in `docs/design/README.md`. Built
      and full `go test ./...` green. Logical-replication
      rows on `pg_stat_replication` are already covered by
      the existing publisher-side walsender registry — no
      additional work needed there.)

- [x] Logical-replication observability — apply-worker /
      tablesync-manager hookup. (landed 2026-04-29:
      `executor.ApplyWorker` gained `SetStatHandle(*wal.Subscriber)`;
      when set, `ApplyMessage` calls `MarkMessage(time.Now(),
      m.EndLSN)` on every frame (so `last_msg_send_time` /
      `last_msg_receipt_time` / `latest_end_lsn` /
      `latest_end_time` move forward in lock-step with the
      publisher's stream) and `applyCommit` calls
      `AdvanceReceivedLSN(m.CommitLSN)` so `received_lsn`
      reflects the highest committed LSN. Caller owns the
      `Register`/`Unregister` lifecycle. `server.TableSyncConfig`
      gained an optional `Stat *wal.Subscriber`; when set,
      `RunTableSync` calls `MarkMessage(time.Now(), 0)` on
      every CopyData frame so an operator selecting
      pg_stat_subscription mid-sync sees freshness. The
      manager (`RunTableSyncManager`) gained an optional
      `OpenStat(ctx, relOID) → *StatPair{Stat, Close}`
      callback that lets the caller register a tablesync
      worker per visited rel; the manager always invokes
      `Close` (typically `subs.Unregister`) — including on
      failure — so the registry never accrues stale rows.
      Plumbing is opt-in (nil disables observability),
      preserving the legacy "apply everything, no
      visibility" path that existing tests rely on. Tests:
      TestApplyWorkerStatHandleAdvancesOnCommit (commit
      LSN flows into received_lsn; B/C EndLSN flows into
      latest_end_lsn; LastMsgReceiptTime non-zero after
      first ApplyMessage), TestRunTableSyncManagerRegistersStatHandle
      (mid-sync probe sees a tablesync worker registered
      with the right relOID/worker_type and a stamped
      LastMsgReceiptTime; post-sync registry is empty),
      TestRunTableSyncManagerStatClosedOnFailure (StatPair
      .Close runs even when the per-rel sync errors).
      Built and full `go test ./...` green. With this
      slice the observability surface is now real: a live
      apply leader + a live tablesync worker register
      themselves and `pg_stat_subscription` reflects their
      progress in real time.)

- [x] Structured replication-event logging. (landed
      2026-04-29: extends the existing `slog` +
      `internal/wal/repllog.go` event-constant pattern from
      M0005 rather than adding a parallel
      `wal.ReplicationLogger` interface — the M0005
      walreceiver / retention paths already use
      `*slog.Logger` with `"event", wal.EventXxx` plus
      structured key/value pairs, and dashboards alert on
      that vocabulary; the logical-replication slice
      reuses it. New event constants in `repllog.go`:
      `EventApplyCommit`, `EventApplyError`,
      `EventTablesyncStarted`, `EventTablesyncCompleted`,
      `EventTablesyncStateChange`. `executor.ApplyWorker`
      gained `SetLogger(*slog.Logger)`; `applyCommit`
      emits `event=apply_commit` with sub/xid/lsn; the
      per-message error path emits `event=apply_error`
      with sub/kind/rel_oid/lsn/err; `promoteSyncedRels`
      emits `event=tablesync_state_change` with
      from=s/to=r/lsn on each successful promotion.
      `server.TableSyncConfig.Logger` and
      `server.TableSyncManagerConfig.Logger` plumb the
      same logger through the tablesync transport;
      `RunTableSync` emits `tablesync_started` at entry,
      `tablesync_state_change` at every actual `i→d` and
      `d→s` transition (the i→d line is suppressed when
      the previous state was already `d` — re-entry on a
      partial sync), and `tablesync_completed` with the
      row count at exit. nil logger falls back to
      `slog.Default()` everywhere so existing tests don't
      need to opt in. Tests:
      TestApplyWorkerLogsCommitAndPromotion (B/R/I/C
      through an apply worker with a tablesync rel at `s`
      produces both `apply_commit` with `lsn:12648430`
      and `tablesync_state_change` from=s to=r);
      TestRunTableSyncLogsLifecycle (happy-path sync of
      two rows produces tablesync_started, both
      state-change lines, and `tablesync_completed` with
      `rows:2`). Design doc
      `docs/design/0008-0006-structured-replication-event-logging.md`
      added and indexed in `docs/design/README.md`. Built
      and full `go test ./...` green. The first draft
      introduced a parallel `wal.ReplicationLogger`
      interface; rejected for the slog/repllog.go pattern
      so the M0005 and M0008 event vocabularies share one
      grep target.)

## Milestone 0009 — AIO subsystem (asynchronous I/O)

See `docs/milestones/0009-aio-subsystem.md`.

- [x] AIO core: `internal/aio/` package exposing the
      submit/wait API plus the `worker` and `sync` methods.
      Design doc `0009-0001-aio-core.md`. (landed
      2026-04-29: `internal/aio/aio.go` ships the public
      surface — `Direction` (read/write), `File` interface
      (io.ReaderAt + io.WriterAt seam — production callers
      pass `*os.File`; tests use an in-memory `memFile`),
      `Op` (File/Buffer/Offset/Direction/optional
      Callback), `Result` (N + Err), `Handle` (Wait blocks
      until completion, idempotent), `Method` interface
      (Submit/Close/Name), `Engine` (front door + atomic
      counters + Stats snapshot), `EngineConfig`
      (Method/Workers/MaxConcurrency), `NewEngine`
      factory. Two methods delivered:
      `internal/aio/method_sync.go` runs every I/O on the
      calling goroutine with optional buffered-semaphore
      backpressure when MaxConcurrency>0;
      `internal/aio/method_worker.go` is a goroutine pool
      that drains a bounded submission channel (channel
      doubles as work queue + concurrency bound — one
      allocation per Submit instead of two; default depth
      `4×workers` when MaxConcurrency==0). Submission
      blocks once the channel is full so a misbehaving
      caller cannot allocate unbounded queue depth.
      `MethodIOUring` is reserved — `NewEngine` returns
      `ErrUnsupportedMethod` until the Linux-only
      follow-up lands. Engine counters: Submitted /
      Completed / Errored / InFlight. `io.EOF` does NOT
      count as Errored (matches upstream's "EOF is
      expected" semantics). GUCs registered in
      `internal/config/defaults.go` (all
      ContextPostmaster, mirroring upstream): `io_method`
      enum sync/worker/io_uring (default `worker`),
      `io_workers` (default 3 = upstream default, range
      1..1024), `io_max_concurrency` (default 0 = "let
      the method decide", range 0..1024). GUCs registered
      but not yet consumed by any caller — that wiring is
      the follow-up. Engine-closed Submits return a
      Handle whose Wait yields the engine-closed error
      rather than deadlocking. Tests in
      `internal/aio/aio_test.go`:
      TestEngineSyncReadWriteRoundTrip (foundational
      write+read round-trip, counters bookkeeping),
      TestEngineSyncCallback (optional Callback sees same
      Result as Wait), TestEngineSyncWaitIdempotent
      (double-Wait returns same Result no block),
      TestEngineWorkerParallelExecution (100 concurrent
      writes through 4 workers / cap=8 commit cleanly,
      InFlight settles to 0),
      TestEngineWorkerSubmitAfterCloseSurfacesError
      (close-vs-submit race surfaces typed error rather
      than deadlock), TestNewEngineRejectsIOUring +
      TestNewEngineRejectsUnknown (ErrUnsupportedMethod),
      TestEngineSyncReadEOF (EOF surfaces but doesn't
      bump Errored). Built and full `go test ./...`
      green.)

- [x] AIO `io_uring` method (Linux-only). Runtime probe
      of `io_uring_setup(2)` availability, fall back to
      `worker` when the probe fails. Selecting
      `io_method = io_uring` on a Linux host where the
      probe succeeds drives I/Os visible to
      `strace -e io_uring_*`. Design doc
      `docs/design/0009-0006-aio-io-uring.md`.
      (landed 2026-04-29: `internal/aio/method_iouring_linux.go`
      reimplements upstream `pgaio_uring.c` directly against
      raw syscalls 425/426 — no third-party wrapper. SQ/CQ
      rings are mmap'd from the kernel-supplied byte offsets
      (`mmap MAP_SHARED|MAP_POPULATE` × 3); SQEs and CQEs are
      Go structs that mirror the kernel UAPI byte-for-byte
      (`ioSqe` 64 B, `ioCqe` 16 B). Submit serialises SQE
      writes under `submitMu` (io_uring's SQ is single-
      producer), bumps the tail with a release-store, and
      kicks via `io_uring_enter(submit=1)`. A dedicated
      reaper goroutine blocks in `io_uring_enter(0, 1,
      GETEVENTS)` and drains CQEs into the per-handle map
      (`pending[userData]`) — idle ring costs one parked
      goroutine, no busy spin. `Close` wakes the reaper out
      of GETEVENTS by submitting an `IORING_OP_NOP` SQE with
      sentinel user_data `0xFFFF...FFFF`; closing the ring
      fd alone does not reliably unblock the syscall on
      every kernel. Slot semaphore caps in-flight ops at
      `min(MaxConcurrency, sqEntries)` so Submit blocks
      naturally under saturation. The probe is the
      `io_uring_setup` call inside `newMethodIOUring`: any
      errno returns the sentinel `errProbeFailed`, which
      `NewEngine` matches via `errors.Is` and falls back to
      MethodWorker. `Engine.FallbackFrom() (requested,
      reason)` exposes the fallback so `cmd/goopg start`
      can log `event=aio_method_fallback requested=io_uring
      actual=worker reason=...` post-Open. Non-Linux hosts
      ship a stub (`method_iouring_other.go`) that always
      returns `errProbeFailed` — same fallback behaviour.
      Fd exposure: optional unexported interface
      `fdHaver { Fd() uintptr }`; `*os.File` satisfies it
      natively, and `relFile` / `aioFileAdapter` /
      `walAIOFileAdapter` add forwarding methods so the
      storage and WAL caller chains preserve fd visibility
      through their adapters. In-memory test files don't
      satisfy `fdHaver` and Submit runs the I/O inline via
      `runOp` (correct, just no kernel acceleration). Tests:
      `TestNewEngineIOUringConstructs` (cross-platform —
      io_uring always constructs, FallbackFrom reports
      correctly), `TestEngineIOUringReadWriteRoundTrip`
      (Linux-only — pwrite-then-pread through the engine
      against a real tmpfile, bytes round-trip, per-
      direction counters bump, in-flight gauge returns to
      zero), `TestEngineIOUringParallel` (Linux-only — 64
      concurrent writes + 64 reads against the same file,
      verify no slot collisions in the userData→Handle
      map). Both Linux-only tests `t.Skip` when
      `e.Method() != MethodIOUring` so the suite passes on
      any Linux host regardless of `io_uring_disabled` or
      seccomp. Built and full `go test ./...` green on
      Linux 6.6 (io_uring honoured). Registered files,
      SQPOLL, linked ops, and io_uring-specific latency
      histograms remain follow-up slices.)

- [x] Read-stream API on top of the AIO core. (landed
      2026-04-29: `internal/aio/read_stream.go` ships the
      predictive-prefetch surface. Public types:
      `NextBlockFunc func() int64` (returns next byte
      offset or the `EndOfStream = -1` sentinel),
      `ReadStreamConfig { Engine, File, BlockSize,
      NextBlock, Lookahead }`, `ReadStream` with `Next()`
      and `Close()`. `NewReadStream` validates the config
      (non-nil Engine / File / NextBlock + positive
      BlockSize → typed errors), clamps Lookahead to
      `[1, MaxReadStreamLookahead=256]` so a pathological
      caller can't allocate unbounded buffer memory (zero
      falls back to `DefaultReadStreamLookahead=4`), and
      primes the prefetch window via up to Lookahead
      `Engine.Submit` calls. Every `Next` blocks on the
      head prefetch's Wait, returns the block's bytes
      (truncated to the underlying ReadAt's reported byte
      count, slice aliases the stream's internal buffer
      and is valid until the next Next/Close call), and
      refills the window. `io.EOF` is the trailing
      sentinel returned exactly once after NextBlock has
      signalled `EndOfStream` AND the queue has drained.
      `Close` waits for in-flight prefetches to land
      rather than abandoning them so the engine's
      `InFlight` counter stays honest (cancellation will
      arrive post-`io_uring`; until then drain is the
      only correct exit). Operates on `File`+offsets
      rather than buffer-manager `Buffer` handles —
      mirrors upstream `read_stream.h`'s shape but keeps
      the abstraction reusable for non-heap-scan
      prefetchers (ANALYZE sample reads, vacuum's free-
      space-map walk). Two backpressure layers stack: the
      per-stream Lookahead window AND the engine's global
      `io_max_concurrency` cap (Submit blocks naturally
      when hit, so the stream's window can shrink under
      contention without violating the cap). Deferred:
      contiguous-merge ("io_combine_limit"), sequential
      ramp-up, `Reset()` for restartable scans. Tests in
      `internal/aio/read_stream_test.go`:
      TestReadStreamSequentialRoundTrip (4-block stream
      at Lookahead=2 round-trips bytes in callback order
      + trailing EOF + Submitted=4),
      TestReadStreamLookaheadCapsConcurrentSubmits (a
      `gateFile` that blocks every ReadAt until released
      lets the test sample the engine's `InFlight`
      counter mid-stream; asserts it never exceeds
      Lookahead),
      TestReadStreamHonoursDefaultLookahead (zero falls
      back to 4), TestReadStreamClampsHugeLookahead
      (10×Max → MaxReadStreamLookahead),
      TestReadStreamRejectsInvalidConfig (each of nil
      Engine / File / NextBlock + zero BlockSize → typed
      error), TestReadStreamSurfacesPerBlockError (empty
      file + non-zero offset → io.EOF on the per-block
      result, mirroring io.ReaderAt's contract),
      TestReadStreamCloseDrainsInFlight (post-Close
      InFlight=0). Built and full `go test ./...` green.
      Design doc `docs/design/0009-0002-read-stream.md`
      added and indexed in `docs/design/README.md`.)

- [x] AIO engine lifecycle + storage integration
      substrate. (landed 2026-04-29:
      `internal/storage/smgr.go` ships
      `storage.AIOEngine` (narrow `Submit(AIOSubmitOp)
      AIOHandle` interface keeping internal/storage free
      of an internal/aio import) plus
      `storage.AIOSubmitOp{File AIOFile, Buffer, Offset}`
      and `storage.AIOFile` (ReadAt-only — prefetch never
      writes). `Manager.SetAIO(eng)` attaches the engine,
      `Manager.PrefetchBlock(rel, blk, buf) (AIOHandle,
      error)` submits via the engine when set and falls
      back to a `preCompletedHandle{n, err}` (Wait never
      blocks) when not — caller Submit/Wait path is
      identical regardless. `relFile.ReadAt` makes
      relfiles satisfy `AIOFile`; the per-relfile mutex is
      shared between Pin/Read paths so PrefetchBlock
      doesn't race the *os.File cursor. Out-of-range
      blocks return a pre-completed handle whose Wait
      surfaces ErrShortRead — matches ReadBlock. Engine
      lifecycle wired via three new `OpenOptions` fields
      (AIOMethod / AIOWorkers / AIOMaxConcurrency); when
      set, `initdb.Open` calls `aio.NewEngine`, surfaces
      it on `Runtime.AIO`, attaches it to the storage
      manager, and closes it AFTER the storage manager in
      `Runtime.Close()` so in-flight prefetches drain.
      Engine adapter (`aioEngineAdapter` /
      `aioFileAdapter` / `aioHandleAdapter`) lives in
      initdb so internal/storage stays import-free of
      internal/aio; the file adapter's WriteAt panics
      because PrefetchBlock is read-only — any DirWrite
      flowing through would be a contract violation, not
      graceful fallback. `cmd/goopg start` reads
      `io_method` / `io_workers` / `io_max_concurrency`
      from the registry via two new helpers
      (`stringGUC` / `intGUC`); `io_uring` is silently
      downgraded to `worker` with a warn-level
      `event=aio_method_fallback` log line until the
      runtime probe lands. Tests in storage:
      TestPrefetchBlockSyncFallback (no engine — pre-
      completed handle returns read bytes),
      TestPrefetchBlockUsesAttachedEngine (recording
      engine sees the right offset, bytes round-trip),
      TestPrefetchBlockOutOfRange (ErrShortRead surface).
      Tests in initdb: TestOpenAttachesAIOEngineWhenMethodSet,
      TestOpenLeavesAIONilWithoutMethod. Built and full
      `go test ./...` green. Design doc
      `docs/design/0009-0003-aio-storage-integration.md`
      added and indexed; milestone-required
      `0009-0003-aio-checkpointer-and-wal.md` and
      `0009-0004-aio-observability.md` renumber to
      `0009-0004` and `0009-0005` respectively when they
      land — recorded in the new doc's "Numbering note"
      section.)

- [x] AIO heap-scan caller integration. (landed
      2026-04-29: `storage.Pool` gained
      `SetPrefetchEnabled(bool)` and `Prefetch(tag)`. With
      prefetching enabled, Prefetch checks the byTag map
      under poolMu and — on a miss — calls
      `Manager.PrefetchBlock` with a throwaway buffer; the
      AIO handle is dropped on the floor (the engine's
      worker goroutine completes the read in the
      background, the buffer warms the kernel page cache
      via the pread syscall, and the buffer is then GC'd).
      Default-off so synchronous deployments don't pay for
      an inline pread we'd repeat on the subsequent Pin.
      `initdb.Open` flips it on after attaching an AIO
      engine. Errors swallowed — a failed prefetch must
      not impact correctness; the subsequent Pin will
      surface any real I/O error. `executor.seqScanOp`
      gained a `prefetchedThru` cursor and a
      `refillPrefetchWindow` helper that keeps
      `seqScanLookahead = 4` blocks ahead of curBlock
      prefetched via Pool.Prefetch. The window is primed
      on Open and topped up after every block advance, so
      the next-but-N block is being read by the AIO engine
      while the consumer decodes the current page. With
      Pool.Prefetch disabled the loop is cheap (atomic
      load + early return) so existing tests that don't
      model AIO are unaffected. Tests in storage:
      TestPoolPrefetchDisabledIsNoOp (default-off — no
      submits even with engine attached),
      TestPoolPrefetchEnabledFiresThroughEngine (one
      Submit per non-cached Prefetch; cached-tag Prefetch
      is a no-op). Tests in executor:
      TestSeqScanFiresPrefetchesAcrossBlocks (recording
      engine attached + Pool.SetPrefetchEnabled(true);
      insert 600 rows spanning multiple blocks; flush +
      InvalidateRel so the SeqScan's Prefetch hits the
      not-cached path; assert engine.submits ∈ [1,
      nBlocks] after a full scan that returns all 600
      rows). Built and full `go test ./...` green. The
      bitmap-heap-scan, ANALYZE-sample, and direct read-
      stream-driven seqScan paths remain follow-up
      slices; the substrate they all sit on is now in
      place.)

- [x] AIO checkpointer + WAL writer caller integration —
      write-side substrate. (landed 2026-04-29:
      `storage.AIOSubmitOp` gained a `Direction` field
      (`AIODirRead` / `AIODirWrite`; the AIODirRead zero
      value preserves the prefetch-only semantics so
      existing callers see the same behaviour). `AIOFile`
      now requires both `ReadAt` and `WriteAt`; the
      `relFile` type satisfies both via mutex-guarded
      forwards to `*os.File`. New
      `Manager.WriteBlockAIO(rel, blk, buf) (AIOHandle,
      error)` submits a write through the engine with
      `Direction=AIODirWrite`, falls back to a
      synchronous `writeBlock` + `preCompletedHandle`
      when no engine is attached, and rejects
      out-of-range blocks via Wait — mirrors WriteBlock's
      "extend through Extend, not the write path"
      contract. The `aioEngineAdapter.Submit` in initdb
      now honours `op.Direction` (DirRead/DirWrite);
      `aioFileAdapter.WriteAt` actually forwards to the
      storage file (it previously panicked when
      PrefetchBlock was read-only). The recording engine
      fake in storage tests was extended to dispatch on
      Direction. Tests:
      TestWriteBlockAIOSyncFallback (no engine — bytes
      round-trip via WriteBlock fallback),
      TestWriteBlockAIOUsesAttachedEngine (recording
      engine sees one submit with Direction=AIODirWrite;
      bytes round-trip via the WriteAt path),
      TestWriteBlockAIORejectsOutOfRange (past-nblocks →
      descriptive Wait error). Read-side regression-
      checked unchanged. Built and full `go test ./...`
      green. Design doc
      `docs/design/0009-0005-aio-checkpointer-and-wal.md`
      added and indexed in `docs/design/README.md`.)

- [x] AIO checkpointer caller integration — Pool.FlushAllPaced
      hot-path wiring. (landed 2026-04-29:
      `internal/storage/bufpool.go` reshaped FlushAllPaced
      from a per-slot serial loop into a batched WAL-flush
      + parallel AIO-submit + Wait loop. New
      `Pool.SetAsyncFlushBatchSize(n)` + `MaxFlushBatchSize`
      cap (256). flushBatchSize() returns max(1, configured)
      so callers that don't opt in see the legacy serial
      loop unchanged. The new batched path: phase 1 takes
      contentMu RLocks across the batch (writers can't tear
      pwrite-d bytes); phase 2 runs ONE wal.FlushUpTo
      against the batch's max pd_lsn (preserves WAL-before-
      data — every page in the batch has pd_lsn ≤ maxLSN);
      phase 3 submits every data write through
      Manager.WriteBlockAIO collecting handles; phase 4
      Waits on every handle (drains all even on first
      error so engine InFlight stays coherent); phase 5
      clears dirty bits where tag still matches. With no
      engine attached, WriteBlockAIO returns a pre-
      completed handle synchronously so the loop runs
      writes serially — bit-equivalent to legacy. Pacing
      semantics widened: pacer fires per-batch instead of
      per-slot (the M0002 smoothed-checkpoint contract is
      preserved at batch=1 = serial; at batch>1 the same
      'fraction completed' value is computed across the
      batch). `initdb.Open` calls
      `pool.SetAsyncFlushBatchSize(8)` after attaching the
      engine (small enough to keep checkpoint pacing
      ticks reasonable, large enough to keep io_workers=3
      busy under load — a future GUC can expose this).
      flushSlot is unchanged for the eviction-path
      callers (Pin / PinNew evict-and-flush) — those keep
      their per-slot serial behaviour. Tests in storage:
      TestFlushAllPacedBatchedSubmitsThroughEngine
      (3 dirty slots + recording engine + batch=4 → 3
      submits all DirWrite; bytes round-trip after
      Invalidate+Pin),
      TestFlushAllPacedBatchSizeOneEquivalentToLegacy
      (default batch=0 → serial path still flushes a
      dirty slot correctly with no engine),
      TestSetAsyncFlushBatchSizeClamps (-1 → 1, 10×Max →
      Max). The exec recording-engine fake was updated to
      dispatch on Direction (DirWrite → File.WriteAt) so
      TestSeqScanFiresPrefetchesAcrossBlocks's flush
      step still round-trips bytes. Built and full
      `go test ./...` green.)

- [x] AIO WAL writer caller integration — wal.state.writeAt
      shaped to flow per-segment writeback through the
      engine. (landed 2026-04-29: `wal.Config.AIO` adds an
      optional engine seam; matching `wal.AIOEngine` /
      `AIOSubmitOp` / `AIOFile` / `AIOHandle` /
      `AIODirection` interfaces mirror the storage-side
      shapes so internal/wal stays import-free of
      internal/aio. `state.aio` mirrors `Config.AIO`;
      `state.writeAt` Submit→Wait through the engine when
      set, falls back to direct `f.WriteAt` when nil.
      Commit-path durability barrier is unchanged: every
      Submit Waits inline (single-threaded writer loop),
      so by the time `flushUpTo` calls `dataSync`/fdatasync
      every byte ≤ the requested LSN has already been
      pwrite'd. WAL writes now appear in `pg_aios` /
      `pg_stat_aio` alongside heap writes — engine, reads,
      heap writes, and WAL writes all flow through one
      pool. `initdb.Open` builds a `walAIOEngineAdapter`
      (parallel to the storage adapter, keeping each
      package free of internal/aio) and threads it through
      `wal.Config.AIO` when an engine is attached. Tests:
      TestWriterAppendNoAIOPreservesLegacyPath (no engine
      → bytes land on disk via direct `f.WriteAt`),
      TestWriterAppendAIOFlowsThroughEngine (engine
      attached → every writeAt-chunk flows through Submit
      with Direction=AIODirWrite; bytes still round-trip
      via the segment file). Built and full
      `go test ./...` green.

      What's deferred: real perf benefit. The WAL writer's
      single-threaded loop means inline Wait gives no
      pipelining — Append n still blocks on Append n's
      pwrite. Restructuring the writer to defer Wait
      across multiple Appends (so commit n+1 doesn't wait
      on commit n's pwrite) is a follow-up that requires
      changing the loop's serialisation model. The
      observability + symmetry win is meaningful; the
      perf win waits on the loop redesign.)

- [x] AIO observability — aggregate counters + startup
      log line (first slice). (landed 2026-04-29:
      `internal/initdb/aio_views.go::registerStatAIOView`
      installs `pg_catalog.pg_stat_aio` backed by
      `*aio.Engine.Stats()`. Columns: `method`
      (sync/worker) + monotonic counters `submitted` /
      `completed` / `errored` / `in_flight`. Single-row
      when an engine is attached, zero-row when none is —
      SELECT works on synchronous deployments without a
      missing-table error. EOF is excluded from `errored`
      per the engine's "EOF is expected" semantics
      (matches Engine.completionBookkeeping's EOF guard).
      Wired into `initdb.Open` next to the existing
      replication / pubsub views; the registration site
      handles a nil engine gracefully so the view can be
      registered unconditionally. `cmd/goopg start` emits
      a structured `event=aio_engine_attached
      method=worker workers=N max_concurrency=N` slog
      line right after Open returns when `rt.AIO != nil`,
      mirroring upstream's "AIO method = ..." startup-log
      shape. The existing `event=aio_method_fallback`
      warn-level line covers the io_uring-requested-but-
      unavailable case. Tests in initdb:
      TestStatAIOViewEmptyWithoutEngine (nil engine → 0
      rows; view still SELECTable),
      TestStatAIOViewReflectsEngineCounters (sync method
      + zero counters pre-Submit; one Submit+Wait bumps
      submitted/completed to 1; in_flight back to 0).
      Built and full `go test ./...` green. Design doc
      `docs/design/0009-0004-aio-observability.md` added
      and indexed in `docs/design/README.md`.)

- [x] AIO observability — per-I/O `pg_aios` view +
      per-handle tracking. (landed 2026-04-29:
      `internal/aio/aio.go` gained per-handle tracking on
      the engine. `Engine.nextID` (atomic.Uint64) hands
      out monotonic per-Submit identifiers; `Engine.inflight`
      is a `map[uint64]inFlightEntry` guarded by an
      `inflightMu sync.RWMutex` (acquired only on Submit
      / completion — never on the I/O hot path, which
      stays on atomic counters). `Handle` gained `id` and
      `submittedAt` fields. New `Engine.registerInFlight(h,
      op)` is called by both methods after building the
      Handle and before kicking off the actual I/O so a
      Snapshot taken concurrently always sees the entry.
      `Engine.finishHandle(h, r)` replaces the old
      `completionBookkeeping(r)` — counters + inflight-map
      removal + Handle.finish in one call. Both methods
      (sync, worker) wired through. New
      `Engine.InFlight() []InFlightInfo` returns a sorted
      copy (sort key: ID, monotonic by submission order)
      so consumers see rows in stable oldest-first order.
      `internal/initdb/aio_views.go::registerPgAiosView`
      installs `pg_catalog.pg_aios` backed by
      `Engine.InFlight()`. Columns: `io_id`, `operation`
      (read/write via Direction.String), `off`, `length`,
      `submitted_at` (RFC3339Nano UTC), `elapsed_us`
      (now − submittedAt in microseconds). Zero rows when
      no engine is attached or no Ops are outstanding.
      Wired into `initdb.Open` next to `pg_stat_aio`.
      Tests in aio: TestEngineInFlightSnapshot (a new
      `gateFileWith` blocks every ReadAt so the test
      observes two outstanding Ops via `Engine.InFlight()`
      mid-flight; asserts ID monotonicity, direction,
      offset, length; post-Wait the inflight map is
      empty). Tests in initdb:
      TestPgAiosViewEmptyWithoutEngine (nil engine → 0
      rows; SELECT works on synchronous deployments),
      TestPgAiosViewReflectsInFlightHandles (gated worker
      method blocks two reads; view returns two rows with
      the right operation / offset / length columns;
      post-Wait the view returns 0 rows). Built and full
      `go test ./...` green. Wait events ("waiting on
      AIO completion" surfaced through pg_stat_activity-
      shaped surface), per-direction / per-relation
      counter breakdowns, and latency histograms remain
      follow-up slices — all unblocked now that per-
      handle tracking is in place.)

- [x] AIO `pg_aios.target_desc` — file-path identity on
      every outstanding I/O. (landed 2026-04-29: `aio.Op`
      gained a free-form `Target string` field; the
      engine's `inFlightEntry` and the public
      `InFlightInfo` surface it. `storage.AIOSubmitOp`
      and `wal.AIOSubmitOp` both gained matching `Target`
      fields and the initdb adapters propagate them.
      `Manager.PrefetchBlock` and `Manager.WriteBlockAIO`
      stamp `f.path` on every submit; `wal.state.writeAt`
      stamps `f.Name()` on every WAL segment write. The
      `pg_aios` view grew a `target_desc` column rendering
      that string. Empty strings render blank — preserves
      the "no target" case for callers that don't set it.
      Tests: TestEngineInFlightTarget (engine snapshot
      surfaces caller-supplied Target),
      TestPrefetchBlockPopulatesTarget (storage read path
      stamps the relfile path),
      TestWriteBlockAIOPopulatesTarget (storage write
      path stamps the relfile path). Built and full
      `go test ./...` green. With this slice, every row
      in pg_aios is human-attributable to a specific WAL
      segment or relation file rather than just a numeric
      offset.)

- [x] AIO observability — per-direction counter breakdown.
      (landed 2026-04-29: `aio.Engine` gained six new
      atomic counters splitting submitted / completed /
      errored by `Direction` (read vs write). `Submit`
      bumps the right `read*` or `write*` Submitted at
      submission time; `finishHandle` bumps the
      Completed and (for non-EOF errors) Errored
      counterparts at completion time. The aggregate
      `submitted/completed/errored` counters still
      reflect the sum so existing consumers are unchanged.
      `Stats` gained `ReadSubmitted`, `ReadCompleted`,
      `ReadErrored`, `WriteSubmitted`, `WriteCompleted`,
      `WriteErrored` fields — sum invariant
      (Submitted == ReadSubmitted + WriteSubmitted) is
      pinned by test. `pg_stat_aio` reshaped to a
      two-row-per-engine view: one row per direction
      (`operation` column = "read" / "write") with the
      per-direction submitted / completed / errored
      counts. Mirrors upstream's `pg_stat_io` row shape.
      `in_flight` is the engine's aggregate (renders on
      the read row; the write row reports 0 — splitting
      InFlight by direction is a future refactor).
      Updated TestStatAIOViewReflectsEngineCounters to
      pin the new shape (2 rows, post-Submit only the
      read row's counters move). New
      TestEngineStatsPerDirection exercises both
      directions and verifies the aggregate-sum
      invariant. Built and full `go test ./...` green.)

- [x] AIO observability — per-direction latency
      (avg + max). (landed 2026-04-29: aio.Engine carries
      four new atomic counters
      `read{Latency,Latency}Sum/MaxMicros` (+ write
      counterpart). `finishHandle` computes
      `time.Since(h.submittedAt).Microseconds()` once and
      Add()s to the per-direction SumMicros and CAS-
      monotonic-clamps via a new `advanceMax(*atomic.Uint64,
      uint64)` helper for the per-direction MaxMicros.
      `Stats` exposes `ReadLatencySumMicros`,
      `ReadLatencyMaxMicros`, `WriteLatencySumMicros`,
      `WriteLatencyMaxMicros`. `pg_stat_aio` grows
      `avg_latency_us` (computed sum/count via the
      `avgLatencyUS` helper, returns "0" when count==0
      to avoid NaN) and `max_latency_us` columns —
      rendered per-direction-row alongside the existing
      counters. Tests: TestEngineStatsLatency
      (non-flaky cross-direction independence + sum ≥
      max sanity check), TestEngineStatsLatencyMaxMonotonic
      (50 ops; MaxMicros never regresses). Built and
      full `go test ./...` green. With this slice an
      operator can spot per-direction tail-latency
      regressions in pg_stat_aio without attaching a
      profiler. Histogram / percentile (p50, p95, p99)
      observability is a future slice — needs a
      streaming-quantile estimator on the engine.)

- [x] AIO observability — per-relation counter breakdown
      (`pg_stat_aio_targets`). (landed 2026-04-29:
      `aio.Engine` gained a `targets sync.Map[string,
      *targetStats]` keyed by `Op.Target`; per-target
      counters (`submitted`, `completed`, `errored`,
      `latencySumMicros`, `latencyMaxMicros`, `bytes`)
      are atomic so updates don't take a lock — only the
      first I/O for a new target pays the LoadOrStore
      allocation. Submit and finishHandle update the
      target-keyed stats alongside the existing
      per-direction counters. Empty `Op.Target` is
      ignored (no "" entry pollutes the view). New
      `Engine.PerTarget() []TargetStats` returns a
      lex-sorted snapshot.
      `internal/initdb/aio_views.go::registerPgStatAIOTargetsView`
      installs `pg_catalog.pg_stat_aio_targets` with
      columns `target`, `submitted`, `completed`,
      `errored`, `bytes`, `avg_latency_us` (sum/count via
      the existing `avgLatencyUS` helper, "0" when
      count==0), `max_latency_us`. Wired into
      `initdb.Open` next to `pg_aios`. Tests in aio:
      TestEnginePerTargetTracking (3 ops across 2
      targets → 2 rows in name order with right
      submitted/completed/bytes per row),
      TestEnginePerTargetEmptyTargetIgnored (empty
      Target doesn't create a row). Tests in initdb:
      TestPgStatAIOTargetsViewEmptyWithoutEngine (nil
      engine → 0 rows), TestPgStatAIOTargetsViewRendersRows
      (two targets across read+write produce two rows in
      lex order with correct columns). Built and full
      `go test ./...` green. With this slice an operator
      can SELECT from pg_stat_aio_targets and see
      exactly which relfile / WAL segment is dominating
      I/O.)

- [ ] (BLOCKED) AIO wait-event surface: register a
      "waiting on AIO completion" wait event so a query
      stalled on an AIO Wait shows up identifiably in the
      pg_stat_activity-shaped surface. The fix_plan
      previously claimed this composes with "the existing
      wait-event registry from M0002", but no such
      registry was actually built — `pg_stat_activity` and
      the wait-event vocabulary aren't implemented yet in
      goopg. Building that surface is itself a meaningful
      milestone-scoped slice (probably its own
      pg-stat-activity / wait-event milestone). This AIO
      hookup remains queued behind it.

## Milestone 0010 — WAL direct I/O & walsender memory handoff

See `docs/milestones/0010-wal-direct-io-and-walsender-memory-handoff.md`
for the full DoD. Decomposed into the three design-doc seams the
milestone calls out (`0010-0001`, `0010-0002`, `0010-0003`); pick the
topmost unchecked item.

- [x] WAL direct-I/O write path — Phase 1 (GUC, probe,
      plumbing, fallback observability). New `wal_direct_io`
      GUC (TypeBool, default `off`, ContextPostmaster).
      `wal.Config.DirectIO` field; `loadState` runs
      `probeDirectIO(walDir)` once at construction when
      DirectIO=true. Probe opens `<walDir>/.wal_direct_io_probe`
      with `O_RDWR|O_CREAT|O_DIRECT`, observes EINVAL /
      EOPNOTSUPP and returns a human-readable fallback
      reason (or empty on success). Linux-only
      `internal/wal/direct_io_linux.go`; non-Linux stub in
      `internal/wal/direct_io_other.go` always falls back
      (mirrors the M0009 io_uring stub). Phase 1 does NOT
      flip O_DIRECT on segment opens — that's Phase 2. The
      probe outcome is plumbed end-to-end:
      `Writer.DirectIORequested()` /
      `Writer.DirectIOFallbackReason()`, `cmd/goopg start`
      reads the GUC into `OpenOptions.WALDirectIO` and emits
      `event=wal_direct_io_active` (probe ok) or
      `event=wal_direct_io_fallback reason=...` (probe
      rejected) — mirrors the M0009 `event=aio_*` shape so
      operators grep one vocabulary across both subsystems.
      Tests: TestDirectIODisabledByDefault (probe skipped
      when GUC off), TestDirectIOEnabledProbesFilesystem
      (probe runs + outcome plumbed correctly per GOOS),
      TestDirectIOFallbackReasonStable (idempotent reads).
      Design doc
      `docs/design/0010-0001-wal-direct-io-write-path.md`
      indexed; spans both phases with Phase 2 explicitly
      marked deferred. (landed 2026-04-29.)

- [x] WAL direct-I/O write path — Phase 2 (M0010-0001b):
      flip O_DIRECT on segment fds, alignment-safe per-write
      RMW, aligned scratch via `unix.Mmap`. (landed
      2026-04-29: `state.directIOActive` snapshots
      `directIORequested && fallback==""` at construction.
      `enableDirectIO(f)` uses `fcntl(F_SETFL,
      flags|unix.O_DIRECT)` to flip the flag on the live fd
      AFTER preallocation finishes — preallocation's 64-KiB
      heap-buffer zero-fill can't satisfy O_DIRECT alignment.
      `state.writeAt` dispatches to `state.writeAtDirectIO`
      when active: pread the aligned region (`alignDown`/
      `alignUp` bracket around the user bytes) into the
      per-state mmap'd scratch, overlay user bytes, pwrite
      the full aligned region back. Past-EOF reads (legacy
      lazy-grow case) zero-pad the tail. `directIOScratchCap
      = 1 MiB`; outsized writes loop through the scratch.
      Aligned scratch is lazy-allocated on first write via
      `unix.Mmap(MAP_PRIVATE|MAP_ANONYMOUS)` (mirrors
      `internal/storage/arena.go`); freed in `state.close`.
      AIO+DirectIO bypasses the engine and uses synchronous
      RMW — engine-side aligned-copy is a perf-only follow-
      up (`Phase 2.b` in the design doc). Block size hard-
      coded at 4 KiB; STATX_DIOALIGN-driven detection
      deferred. Walreceiver's WAL-persist path inherits via
      the shared writer fd (no separate code path).
      Tests: TestDirectIORoundTripWithPreallocation
      (three appends + flush + ReadAll round-trip via
      RMW; SegmentSize=4 KiB),
      TestDirectIORecordSpanningBlocks (12 KiB payload
      across ~3 block boundaries; every byte round-trips).
      Both `t.Skip` on probe fallback. Crash-restart
      correctness rides on existing
      `TestPreallocatedSegmentRecoversCleanly`: the byte
      stream is identical under O_DIRECT vs buffered. Built
      and full `go test ./...` green. Design doc
      `docs/design/0010-0001-wal-direct-io-write-path.md`
      updated with Phase 2 details.)

- [x] Walsender in-memory WAL handoff. (landed 2026-04-29:
      new `wal.MemRing` (`internal/wal/mem_ring.go`):
      fixed-size byte ring keyed by 0-based LSN-byte
      position. `wal_sender_memory_buffer` GUC (TypeInt,
      default 16 MiB, range `[0, 1 GiB]`, ContextPostmaster);
      0 disables. `state.append` calls
      `s.memRing.Append(writePos, record)` AFTER
      `state.writeAt` succeeds — a failed pwrite must not
      appear in the ring. `RecordIterator.readBytesAt`
      consults `it.writer.MemRing().ReadAt(pos, out)` first;
      on hit returns from RAM (no syscall), on miss falls
      through to the legacy per-segment pread loop. Single
      `sync.RWMutex`: writers Lock during memcpy + head/tail
      advance, readers RLock during their own memcpy so a
      concurrent eviction can't free bytes mid-read.
      `hits` / `misses` are atomic counters bumped per
      `ReadAt`. Eviction is FIFO: when `tail - head > cap`,
      head advances to `tail - cap`. Partial-overlap reads
      return `(0, false)` so the caller fetches the whole
      range from disk in one go (no two-source stitching).
      Reset path handles post-recovery first-Append at
      non-zero `writePos`; oversized Appends keep only the
      trailing `cap` bytes. `cmd/goopg start` logs
      `event=wal_sender_memory_buffer_attached
      capacity_bytes=N` at startup when configured. Tests:
      TestMemRingNilSafe, TestMemRingRoundTripWithinCap,
      TestMemRingEvictsOldBytesOnOverflow,
      TestMemRingPartialOverlapMisses, TestMemRingWraps,
      TestMemRingWriteLargerThanCap,
      TestMemRingConcurrentReads (4 readers vs 100 writes —
      pins the lock-during-memcpy invariant);
      TestIteratorReadsFromMemRing (ring on + segment file
      deleted post-Append → iterator still streams, proving
      RAM source; Hits()>0),
      TestIteratorFallsBackToDiskWithoutRing (ring off +
      segment intact → pread succeeds, legacy path
      unchanged). Built and full `go test ./...` green.
      Design doc
      `docs/design/0010-0002-walsender-in-memory-wal-handoff.md`
      indexed. Walsender's
      `pg_stat_replication.send_buffer_*` counter surface
      remains follow-up under M0010-0003.)

- [x] WAL direct-I/O observability + operations. (landed
      2026-04-29: new `pg_catalog.pg_stat_wal_io` virtual
      view (`internal/initdb/wal_io_views.go`): one row per
      attached WAL writer, eight columns —
      `direct_io_active` (t/f when DirectIO requested AND
      probe succeeded), `direct_io_fallback_reason`,
      `direct_writes` (lifetime `writeAtDirectIO` regions
      via O_DIRECT pwrite), `tail_rmw_writes` (subset where
      the user range wasn't block-aligned),
      `send_buffer_capacity_bytes`,
      `send_buffer_bytes_resident`, `send_buffer_hits`,
      `send_buffer_misses`. Counter wiring: new
      `directIOCounters` struct (atomic `directWrites` /
      `tailRMWWrites`) shared between Writer and state via
      pointer; `state.writeAtDirectIO` bumps both per-
      region (RMW counter only when `userStart!=regionStart
      || userEnd!=regionEnd`). New `Writer.DirectIOWrites()`
      / `Writer.TailRMWWrites()` accessors. New
      `MemRing.BytesResident()` for the resident column.
      `pg_stat_replication` extended with the same trio
      (cluster-wide values on every per-sender row) so
      operators see ring health alongside per-sender lag in
      one query — `registerStatReplicationView` gains a
      `*wal.Writer` parameter, plumbed through `Open`. The
      M0009-0006 / M0010-0001 startup log lines already
      established the `event=...` vocabulary; this slice
      adds the SQL surface that mirrors them. Tests:
      TestStatWALIOEmptyWithoutWriter (nil writer → 0 rows
      — pins the "view exists, just empty" contract),
      TestStatWALIORendersWriterCounters (capacity matches
      GUC; bytes_resident moves with appends; column shape
      stable), TestStatWALIOActiveWhenProbeOk (probe ok →
      active=t; 3-byte Append bumps direct_writes AND
      tail_rmw_writes; `t.Skip` on probe fallback). Built
      and full `go test ./...` green. Operator playbook
      ("when to enable", "when not to", "how to size the
      ring") in the design doc
      `docs/design/0010-0003-wal-direct-io-observability-and-operations.md`.)

## Milestone 0011 — B-tree NUMERIC key support

See `docs/milestones/0011-btree-numeric-key-support.md` for the full
DoD. Decomposed into the three design-doc seams the milestone calls
out (`0011-0001`, `0011-0002`, `0011-0003`); pick the topmost
unchecked item.

- [x] B-tree NUMERIC key ordering: byte encoding + comparator
      contract for variable-length numeric keys. Design doc
      `docs/design/0011-0001-btree-numeric-key-ordering.md`.
      (landed 2026-04-29: new
      `internal/access/btree.EncodeNumericKey(mantissa int64,
      scale int8) []byte` produces a scale-invariant sortable byte
      string. Layout: `sign(1) || biased exp(4 BE) || normalized
      digits || terminator(1)`. Sign byte (`0x00` neg, `0x01`
      zero, `0x02` pos) settles cross-sign cases; exponent is
      biased big-endian (inverted for negatives so bigger E sorts
      smaller); digits have trailing zeros stripped and are
      emitted as ASCII (inverted `'0'+(9-d)` for negatives);
      terminator is `0x00` for positives / `0xFF` for negatives
      so prefix-equal-but-shorter sorts on the correct side.
      Zero is the single-byte sentinel `[0x01]`. Numerically-
      equal `(m, s)` pairs (e.g. `(10,1)` and `(100,2)`, both
      `1.0`) produce identical bytes — required for
      UNIQUE/PRIMARY KEY equality. `CompareKeys` simplified to
      straight `bytes.Compare` (length-first comparison was
      wrong for variable-length keys; int4 keys are all 4 bytes
      so behaviour unchanged for the existing path). Encoding
      bound ≤ 25 bytes. Tests:
      TestEncodeNumericKeyZeroIsSingleByte,
      TestEncodeNumericKeyScaleInvariance,
      TestEncodeNumericKeySignOrder,
      TestEncodeNumericKeyMonotone (23-element sorted slice
      assertion), TestEncodeNumericKeySamePrefixDifferentLengths,
      TestEncodeNumericKeyMinInt64. Built and full `go test
      ./...` green. The DDL acceptance, build/uniqueness path,
      and variable-length `BTPageOpaque.HighKey` adjustment land
      in M0011-0002.)

- [x] B-tree NUMERIC build + UNIQUE/PRIMARY KEY (M0011-0002).
      Design doc
      `docs/design/0011-0002-btree-numeric-build-and-uniqueness.md`.
      (landed 2026-04-29: `BTPageOpaque.HighKey` becomes
      `[]byte` in memory and a 32-byte variable slot
      (`MaxHighKeyLen=32`) on disk with a 2-byte length prefix
      at offset 14 replacing the old `_padding`; opaque grows
      24→48 bytes; `btreeVersion` bumps 2→3 (pre-GA, older
      btrees error with `ErrNotABTree`). Split path's
      length-equality guard replaced by a `MaxHighKeyLen`
      upper bound. `createSingleColumnBTreeIndex` accepts
      `numeric` / `decimal` via new `isSupportedBTreeKeyType`
      predicate. `backfillSingleColumnBTree` switches dedup
      from `map[int32]struct{}` to `map[string]struct{}`
      keyed on the encoded bytes so `(10,1)` and `(100,2)`
      collapse for UNIQUE. New shared helper
      `encodeBTreeKeyForColumn(v, col, pos)` — int4 path
      unchanged, numeric path uses `EncodeNumericKey`
      (`KindInt` promoted to scale=0). `indexScanOp.lookupKey`
      calls the same helper so probe and stored keys match
      bit-for-bit. Tests:
      TestDDLCreateNumericBTreeIndexAcceptsType,
      TestDDLNumericUniqueIndexCollapsesScales (1.0 / 1.00 →
      23505), TestDDLNumericIndexSplitWithVariableLengthHighKey
      (600-row mantissa/scale-varying insert forces leaf
      split with non-uniform key lengths),
      TestDDLInt4IndexUnchangedRegressionGuard. Full
      `go test ./...` green. NumericConst-driven IndexScan
      planner enablement and HammerDB end-to-end validation
      are M0011-0003.)

- [x] HammerDB TPC-H NUMERIC index validation (M0011-0003).
      Design doc
      `docs/design/0011-0003-hammerdb-tpch-numeric-index-validation.md`.
      (landed 2026-04-29: two pieces. (1) Planner —
      `planIndexScanFromWhere` accepts `*NumericConst` on the
      rhs of `col = const` so equality queries on NUMERIC
      columns emit `IndexScan` rather than `SeqScan +
      Filter`. (2) Integration test
      `internal/executor/tpch_numeric_index_test.go` brings
      up the eight TPC-H tables via `tpch.DDL()` +
      `tpch.SampleInserts()` and runs the 13 single-column
      NUMERIC CREATE INDEX statements mirroring HammerDB's
      `pgolap.tcl` lines 511-544 (region_pk … idx_lineitem_
      orderkey_fkidx). All 13 land cleanly. Sanity probe:
      `btree.Search(EncodeNumericKey(k, 0))` round-trips on
      `orders_pk`. End-to-end probe:
      `SELECT o_orderkey FROM orders WHERE o_orderkey = 4`
      now plans to IndexScan (verified via
      `planContainsIndexScan` walking past Project/Filter
      wrappers) and returns exactly one row.
      `TestTPCHCompositeIndexStillRejected` pins the v0
      single-column boundary — the four HammerDB composite
      indexes still surface `0A000 only single-column btree
      indexes are supported`. Full `go test ./...` green.
      M0011 closes — `bench/tpch/run_all.sh` reaches the
      "CREATING TPCH INDEXES" stage without the int4-only
      restriction; full HammerDB completion needs composite
      indexes which are separate scope.)

## Milestone 0012 — Lock manager + deadlock detection

See `docs/milestones/0012-lock-manager-and-deadlock-detection.md`
for the full DoD. Decomposed into the three design-doc seams the
milestone calls out (`0012-0001`, `0012-0002`, `0012-0003`); pick
the topmost unchecked item.

- [x] Lock manager v0 surface (M0012-0001). Design doc
      `docs/design/0012-0001-lock-manager-architecture.md`.
      (landed 2026-04-29: new `internal/lockmgr` package with
      eight upstream lock modes (`AccessShareLock` …
      `AccessExclusiveLock`, numeric values matching
      `lockdefs.h`), `conflictTab[1..8]` ported verbatim from
      `postgres/src/backend/storage/lmgr/lock.c LockConflicts[]`,
      relation-level `LockTag{DB, Rel}`, per-tag `lockState`
      with `holders map[BackendID]Mask` + FIFO `waiters
      []*Waiter` + cached `granted` Mask, lazy-alloc + GC
      empties. `Acquire(ctx, b, t, m)` grants immediately
      iff no conflict against `grantedExcept(b)` AND no
      waiters queued (second check enforces strict
      head-of-line FIFO); else parks on a buffered signal
      chan; cancellation splices waiter under `lm.mu` and
      handles the Release-promoted-during-cancel race.
      `Release` runs FIFO wake-pass head-first.
      `ReleaseAll(b)` is the txn-end hook. Single coarse
      `sync.Mutex` (per-tag striping deferred). Self-conflict
      impossible — `grantedExcept(b)` excludes requester's
      own holdings. `ConflictsWith(m, held)` exported for
      M0012-0002. Tests:
      TestLockConflictMatrixMatchesUpstream (exhaustive 8-mode
      cross-check), TestLockManagerNoConflictGrantsImmediately,
      TestLockManagerCompatibleModesCoexist,
      TestLockManagerConflictBlocksAndWakesOnRelease,
      TestLockManagerSelfDoesNotConflict,
      TestLockManagerIdempotentAcquire,
      TestLockManagerWaiterCancellationCleansUp,
      TestLockManagerReleaseAllWakesEveryone,
      TestLockManagerFIFOFairness,
      TestLockManagerGCEmptiesState. Race-clean
      (`go test -race`). Full `go test ./...` green.
      Deadlock detection (M0012-0002) and executor
      integration (M0012-0003) build on this surface.)

- [x] Wait-for graph + deadlock detection (M0012-0002).
      Design doc
      `docs/design/0012-0002-deadlock-detection-algorithm.md`.
      (landed 2026-04-29: new
      `internal/lockmgr/deadlock.go` with `runDeadlockCheck`
      (timer target), `CheckDeadlocksNow` (synchronous test
      hook), `findCycle` (iterative 3-colour DFS over
      waiter→conflicting-holder edges), and
      `cancelVictimLocked` (splice + signal). Victim policy:
      youngest backend in the cycle (max `BackendID`).
      Cancellation contract: `Waiter` grew a `victim chan
      struct{}`; the cancelled `Acquire` selects on
      signal/victim/ctx.Done and returns
      `ErrDeadlockDetected` on victim, then calls
      `ReleaseAll` to free other holdings. Scheduling: every
      parked Acquire arms a `time.AfterFunc(
      deadlockTimeout, runDeadlockCheck)`; default 1s,
      `SetDeadlockTimeout` for tests; concurrent fires
      serialise on `lm.mu`. Tests:
      TestDeadlockDetectsTwoSessionCycle,
      TestDeadlockYoungestBackendIsVictim,
      TestDeadlockDetectsThreeSessionCycle (A→B→C→A multi-
      edge),
      TestDeadlockNoCycleNoCancel (false-positive guard),
      TestDeadlockTimerSchedulesCheck (real AfterFunc path,
      no synchronous trigger), TestDeadlockSurvivorMakesProgress.
      Race-clean. Full `go test ./...` green.
      `ErrDeadlockDetected` → SQLSTATE `40P01` translation
      lands with executor integration in M0012-0003.)

- [x] Executor integration + multi-session test matrix
      (M0012-0003). Design doc
      `docs/design/0012-0003-lock-wait-integration-and-test-matrix.md`.
      (landed 2026-04-29: `executor.Context` grew
      `LockMgr *lockmgr.LockManager` + `BackendID
      lockmgr.BackendID` (both nil-safe). New helper
      `Context.acquireRelLock(rel, mode)` is the single
      funnel: nil-LockMgr → no-op; `ErrDeadlockDetected` →
      `*ExecError{Code:"40P01", Message:"deadlock
      detected"}`; ctx-cancel → `*ExecError{Code:"57014"}`.
      Wired into 5 operators: seqScanOp /indexScanOp.Open
      take AccessShareLock; insertOp/updateOp/deleteOp.Open
      take RowExclusiveLock. `server.Config` grew `LockMgr`;
      `Server` grew `nextBackendID atomic.Uint32`;
      `dispatch.go` plumbs both into the Context and calls
      `LockMgr.ReleaseAll(backendID)` from the deferred
      txn-end block. Tests in
      `internal/executor/lock_integration_test.go`:
      TestExecutorAcquireHelperNilLockMgr (regression guard
      for fixture compatibility), TestExecutorAcquireHelperGrantsLock,
      TestExecutorDeadlockTwoSession (A↔B cycle —
      ErrDeadlockDetected → 40P01 mapping pinned),
      TestExecutorDeadlockThreeSession (A→B→C→A multi-edge,
      exactly one 40P01, BackendID 3 = youngest cancelled),
      TestExecutorNonDeadlockContention (linear waiter chain
      — no false-positive 40P01). Race-clean. Full
      `go test ./...` green. M0012 closes — DDL paths
      (DROP/ALTER), catalog-level locks, `lock_timeout` GUC,
      and `pg_locks` view are separate follow-up scopes.)

## Milestone 0013 — WAL buffers + eviction-safe durability

See `docs/milestones/0013-wal-buffers-optimization-with-eviction-safe-wal-before-data-durability.md`
for the full DoD. Decomposed into the three design-doc seams the
milestone calls out (`0013-0001`, `0013-0002`, `0013-0003`); pick
the topmost unchecked item.

- [x] WAL buffers architecture: bounded in-memory WAL buffer
      between `state.append` and segment files; overflow drain
      preserves LSN order; FlushUpTo two-stage (drain then
      dataSync); records ≥ wal_buffers bypass the buffer in one
      shot. Design doc
      `docs/design/0013-0001-wal-buffers-architecture.md`.
      (landed 2026-04-29: new `wal_buffers` GUC (TypeInt,
      BootVal 16 MiB, range `[0, 1 GiB]`, ContextPostmaster);
      0 disables. New `internal/wal/wal_buffer.go` with
      LSN-addressed ring (`base`/`head`/`tail`, byte-wise
      wraparound, two-slice `readForDrain`). `state` grew
      `walBuf *walBuffer` and `drainedLSN uint64` (last byte
      written to a segment file; `flushedLSN ≤ drainedLSN ≤
      writeLSN`; with walBuf nil, drainedLSN tracks writeLSN
      exactly). `state.append` branches: (a) buffer disabled
      OR record larger than walBuf.cap → bypass to writeAt
      directly (advances drainedLSN); (b) buffered append
      with overflow drain via `drainBufferBytes(need)` if
      free space < record size; record then copied into
      walBuf. `flushUpTo` adds Stage 1
      `drainBufferUpTo(targetLSN)` before the existing Stage 2
      dataSync — every byte ≤ targetLSN lands in segment
      files and on the dirty list before the durability
      barrier runs. `state.writeAt`'s existing dirty-flag
      bookkeeping covers drained segments automatically (the
      "sync debt" the milestone calls out). Walsender
      MemRing (M0010-0002) keeps mirroring every Append
      regardless of which path, so streaming-from-RAM is
      unchanged. Plumbed cmd/goopg start →
      `OpenOptions.WALBuffers` → `wal.Config.WALBuffers`.
      Tests: TestWALBufferDisabledByDefault (legacy 0-cap
      regression guard), TestWALBufferRetainsRecordsInRAM
      (segment file size unchanged for 5 small Appends with
      64 KiB cap), TestWALBufferOverflowDrainsToSegments
      (256-byte cap + 8×50-byte payloads → segment grows),
      TestFlushUpToDrainsBufferThenSyncs (Stage 1 drain then
      Stage 2 ReadAll round-trip), TestWALBufferRecordLargerThan
      BufferBypasses (256-byte record vs 64-byte cap), and
      TestWALBufferReadAllRoundTrip table-test across three
      cap values (0, 64 KiB, 256 B forced drains) — every
      configuration round-trips byte-identical. Full
      `go test ./...` green. Eviction-driven drain
      verification + counter surface land in M0013-0002 and
      M0013-0003.)

- [x] Overflow + eviction durability ordering (M0013-0002).
      Design doc
      `docs/design/0013-0002-overflow-and-eviction-durability-ordering.md`.
      (landed 2026-04-29: pure additive testing — no
      production code changes. The M0013-0001 two-stage
      `FlushUpTo` is already invoked by the existing flush
      sites (`Pool.flushSlot` line 886; `Pool.flushDirtyBatch`
      line 836) before `mgr.WriteBlock` fires, so the
      WAL-before-data invariant is preserved by construction.
      New `internal/storage/wal_buffer_eviction_test.go`
      (external `package storage_test` so it can import
      `wal` without cycle). Two tests pin the invariant:
      TestEvictionDrainsBufferedWALBeforeWritingPage (real
      `*wal.Writer` with WALBuffers=64 KiB, Append small
      record → segment 0 bytes confirmed in walBuf →
      MarkDirtyWithLSN(endLSN) → force eviction via second
      PinNew on one-slot pool → assert heap byte persisted
      AND segment bytes > 0 AND ReadAll returns the
      payload). TestFlushAllPacedDrainsBufferedWAL exercises
      the checkpointer-driven batched path
      (`Pool.FlushAllPaced` → `flushDirtyBatch` →
      `FlushUpTo(maxLSN)`) with 3 pages, 3 distinct
      buffered LSNs — every payload durable, every heap
      byte on disk. Failure-mode catalogue in the design
      doc maps Stage 1 skipped / Stage 2 skipped / wrong
      ordering / stale maxLSN to the specific assertion
      that catches each. Counters / observability surface
      for forced-drain events land in M0013-0003.)

- [x] WAL buffers observability + rollout (M0013-0003).
      Design doc
      `docs/design/0013-0003-wal-buffers-observability-and-rollout.md`.
      (landed 2026-04-29: new `walBufferCounters` struct
      (mirrors directIOCounters — single alloc owned by
      Writer, shared with state via pointer) carries two
      `atomic.Uint64`s: `overflowDrainBytes` (sizing
      indicator) and `flushDrainBytes` (durability cost).
      `drainReason` enum classifies each `drainBufferBytes`
      call site so attribution is explicit. Four new Writer
      accessors: WALBuffersCapacity (static GUC),
      WALBuffersBytesResident (live read via new
      `opWALBufStat` writer-loop op so it serialises
      against append/drain), WALBuffersOverflowDrainBytes,
      WALBuffersFlushDrainBytes. `pg_catalog.pg_stat_wal_io`
      extended with four columns at the end
      (wal_buffers_capacity_bytes /
      wal_buffers_bytes_resident /
      wal_buffers_overflow_drain_bytes /
      wal_buffers_flush_drain_bytes) — operator's
      "one place to look" preserved. Startup log line in
      `cmd/goopg start`: `event=wal_buffers_attached
      capacity_bytes=N` when `wal_buffers>0`. New test
      TestWALBufferCountersTrackDrains pins each counter
      only moves on its trigger (8×50B / 256B cap →
      OverflowDrainBytes; small append + FlushUpTo →
      FlushDrainBytes). Existing pg_stat_wal_io view tests
      keep passing — column-name based assertions, the
      four-column extension is transparent. Full
      `go test ./...` green. M0013 closes — every DoD item
      maps to a landed slice.)

## Milestone 0014 — PostgreSQL-compatible WAL on-disk format

See `docs/milestones/0014-wal-compatibility-with-pg.md` for the
full DoD. Decomposed into the four design-doc seams the milestone
calls out (`0014-0001`, `0014-0002`, `0014-0003`, `0014-0004`);
pick the topmost unchecked item.

- [x] XLOG page and segment layout — **types and helpers**
      (M0014-0001 step 1). Design doc
      `docs/design/0014-0001-xlog-page-and-segment-layout-compat.md`.
      (landed 2026-04-29: pure additive — no production-path
      changes yet. New `internal/wal/xlog_page.go` defines
      upstream-compatible page-header types and helpers
      targeting PG18: `XLogPageHeader` (24 bytes, mirrors
      `XLogPageHeaderData`), `XLogLongPageHeader` (40 bytes,
      mirrors `XLogLongPageHeaderData` with the
      sysid/seg_size/xlog_blcksz cross-check), constants
      `XLOGBlockSize=8192`, `XLOGPageMagic=0xD119`,
      `SizeOfXLogShortPHD=24`, `SizeOfXLogLongPHD=40`, all
      flag bits (`XLPFirstIsContRecord`, `XLPLongHeader`,
      `XLPBkpRemovable`, `XLPFirstIsOverwriteContRecord`,
      `XLPAllFlags`). Encode/decode helpers serialise to/from
      little-endian (host byte order on x86_64/aarch64 Linux,
      matches upstream's de-facto LE assumption — cross-arch
      transfer out of scope). `EncodeXLogPageHeader` rejects
      undefined flag bits (XLPAllFlags contract);
      `DecodeXLogPageHeader` returns the typed sentinel
      `ErrInvalidPageHeader` on magic mismatch so the
      M0014-0004 legacy-format detector has a clean branch.
      `EncodeXLogLongPageHeader` auto-sets the long bit;
      `DecodeXLogLongPageHeader` enforces it. Filename
      helpers `XLogFileName(tli, segno, segSize)` and
      `ParseXLogFileName(name, segSize)` produce/consume the
      upstream `<TLI:08X><Log:08X><Seg:08X>` form via strict
      `strconv.ParseUint` (rejects partial parses). Tests:
      TestXLogFileNameRoundTrip (5 representative TLI/segno
      cases including log-boundary 255→256), TestParseXLogFile
      NameRejectsGarbage, TestXLogPageHeaderRoundTrip,
      TestXLogLongPageHeaderRoundTrip, TestDecodeXLogPageHeader
      RejectsBadMagic (typed sentinel), TestEncodeXLogPageHeader
      RejectsUndefinedFlags (XLPAllFlags contract), TestDecode
      XLogLongPageHeaderRequiresLongBit. Coexists with the
      legacy `formatSegmentName` / `parseSegmentName` so the
      writer/reader switchover lands atomically in M0014-0001
      step 2 without churning unrelated code first. Full
      `go test ./...` green.)

- [x] XLOG page emission in writer + page-aware reader
      (M0014-0001 step 2). Continues
      `docs/design/0014-0001-xlog-page-and-segment-layout-compat.md`.
      (landed 2026-04-29: gated by new `Config.PageHeaders`
      (+ `SystemID` / `TimelineID`) flag — default `false`
      keeps every legacy data dir / test byte-unchanged.
      `state.append` calls `emitWithPageHeaders(record,
      writePos, segSize, sysID, tli)` which interleaves a
      40-byte `XLogLongPageHeader` at every segment boundary
      (stamps `xlp_sysid` / `xlp_seg_size` / `xlp_xlog_blcksz`)
      and a 24-byte short header at every other 8 KiB page
      boundary. Records crossing a page boundary stamp
      `XLP_FIRST_IS_CONTRECORD` and `xlp_rem_len =
      bytes_remaining_of_record` on the next page; multi-page
      records re-decrement page-by-page. `state.writePos` and
      `Writer.WrittenLSN()` advance over the combined stream
      length, preserving upstream's invariant **LSN = byte
      offset in the on-disk WAL stream**. `Append` returns
      `startLSN = writePos + leading_header_bytes + 1` so the
      LSN lands on the first record byte. Reader side:
      `RecordIterator.Next` skips any header at the cursor
      before checking write-tail; `readRecordBytesAt` is the
      new helper that mirrors the writer's interleave —
      returns record bytes and the physical advance count
      (record bytes + skipped header bytes). `ReadAll`
      auto-detects format via `DetectWALFormat(walDir)` and
      dispatches to `readAllPageAware`; classification errors
      silently fall back to the legacy walk so the existing
      tiny-segment tests still work. `scanLastSegmentEnd`
      (writer startup) consults `cfg.PageHeaders` directly;
      EOS becomes two-flavoured — all-zero page header at a
      page boundary OR all-zero record header mid-page
      (preserves the M0007 / 0007-0001 preallocated-tail
      contract). MemRing capture / walBuf path / writeAt
      layering all consume `stream` instead of the bare
      record so direct-I/O alignment, AIO submission, and
      walsender RAM streaming see the same physical bytes
      the segment carries. New tests in
      `internal/wal/xlog_emit_test.go`:
      TestPageEmissionLongHeaderAtSegmentStart,
      TestPageEmissionShortHeaderAtPageBoundary (exact
      `xlp_rem_len = record_size - (XLOGBlockSize -
      SizeOfXLogLongPHD)` arithmetic),
      TestPageEmissionRecordCrossesPage (Append/ReadAll LSN
      cross-check), TestPageEmissionRecordCrossesSegment
      (segment-spanning record → long header on next segment
      with both XLPLongHeader and XLPFirstIsContRecord set),
      TestPageEmissionIteratorRoundTrip,
      TestPageEmissionRecoversCleanly (close + reopen with
      Preallocate=true), TestPageEmissionLegacyDefaultUnchanged
      (rollout invariant: byte-identical output when
      PageHeaders=false). Full `go test ./...` green. The
      XLogRecord-header switchover (M0014-0002 step 2) and
      pg_waldump validation (M0014-0003) remain deferred —
      `PageHeaders=true` today produces upstream-shaped pages
      with goopg's legacy 8-byte length+CRC32-IEEE record
      frames inside; pg_waldump won't accept those until the
      record-frame switchover lands.)

- [x] XLogRecord header + rmgr — **types and helpers**
      (M0014-0002 step 1). Design doc
      `docs/design/0014-0002-xlogrecord-header-and-rmgr-mapping.md`.
      (landed 2026-04-29: pure additive — no production-path
      changes yet, mirroring the M0014-0001-step-1 pattern.
      New `internal/wal/xlog_record.go` defines
      `XLogRecord` (24 bytes; mirrors upstream's
      `xl_tot_len/xl_xid/xl_prev/xl_info/xl_rmid/_pad/xl_crc`
      layout byte-for-byte), `Rmgr` enum with values matching
      upstream's `RM_*_ID` (RmgrXLog=0, RmgrXact=1,
      RmgrStorage=2, RmgrHeap2=9, RmgrHeap=10, RmgrBtree=11
      — only the IDs goopg's current record kinds map to)
      plus MaxKnownRmgr boundary, flag-bit constants
      (XLRInfoMask / XLRRmgrInfoMask / XLRSpecialRelUpdate /
      XLRCheckConsistency). New `XLogCRC32C(data)` uses Go's
      `crc32.MakeTable(crc32.Castagnoli)` (singleton) for
      upstream-compatible CRC32C. `EncodeXLogRecordHeader`
      validates TotLen == header+payload, rejects undefined
      framework bits, zeros the 2-byte padding (upstream
      invariant), zeros the CRC field while computing the
      running CRC across (header || payload), then writes the
      stored CRC back. `DecodeXLogRecordHeader` returns the
      typed sentinel `ErrInvalidRecordHeader` on non-zero
      padding, unknown rmid (> MaxKnownRmgr), or undefined
      framework bits. Separate `VerifyXLogRecordCRC(headerBytes,
      payload, want)` for recovery-time checking — caller
      doesn't have to zero the CRC field, the helper does that
      in a scratch copy. Tests: TestXLogCRC32CIsCastagnoli (the
      canonical "123456789" → 0xE3069283 vector — any drift to
      crc32.IEEE breaks pg_waldump compat), TestXLogRecordHeader
      RoundTrip, TestVerifyXLogRecordCRCDetectsTamper (1-bit
      payload flip → ErrCorruptRecord), TestEncodeRejectsTotLen
      Mismatch, TestEncodeRejectsUndefinedFrameworkBits,
      TestDecodeRejectsNonZeroPadding,
      TestDecodeRejectsUnknownRmgr, TestDecodeTruncatedSrc,
      TestEncodeCRCMatchesDirectComputation. Goopg→upstream
      record-kind mapping table is in the design doc; the
      actual writer/reader switchover for both M0014-0001
      step 2 and M0014-0002 will land together in a later loop
      so a half-migrated segment file (new pages + legacy
      records, or vice versa) is impossible. Full
      `go test ./...` green.)

- [ ] Recovery + streaming + compat validation (M0014-0003).
      Update WAL replay paths to decode the new format;
      verify pg_waldump can parse goopg's emitted WAL on
      representative workloads. Continues
      `docs/design/0014-0003-recovery-streaming-and-compat-validation.md`.

- [x] Rollout guardrails — **legacy-format detection helper**
      (M0014-0004 step 1). Design doc
      `docs/design/0014-0004-rollout-guardrails-and-operator-playbook.md`.
      (landed 2026-04-29: pure additive read-only utility.
      New `internal/wal/format_detect.go` with
      `WALFormatVersion` enum (Unknown / Legacy / PGCompat,
      `String()` for log lines) and
      `DetectWALFormat(walDir) (WALFormatVersion, error)`.
      Inspects the lowest-numbered segment file (parsed
      via either `parseSegmentName` legacy or
      `ParseXLogFileName` pg-compat — both return ok=false
      for `.gitignore`-shaped non-segment files), reads
      first 40 bytes, tries `DecodeXLogLongPageHeader` then
      `DecodeXLogPageHeader`. Magic match → PGCompat;
      `errors.Is(err, ErrInvalidPageHeader)` → Legacy
      (legacy frames start with a `[len:uint32][crc:uint32-IEEE]`
      pair so they can never collide with `XLOGPageMagic=0xD119`
      at offset 0..1 — uint32 length values up to ~16 MB stay
      well below the high-byte-set magic). Empty / nonexistent
      dirs → (Unknown, nil) — fresh-data-dir bootstrap doesn't
      have to special-case. Truncation < 24 bytes → typed
      error. CRC validation intentionally skipped — too
      expensive at startup and the writer's per-record check
      catches real corruption later. Tests:
      TestDetectWALFormatNonexistentDir,
      TestDetectWALFormatEmptyDir,
      TestDetectWALFormatIgnoresGarbageFiles (3 user files,
      none parses as segment),
      TestDetectWALFormatLegacy (synthesised legacy
      length+CRC32-IEEE frame at offset 0),
      TestDetectWALFormatPGCompat (`EncodeXLogLongPageHeader`
      → file → DetectWALFormat round-trip),
      TestDetectWALFormatTruncatedSegment,
      TestDetectWALFormatVersionString. Caller wiring
      (loadState fail-fast on format mismatch) lands in
      M0014-0004 step 2 alongside the joint M0014-0001/0002
      writer/reader switchover. Full `go test ./...` green.)

## Milestone 0015 — PL/pgSQL stored routines (function-first delivery)

See `docs/milestones/0015-plpgsql-stored-routines-function-first.md`
for the full DoD. Substantial milestone — large surface area
(parser + analyzer + plpgsql interpreter + catalog + executor).
Decomposition + design docs land when this milestone is picked up.

- [ ] Stage A: function-first delivery (CREATE OR REPLACE FUNCTION
      ... LANGUAGE plpgsql, callable from SELECT). Decompose into
      seam-sized slices when picked up.
- [ ] Stage B: procedure follow-up (CREATE PROCEDURE, CALL,
      out-parameter binding).

## Milestone 0016 — WITH clause (CTE) support

See `docs/milestones/0016-with-clause-cte-support.md` for the full
DoD. Decomposed into the four design-doc seams the milestone calls
out (`0016-0001`..`0016-0004`); pick the topmost unchecked item.

- [x] WITH parser, AST, and statement dispatch
      (M0016-0001 step 1). Design doc
      `docs/design/0016-0001-with-parser-ast-and-name-resolution.md`.
      (landed 2026-04-29: parser-only slice. New AST nodes
      `CommonTableExpr` (Name + optional Columns alias list +
      Query *SelectStmt) and `WithClause` (Recursive flag +
      ordered CTEs slice). `SelectStmt` / `InsertStmt` /
      `UpdateStmt` / `DeleteStmt` each grew an optional
      `With *WithClause` field — nil for pre-M0016 callers,
      so existing tests are byte-for-byte unchanged. New
      `KwRecursive` keyword. New `internal/parser/with.go`
      with `parseWithClause` (handles `WITH [RECURSIVE]
      cte (, cte)*`) and four `parseSelectWithCTE` /
      `parseInsertWithCTE` / `parseUpdateWithCTE` /
      `parseDeleteWithCTE` thin wrappers that delegate to
      the existing per-statement parsers and stamp the
      pre-parsed WithClause onto the result. New `parseCTE`
      handles the `name [(col, ...)] AS (SELECT ...)` shape;
      Stage A restriction (`KwInsert/KwUpdate/KwDelete`
      bodies) surfaces as a clean parse error pinned at the
      inner-statement keyword position. `parseStatement`
      gained a new `case KwWith → parseStatementWithCTE`
      dispatch that peeks past the WithClause to route to
      the right per-statement parser. Tests: 11 sub-tests
      cover simple WITH, multiple CTEs (left-to-right
      order), RECURSIVE keyword acceptance (planner-level
      rejection is a separate slice), column-alias list,
      data-modifying-body rejection (3 variants), With
      flow-through to Insert/Update/Delete (3 sub-tests),
      missing-AS rejection, empty-list rejection, and a
      regression guard pinning that plain SELECT still
      produces With=nil. Full `go test ./...` green.
      Analyzer name resolution + planner / executor
      integration land in M0016-0001 step 2 / M0016-0002.)

- [x] WITH analyzer: name resolution, scope rules,
      shadowing handling (M0016-0001 step 2). Design doc
      `docs/design/0016-0001-with-parser-ast-and-name-resolution.md`.
      (landed 2026-04-29: `scope` grew a `ctes
      map[string]*catalog.Table` field — populated by new
      `analyzeWith(with, ctx)` which walks each CTE in
      declaration order, recursively analyzes its inner
      SELECT under ctx (so a later CTE can reference an
      earlier sibling — left-to-right visibility), builds a
      synthetic catalog.Table mirroring
      `synthesizeSubqueryTable`'s naming chain, applies the
      optional `(col, ...)` alias list with arity validation
      (42P10 on mismatch), rejects duplicate CTE names within
      one WITH list (42710), and rejects WITH RECURSIVE
      (0A000 — Stage A only). New `resolveTable(ctx, rv)`
      helper walks the scope chain head-first looking for a
      matching CTE name (so an inner WITH shadows an outer
      one) before falling through to the catalog. Schema-
      qualified names bypass CTE lookup. New
      `buildSelectScopeIn(s, ctx)` is the scope-aware variant
      used inside CTE bodies. `analyzeSelectWithParent` calls
      `analyzeWith` early then routes FROM-clause resolution
      through `buildSelectScopeIn` so CTE references resolve.
      `analyzeInsert` / `analyzeUpdate` / `analyzeDelete`
      each gained an early `analyzeWith` call so type errors
      inside CTE bodies surface even though Stage A's
      planner / executor doesn't yet consume the CTEs. The
      analyzer Insert keeps using `lookupTable` for its
      target relation (CTE shadowing of `INSERT INTO t`
      target is intentionally NOT supported; matches PG
      behaviour). Tests:
      TestAnalyzeWithCTEResolvesInFROM,
      TestAnalyzeWithCTELeftToRightVisibility,
      TestAnalyzeWithCTERejectsForwardReference (pins the
      right-to-left scope rule), TestAnalyzeWithRecursive
      Rejected (0A000), TestAnalyzeWithDuplicateCTEName
      (42710), TestAnalyzeWithColumnAliasArityMismatch
      (42P10), TestAnalyzeWithColumnAliasesRenameColumns
      (alias list overrides inner names; original names no
      longer resolve), TestAnalyzeWithCTEShadowsBaseRelation
      (CTE name shadows the catalog table; columns the CTE
      doesn't expose return 42703), TestAnalyzeWithoutCTE
      Unchanged (regression guard for byte-for-byte
      invariance), TestAnalyzeWithCTEErrorsBubbleUp.
      Full `go test ./...` green. Planner / executor for
      non-recursive CTEs lands in M0016-0002.)

- [x] Non-recursive CTE planner + executor (M0016-0002).
      Design doc
      `docs/design/0016-0002-nonrecursive-cte-planner-executor.md`.
      (landed 2026-04-29: Stage A inline-substitution
      strategy — each FROM-clause reference to a CTE name
      returns a freshly-cloned plan of that CTE's body. New
      `internal/planner/with.go` with `plannedCTE` record
      (name + planned Node + Schema + synthetic
      *catalog.Table), package-local `planCTEs map` (mirrors
      the existing `planParent` save/restore pattern), and
      `preplanWithClause(with, cat)` which walks each CTE
      left-to-right with prior siblings visible to later ones
      via the in-progress map. Returns a restorer the caller
      defers. CTE body planning bypasses Plan()'s analyzer
      pass and calls planSelect directly — the outer Plan()
      Analyze already validated the whole tree under the
      analyzer's CTE scope, and a second analyzer pass would
      re-validate without the parent CTE scope and
      erroneously reject sibling references. Column-alias
      list arity validation matches the analyzer's check
      (42P10). RECURSIVE rejected at planner level too
      (0A000) as second line of defence. `planScanRangeVar`
      gained a CTE-substitution path before catalog lookup,
      gated on `rv.Schema == ""` (CTE names are unschemed).
      Wired into all four entry points: `planSelect` /
      `planInsert` / `planUpdate` / `planDelete` each gained
      the preplanWithClause + defer restore stanza. Tests:
      8 planner-side (TestPlanWithSimpleCTE, TestPlanWithCTE
      ReadingTable (with table column type flow-through),
      TestPlanWithCTEMultipleConsumers (cross-product —
      pins inlining), TestPlanWithCTEReferencingPriorSibling
      (left-to-right scope), TestPlanWithRecursiveRejected,
      TestPlanWithCTEShadowsBaseRelation,
      TestPlanWithColumnAliasArityMismatch (42P10),
      TestPlanWithoutCTEUnchanged regression guard) + 2
      executor end-to-end (TestExecuteWithSimpleCTE,
      TestExecuteWithCTEMultipleConsumers — confirm the
      executor needs zero CTE infrastructure since Stage A
      inlining produces a vanilla plan tree). Full
      `go test ./...` green. Materialise-once optimisation
      and EXPLAIN labels for CTE producers land in
      M0016-0004.)

- [ ] (BLOCKED) Recursive CTE fixpoint execution
      (M0016-0003). Hard prereq: `UNION ALL` planner +
      executor support, which goopg's planner currently
      rejects with `0A000 set operations are not supported`.
      Recursive CTEs are inherently `WITH RECURSIVE r AS
      (anchor UNION [ALL] recursive_member) SELECT ...`
      shaped, so this slice can't ship without UNION ALL
      first. Once UNION ALL lands, this slice does the
      anchor/recursive-member detection, planner-side
      fixpoint scan node, executor iteration, cycle-safe
      termination, and unsupported-recursive-shape
      rejection. Continues
      `docs/design/0016-0003-recursive-cte-fixpoint-execution.md`.

- [x] CTE observability + compatibility tests
      (M0016-0004). Design doc
      `docs/design/0016-0004-cte-observability-and-compat-tests.md`.
      (landed 2026-04-29: closes the Stage A picture.
      New `planner.CTEScan` plan node wraps each cloned
      CTE body at `planScanRangeVar`'s substitution site so
      EXPLAIN can label the inlined subtree (Name + Alias).
      Pure labeling artifact — `executor.Build` unwraps to
      Child, so Stage A's "zero new executor infrastructure"
      property is preserved. EXPLAIN's `describePlan` switch
      gained a CTEScan arm rendering "CTE Scan on <name>"
      (or "CTE Scan on <name> <alias>" when distinct);
      `planChildren` recurses into Child so the inlined body
      still appears below the label. Tests:
      TestExplainCTEScanLabelsCTEByName (`WITH a AS (SELECT
      1) SELECT * FROM a` produces "CTE Scan on a" in the
      EXPLAIN output), TestExplainCTEScanShowsAlias
      (FROM-alias rendering),
      TestExplainCTEScanRecursesIntoChild. Plus three
      end-to-end PG-shaped compat tests in
      with_compat_test.go: TestCompatCTEFilterThenAggregate
      (filter via CTE + count(*)),
      TestCompatCTEMultiConsumerCrossProduct (single-row
      CTE × itself = 1 row), TestCompatCTEChainedSiblings
      (left-to-right `a → b` reference end-to-end). Full
      `go test ./...` green. Materialise-once optimisation
      and runtime CTE counters in pg_stat_* views remain
      out of scope per design doc — the inlining model
      makes per-CTE counters less informative than per-
      statement counters.)

## Milestone 0017 — UPSERT (INSERT ... ON CONFLICT DO UPDATE)

See `docs/milestones/0017-upsert-on-conflict-do-update.md` for the
full DoD. Substantial milestone — parser + planner + executor +
concurrent-write semantics. Decompose when picked up.

- [x] UPSERT Stage A — **parser surface and AST**
      (M0017-0001 step 1). Design doc
      `docs/design/0017-0001-on-conflict-parser-ast-and-analysis.md`.
      (landed 2026-04-29: parser-only additive slice mirroring
      M0016-0001 / M0018-0001 step-1 pattern. `InsertStmt`
      grew an optional `OnConflict *OnConflictClause` —
      nil-default keeps existing INSERT call sites byte-for-byte
      unchanged. New AST: `OnConflictAction` enum (None /
      Nothing / Update; None is the zero-value sentinel for
      "no clause"), `OnConflictTarget` (Columns + Constraint
      — exactly one populated; both empty for the no-target
      shape), `OnConflictClause` (Target + Action + UpdateSet
      + UpdateWhere). New keywords: `KwConflict` / `KwDo` /
      `KwNothing`. `excluded` stays an unreserved identifier
      so `excluded.col` parses as a normal
      `*ColumnRef{Table:"excluded"}` — promoting it would
      shadow legitimate column refs named `excluded` in
      pre-UPSERT clusters; matches upstream's de-facto rule.
      New `parseOnConflict()` in dml.go handles the next-token
      disambiguation (`(` → column list, `ON` keyword →
      constraint name, otherwise no target) and the action
      keyword set (`NOTHING` / `UPDATE` after `DO`). Parser
      accepts every upstream shape including the constraint-name
      target the milestone defers to Stage B — keeps the AST
      shape stable so Stage B promotion doesn't change
      parser-error semantics. Tests (10 in
      on_conflict_test.go): TestParseInsertOnConflictNoTargetDoNothing,
      TestParseInsertOnConflictColumnTargetDoNothing,
      TestParseInsertOnConflictDoUpdate (pins `excluded.b`
      AST shape so analyzer slice has stable input),
      TestParseInsertOnConflictDoUpdateWithWhere,
      TestParseInsertOnConflictConstraintTarget,
      TestParseInsertOnConflictWithReturning (composes after
      ON CONFLICT in upstream-mandated order),
      TestParseInsertOnConflictCTE (M0016 + M0017 surface
      combine without parser interaction),
      TestParseInsertOnConflictRejectsBadAction (DO REPLACE /
      DO INSERT → parse error),
      TestParseInsertOnConflictRejectsMissingDo,
      TestParseInsertWithoutOnConflictUnchanged (rollout
      guardrail — no spurious empty clauses for unmigrated
      callers). Full `go test ./...` green. Analyzer name
      resolution + `excluded` pseudo-table scope, planner
      conflict-arbiter selection, executor concurrency /
      locking integration, observability counters all land
      in subsequent slices (M0017-0001 step 2 / M0017-0002 /
      M0017-0003 / M0017-0004).)
- [x] UPSERT Stage A — **analyzer wiring**
      (M0017-0001 step 2). Continues
      `docs/design/0017-0001-on-conflict-parser-ast-and-analysis.md`.
      (landed 2026-04-29: `scopeRel` grew a `qualifiedOnly
      bool` field — hides the rel from the unqualified column-
      resolution loop AND restricts qualified-arm matches to
      alias-only (never via the underlying table's catalog
      name). Without that dual-restriction, registering
      `excluded` as a synthetic rel pointing at the same
      `*catalog.Table` would (a) make every bare `col`
      ambiguous between target and excluded, and (b) make
      `<target>.col` ambiguous because both rels share
      `rel.table.Name`. New `analyzeOnConflict(oc, tbl, cat,
      targetAlias)` called from `analyzeInsert` validates:
      (1) Stage B reject for `ON CONSTRAINT name` — `0A000`;
      (2) `DO UPDATE` requires a target — `42601` mirroring
      upstream's "requires inference specification or
      constraint name"; (3) conflict-target columns exist
      on target table — `42703` (planner picks the unique-
      arbiter index in M0017-0002); (4) DO UPDATE scope
      builds two rels — target (bare + qualified-by-alias
      resolve here) and `excluded` qualifiedOnly (reachable
      only via `excluded.col`); (5) SET assignment column
      existence (`42703`) + RHS type (`42804`) mirroring
      `analyzeUpdate`; (6) optional WHERE analyzed under
      the same scope, must be boolean (`42804`). `planInsert`
      second-line gate rejects any `InsertStmt` carrying a
      non-nil `OnConflict` with `0A000` so an executable
      plan never silently drops the clause when a caller
      bypasses the analyzer. Tests: 11 analyzer-side in
      `internal/analyzer/on_conflict_test.go` covers all
      six accepted shapes (DO NOTHING no-target, DO NOTHING
      with target, DO UPDATE basic, DO UPDATE mixed bare +
      excluded refs, DO UPDATE WHERE qualified by target
      name + excluded, all six rejected diagnostics
      including 0A000 constraint, 42601 update-without-
      target, 42703 unknown target/SET/excluded column,
      42804 type mismatch and non-boolean WHERE) + 1
      planner-side `TestPlanInsertOnConflictRejected`
      mirroring the `TestPlanWithRecursiveRejected`
      pattern. Full `go test ./...` green.)
- [x] UPSERT Stage A — **planner + arbiter selection**
      (M0017-0002). Design doc
      `docs/design/0017-0002-upsert-planner-and-arbiter-selection.md`.
      (landed 2026-04-29: produces an executable plan node
      from the parser/analyzer-validated AST. New
      `OnConflictPlan` (Action / ArbiterIndex /
      ArbiterColumns / UpdateSet / UpdateWhere) hangs off
      `Insert.OnConflict` — nil-default keeps every
      pre-M0017 INSERT byte-unchanged. New
      `OnConflictAction` enum (Nothing / Update).
      `resolveArbiterIndex(target, tbl, cat)` walks
      `cat.IndexesOnTable(tbl)` looking for a unique index
      whose column **set** (case-insensitive,
      order-insensitive) equals the user-supplied target —
      first match wins, catalog iteration order is stable
      so the choice is deterministic; `(a,b)` matches
      `(a,b)` and `(b,a)` indexes per upstream semantics.
      Returns `42P10` "no unique or exclusion constraint
      matching the ON CONFLICT specification" on no match.
      `ArbiterColumns` is the per-target-column ordinal
      list in `tbl.Columns` order matching
      `ArbiterIndex.Columns` so the executor extracts the
      conflict key without re-doing name lookup.
      `rangeBinding` grew `qualifiedOnly bool` — same
      dual-restriction as the analyzer's
      `scopeRel.qualifiedOnly`: hidden from the unqualified
      loop AND restricted to alias-only on the qualified
      arm. Without it, registering `excluded` as a synthetic
      binding pointing at the same `*catalog.Table` would
      make every bare `col` ambiguous and let
      `<target>.col` accidentally match excluded. DO UPDATE
      planner builds a 2-binding scope (target at offset 0,
      excluded at offset N qualifiedOnly), 2N-wide schema;
      `UpdateSet` is parallel to `tbl.Columns` (nil = leave
      alone). Resolved ColumnRefs carry Index values 0..N-1
      (existing tuple) / N..2N-1 (inserted tuple) — the
      executor will arrange the merged layout at runtime.
      Executor `*planner.Insert` build-path rejects when
      `p.OnConflict != nil` with "ON CONFLICT execution is
      not supported in v0 (Stage A executor lands in
      M0017-0003)" — two-step gate: planner produces full
      plan so misuses surface specific catalog/arbiter
      errors not a generic curtain; executor refuses to
      silently drop the clause. Tests in
      `internal/planner/with_test.go`:
      TestPlanInsertOnConflictNoMatchingArbiter (42P10
      without index), TestPlanInsertOnConflictWithUniqueIndex
      (full plan-shape assertion against an installed
      unique index — ArbiterIndex / ArbiterColumns / UpdateSet
      slot mapping all checked),
      TestPlanInsertOnConflictDoNothingNoTarget (no-target
      form leaves ArbiterIndex nil for runtime). Full
      `go test ./...` green.)
- [x] UPSERT Stage A — **executor runtime**
      (M0017-0003). Design doc
      `docs/design/0017-0003-upsert-executor-concurrency-and-locking.md`.
      (landed 2026-04-29: new `upsertOp` in
      `internal/executor/operators_upsert.go` runs the
      planner-resolved state. Per row: encode conflict
      key from `OnConflict.ArbiterColumns`, probe via
      `btree.RangeScan(key, key, callback)` and skip
      invisible tuples via `mvcc.TupleVisible` (essential
      because UPSERT inserts duplicate index entries —
      historical dead versions are still reachable). On
      no-conflict: `writeHeapRowReturning` + arbiter
      `tree.Insert(key, newPtr)` so subsequent rows in
      the same statement see the new entry. On conflict
      + DO NOTHING: skip silently (RowsAffected does NOT
      bump — matches upstream). On conflict + DO UPDATE:
      build merged 2N-wide row (existing 0..N-1 || inserted
      N..2N-1 — planner ColumnRef Index values already
      address this layout), evaluate UpdateWhere (non-true
      → silent skip per upstream — no DO NOTHING fallback),
      evaluate each non-nil UpdateSet[i] (nil slots inherit
      existing[i]), stamp xmax on conflicting tuple via
      `PageSetHeapTupleXmax` + `markHeapDeleteDirty`,
      writeHeapRowReturning the updated tuple,
      maintainArbiter inserts new (key, newPtr); old (key,
      oldPtr) entry stays in place since btree.Insert
      allows duplicates and future probe's visibility
      filter rejects the dead one. Refactor:
      `writeHeapRow` split into void wrapper +
      `writeHeapRowReturning` that surfaces the new
      tuple's `(block, slot)`; existing INSERT/UPDATE
      callers unchanged. Stage A scope guard at
      `upsertOp.Open`: UpdateSet targeting a conflict-key
      column ordinal rejects with `0A000` — without it
      the arbiter entry for the original key would point
      at a tuple whose actual key bytes differ; future
      probes would land on the wrong row. Multi-column
      arbiters surface `0A000` at runtime (v0 btree only
      has single-column key encoding). Tests in
      `operators_upsert_test.go`: 6 end-to-end scenarios
      through parser→analyzer→planner→executor —
      TestUpsertNoConflictInsertsRow (new key path),
      TestUpsertConflictDoUpdate (replace existing label),
      TestUpsertConflictDoNothing (RowsAffected=0),
      TestUpsertDoUpdateMixingExistingAndExcluded
      (`SET label = label || '/' || excluded.label`
      exercises merged-row layout — bare `label` from
      existing[1], `excluded.label` from inserted[N+1]),
      TestUpsertDoUpdateWithWhereSkipsRow (predicate
      false → silent skip), TestUpsertConflictKeyModificationRejected
      (`0A000` Stage A guard). Test fixture seeds rows
      THEN creates the unique index so backfill picks
      up the seeded tuples — required because v0 doesn't
      maintain non-arbiter indexes on plain INSERT
      (pre-existing limitation; the arbiter is the only
      index UPSERT itself maintains). Full `go test ./...`
      green. Concurrency hardening (speculative insert +
      cleanup on conflict, MVCC-correct under contention)
      deferred to a follow-on slice; under concurrent
      UPSERTs both writers may believe they're winning
      the race until the next CREATE INDEX rebuild
      surfaces the duplicate.)
- [x] UPSERT Stage B: `ON CONFLICT ON CONSTRAINT name`.
      Continues
      `docs/design/0017-0001-on-conflict-parser-ast-and-analysis.md`.
      (`excluded.col` references already landed in Stage A.)
      (landed 2026-04-30: promotes the constraint-name target
      from the Stage A 0A000 reject to a fully-supported path.
      Analyzer's `analyzeOnConflict` constraint branch:
      `cat.LookupIndex(parser.ObjectName{Name:
      target.Constraint})` then 42704 ("constraint X for
      table Y does not exist") / 42704 ("does not belong to")
      / 42P10 ("is not a unique constraint") — mirrors
      upstream's three diagnostics from
      `transformOnConflictClause`. Planner's
      `resolveArbiterIndex` gains a constraint-name branch
      ahead of the column-set inference loop, returns the
      named index + tbl ordinals matching `idx.Columns` so
      the executor's existing `ArbiterColumns` handling
      works without change. Executor needs **zero new code**
      — upsertOp consumes ArbiterIndex/ArbiterColumns
      regardless of how the planner resolved them. Tests:
      3 new analyzer tests (TestAnalyzeOnConflictAccepts
      ConstraintTarget, TestAnalyzeOnConflictRejectsNonUnique
      Constraint, TestAnalyzeOnConflictRejectsConstraintOn
      DifferentTable; the old Stage A reject test
      TestAnalyzeOnConflictRejectsConstraintTarget is
      replaced with TestAnalyzeOnConflictRejectsUnknown
      Constraint asserting 42704), 1 planner
      TestPlanInsertOnConflictByConstraintName (pins
      ArbiterIndex pointer + ArbiterColumns), 1 executor
      end-to-end TestUpsertConflictByConstraintName. Full
      `go test ./...` green.)

## Milestone 0018 — EXPLAIN / EXPLAIN ANALYZE

See `docs/milestones/0018-explain-and-explain-analyze.md` for the
full DoD. Decomposed into the four design-doc seams the milestone
calls out.

- [x] EXPLAIN parser options + AST (M0018-0001 step 1). Design
      doc `docs/design/0018-0001-explain-parser-options-and-ast.md`.
      (landed 2026-04-29: parser-only additive slice mirroring
      the recent step-1 pattern. New `ExplainOptions` struct
      (Analyze / Verbose / Costs / Buffers / Settings / Timing
      / Summary bools + Format enum). New `ExplainFormat` enum
      (Text / JSON). `ExplainStmt` grew an `Options` field —
      zero value preserves byte-for-byte invariance for the
      pre-M0018 bare-EXPLAIN form. New `parseExplainOptionList`
      handles `EXPLAIN (option [VALUE], ...) <stmt>`; the
      keyword form `EXPLAIN [ANALYZE] [VERBOSE]` continues to
      work in either order. Per-option dispatch validates the
      name set; FORMAT TEXT|JSON is recognised, all other
      options take an optional bool with `defGetBoolean`-style
      forms (ON/OFF/TRUE/FALSE/1/0). Unknown options error
      with byte-position diagnostics; FORMAT XML/YAML rejects
      with "unsupported FORMAT". Tests: 13 parser tests
      cover bare-form regression guard, both keyword forms in
      either order, parenthesised default-true behaviour, all
      bool flags via the parenthesised form, FORMAT JSON +
      TEXT, unknown-option rejection, empty-list rejection,
      bad-FORMAT rejection, all six bool value forms
      (ON/OFF/TRUE/FALSE/1/0), and a mixed-options test.
      Full `go test ./...` green. Static plan rendering
      (FORMAT JSON output, VERBOSE), runtime instrumentation
      for ANALYZE, and JSON snapshot regression strategy
      land in 0018-0002 / 0018-0003 / 0018-0004.)

- [x] Static plan rendering — Stage A (M0018-0002).
      Design doc
      `docs/design/0018-0002-static-plan-rendering-and-output-contract.md`.
      (landed 2026-04-29: parser AST options now flow into
      EXPLAIN output. `planner.Explain` grew an `Options
      parser.ExplainOptions` field; the Plan dispatcher
      copies `s.Options` from `parser.ExplainStmt` and
      rejects ANALYZE BEFORE planning the inner statement
      with SQLSTATE 0A000 and a "Stage B" message — pre-plan
      check ensures `EXPLAIN ANALYZE INSERT ...` doesn't
      trigger side effects of planning a write path that
      won't execute. `explainOp.Open` branches on
      `Options.Format`: ExplainFormatText runs the existing
      indented walker (byte-for-byte unchanged for the
      bare-EXPLAIN form); ExplainFormatJSON renders the plan
      tree as a JSON object via new `planToJSON` (Node Type
      + Plan Rows + optional Output array + recursive Plans
      array, wrapped in a single-element array matching
      upstream's `[ {root} ]` shape). VERBOSE adds an
      `Output: (col, ...)` line under each node in TEXT
      mode and an `Output` array key in JSON mode (matches
      upstream's "Output is part of VERBOSE" behaviour).
      Other static options (COSTS / TIMING / SUMMARY /
      BUFFERS / SETTINGS) are parsed-but-no-op until Stage B
      / M0018-0004 — agreed Stage A contract per the
      milestone doc. Tests:
      TestExplainTextFormatUnchanged regression guard,
      TestExplainFormatJSONProducesValidJSON,
      TestExplainFormatJSONOmitsOutputWithoutVerbose,
      TestExplainFormatJSONHonoursVerbose,
      TestExplainVerboseAddsOutputLine (text mode),
      TestExplainAnalyzeRejected (keyword form),
      TestExplainParenAnalyzeAlsoRejected (parens form too).
      Full `go test ./...` green.)

- [x] EXPLAIN ANALYZE instrumentation (M0018-0003).
      Design doc
      `docs/design/0018-0003-explain-analyze-instrumentation.md`.
      (landed 2026-04-29: M0018-0002 Stage A's ANALYZE
      0A000 rejection lifted. New
      `internal/executor/instrument.go`: `nodeStats` struct
      tracks per-Node `rowsOut` / `loops` / `startupNs` /
      `totalNs`; `instrumentedOp` wraps each Operator
      delegating Schema/RowsAffected with sidecar counters.
      Package-local `instrumentScope` + `withInstrumentation`
      mirror the existing planParent / outerScope pattern.
      `Build` switch arms each gained a `maybeInstrument(p,
      op)` wrap before return — recursive Build naturally
      wraps every node in the tree. nil-scope (the default)
      returns raw operators byte-for-byte unchanged so every
      pre-M0018-0003 path is invariant. Planner: dropped the
      Stage A ANALYZE rejection so the AST flows through to
      the executor. Executor: `explainOp.Open` branches on
      `Options.Analyze` — true builds with instrumentation,
      drains the inner plan to completion (Open / Next loop
      / Close so timers fire), then renders. TEXT output
      gains `(actual time=X..Y rows=R loops=L)` per node
      and a `Planning Time: N ms` / `Execution Time: N ms`
      footer. JSON output gains Actual Rows / Actual Loops /
      Actual Total Time / Actual Startup Time per node and
      Planning Time / Execution Time on the root. TIMING
      and SUMMARY are always-on under ANALYZE in this slice;
      explicit `TIMING OFF` / `SUMMARY OFF` is a follow-up.
      Tests: TestExplainAnalyzeRunsInnerAndReportsActualRows
      (5-row table → "rows=5 loops=1"),
      TestExplainAnalyzeIncludesPlanningExecutionTime,
      TestExplainAnalyzeJSONIncludesActualFields (all 6
      keys present + Actual Rows accurate), TestExplain
      AnalyzeOnSelectOneRowsAccurate. Stage A's
      ANALYZE-rejection tests deleted (the gate they pinned
      no longer exists). Full `go test ./...` green.
      BUFFERS / SETTINGS counter rendering and the JSON
      snapshot regression strategy land in M0018-0004.)

- [x] TIMING/SUMMARY OFF wiring + JSON snapshot strategy
      (M0018-0004). Design doc
      `docs/design/0018-0004-json-format-and-regression-strategy.md`.
      (landed 2026-04-29: parser AST grew an
      `ExplainOptionsSet` companion struct tracking which
      EXPLAIN options the user wrote explicitly. The parser
      flips `Set.<Option>` for every keyword-form ANALYZE /
      VERBOSE and every parenthesised-form name. Executor
      ANALYZE path computes effective TIMING/SUMMARY via
      `effective = !Set.Option || opts.Option` — defaults
      ON under ANALYZE, explicit OFF wins. nodeStats.timing
      flows from this; the wrapper skips per-row
      `time.Now()` snapshots when timing is false; the
      walkPlanAnalyze TEXT renderer emits `(actual rows=N
      loops=N)` without the `time=X..Y` bracket; planToJSON
      WithStats omits `Actual Total Time` / `Actual Startup
      Time` keys. SUMMARY=false suppresses the
      `Planning Time:` / `Execution Time:` footer rows in
      TEXT and root-level keys in JSON. Top-level wallclock
      is now measured unconditionally under ANALYZE so
      SUMMARY-without-TIMING still has a number to report.
      Tests:
      TestExplainAnalyzeTimingOffSuppressesTimeBracket,
      TestExplainAnalyzeTimingOnByDefault (regression guard
      for the default-true semantics),
      TestExplainAnalyzeSummaryOffSuppressesFooter,
      TestExplainAnalyzeTimingOffJSONOmitsTimeKeys (Actual
      Rows / Loops still present; Total / Startup Time
      gone), TestExplainAnalyzeSummaryOffJSONOmitsTimingKeys,
      TestExplainJSONShapeStable (structural snapshot for
      non-ANALYZE: required Node Type present; all gated
      keys absent — pins the contract for future BUFFERS /
      SETTINGS additions). Full `go test ./...` green.
      M0018 closes — BUFFERS / SETTINGS counter rendering
      is queued as a Pool-level instrumentation follow-up,
      not a M0018-internal slice.)

## Milestone 0019 — Autovacuum

See `docs/milestones/0019-autovacuum-support.md`. Substantial.
Decompose when picked up.

- [ ] Autovacuum launcher + worker architecture; trigger
      policy; observability.

## Milestone 0020 — Window functions

See `docs/milestones/0020-window-functions-over-row-number-rank-lag-lead.md`.
Substantial. Decompose when picked up.

- [ ] Window-function parser + planner + executor.

## Milestone 0021 — SELECT ... FOR UPDATE

See `docs/milestones/0021-pessimistic-lock-select-for-update.md`.

- [x] SELECT … FOR UPDATE parser surface + AST
      (M0021-0001 step 1). Design doc
      `docs/design/0021-0001-for-update-parser-analysis-and-ast.md`.
      (landed 2026-04-30: parser-only additive slice
      mirroring M0016-0001 / M0017-0001 / M0018-0001
      step-1 pattern. `SelectStmt.Locking
      []*LockingClause` — empty-default keeps existing
      tests byte-unchanged. New AST: `LockStrength`
      enum (`LockStrengthForUpdate=iota+1`,
      `LockStrengthForShare`; zero reserved),
      `LockWaitPolicy` enum (Block / NoWait /
      SkipLocked), `LockingClause` (Strength + Targets
      + WaitPolicy). New keywords: KwShare / KwOf /
      KwNowait / KwSkip / KwLocked. `parseLockingClause`
      called in a for-loop after LIMIT/OFFSET/FETCH so
      multiple clauses collect in source order
      (upstream allows e.g. `FOR UPDATE OF a NOWAIT
      FOR SHARE OF b`). OF list captured as raw
      identifiers; alias/table-name resolution is the
      analyzer's job. SKIP requires LOCKED. Stage A
      only accepts UPDATE and SHARE — NO KEY UPDATE /
      KEY SHARE deferred (would need NO+KEY composite
      tokens). `planSelect` second-line gate rejects
      `len(s.Locking) > 0` with 0A000 — two-step gate:
      parse the surface so diagnostics surface specific
      feature names, refuse to silently drop locking
      intent at runtime. Tests in locking_test.go: 10
      scenarios — all six accepted shapes (bare FOR
      UPDATE / FOR SHARE / OF list / NOWAIT / SKIP
      LOCKED / multi-clause / AFTER LIMIT) plus 2
      diagnostic guards (FOR READ, bare SKIP) plus 1
      rollout guardrail (TestParseSelectWithoutLocking
      Unchanged). Full `go test ./...` green. Analyzer
      validation + planner row-lock metadata +
      executor row-lock acquisition + NOWAIT/SKIP
      LOCKED runtime + deadlock + observability all
      deferred to M0021-0001 step 2 / M0021-0002 /
      M0021-0003 / M0021-0004.)
- [x] SELECT … FOR UPDATE — **analyzer wiring**
      (M0021-0001 step 2). Continues
      `docs/design/0021-0001-for-update-parser-analysis-and-ast.md`.
      (landed 2026-04-30: `analyzeLockingClauses(s, ctx)`
      runs at the tail of `analyzeSelectWithParent` when
      `len(s.Locking) > 0`. Mirrors upstream's
      `transformLockingClause` / `preprocess_rowmarks`
      rejection set: (1) **must have FROM** — `SELECT 1
      FOR UPDATE` → 0A000 "FOR UPDATE/SHARE is not
      allowed in this context"; (2) **no GROUP BY /
      HAVING** — aggregation produces grouped rows that
      don't map back to individual storage tuples, both
      → 0A000; (3) **OF target resolution** — each name
      must match a FROM-clause range variable by alias
      (when set) or by bare table name, mismatch → 42P01
      "relation not in FROM". `lockingTargetMatches`
      uses the alias-shadows-table rule (when `rel.alias
      != ""` we ONLY check alias — matches upstream
      column-reference rules). Wait-policy modifiers
      (NOWAIT, SKIP LOCKED) accepted at analyze time for
      AST stability across stages. Tests in
      `locking_test.go`: 10 scenarios covering every
      accept/reject combination including the
      multi-clause shape. Full `go test ./...` green.
      Aggregate-functions-in-target detection deferred —
      analyzer doesn't expose that predicate cleanly yet.
      Locking inside subqueries/CTEs also deferred.
      Planner row-lock metadata + executor lands in
      M0021-0002 / M0021-0003 / M0021-0004.)
- [x] SELECT … FOR UPDATE — **planner row-lock metadata
      + LockRows plan node** (M0021-0002). Design doc
      `docs/design/0021-0002-row-lock-planner-executor-integration.md`.
      (landed 2026-04-30: produces an executable plan
      node carrying the resolved per-relation locking
      intent. New `LockRows` wrapper at the top of the
      plan tree, Output() returns the child schema
      unchanged. New types: `LockStrength`
      (ForUpdate=iota+1 / ForShare), `LockWaitPolicy`
      (Block / NoWait / SkipLocked), `LockedRel`. Pre-
      M0021 SELECTs never produce LockRows so existing
      tests stay byte-unchanged. `planSelect` wraps the
      trailing Project with LockRows when `s.Locking !=
      nil`. `resolveLockedRels(s, ctx)` walks each parsed
      clause: empty Targets → one LockedRel per binding;
      non-empty → one per name via `findBindingByName`
      with alias-shadows-table semantics. Multiple
      clauses targeting the same rel produce duplicate
      LockedRels — the Stage A executor will fold them.
      `executor.Build` rejects `*planner.LockRows` with
      "row-level locking execution is not supported in
      v0" — two-step gate from M0017-0002→0003: planner
      produces full plan so EXPLAIN works, Build refuses
      to silently drop locking intent. EXPLAIN
      integration: `describePlan` returns "LockRows",
      `planChildren` returns child for tree recursion.
      Tests in `internal/planner/locking_test.go`: 6
      scenarios — TestPlanSelectForUpdateWrapsLockRows
      (full shape), TestPlanSelectForUpdateOfAlias
      (alias-only), TestPlanSelectForUpdateNoTargetLocks
      AllRels (bare FOR UPDATE locks every FROM rel),
      TestPlanSelectForUpdateNoWaitPropagates (enum
      conversion), TestPlanSelectForShareStrength,
      TestPlanSelectWithoutLockingNoWrapper (rollout
      guardrail). Full `go test ./...` green. Stage A
      executor (acquire row-locks before yielding) +
      NOWAIT/SKIP LOCKED runtime + deadlock observability
      all deferred to M0021-0003 / M0021-0004.)
- [ ] SELECT … FOR UPDATE — Stage A executor (acquire
      row-locks via lockmgr) + NOWAIT/SKIP LOCKED runtime
      + deadlock observability (M0021-0003 / M0021-0004).
- [ ] Tuple-level pessimistic locking on top of M0012 lock
      manager.

## Notes

- This file is the authoritative TODO list for Ralph. Update it after every
  meaningful change.
- Keep work to ONE item per loop. Decompose further if an item is larger
  than what fits in a single agent invocation.
- Every non-trivial subsystem must land alongside (or just before) a design
  doc under `docs/design/`. The spec treats this as a hard requirement.

## Completed

- [x] Project initialization (Ralph harness wired up).
