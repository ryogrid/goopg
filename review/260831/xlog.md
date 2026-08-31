# WAL (XLog) — Code Review 2026-08-31

Files: append_xlog_payload.go, archive_recovery.go, archive_restore.go, checkpointer.go, classifier.go, decoder.go, format.go, format_detect.go, index_am_refusal.go, insert_pos.go, insert_pos_publish.go, insertion_tracker.go, iterator.go, mem_ring.go, mem_ring_concurrent.go, padded_mutex.go, pg_assembled_emit.go, pg_xact_parse.go, pg_xlog_decode.go, pgoutput.go, pgoutput_decoder.go, predict_emitted_size.go, predict_xlog_record_len.go, publish_visibility.go, reader.go, reader_early_end.go, recovery.go, recovery_cache.go, relmap.go, reorder.go, repllog.go, replmon.go, reserve_emitted.go, retention.go, rmgr_map.go, segment_pad.go, segment_pad_emit.go, seq_log.go, slot_decoder.go, slots.go, slots_pg.go, snapshot.go, stream_replayer.go, stripe_append.go, stripe_append_emitted.go, stripe_writer_core.go, subscriber_mon.go, sync_linux.go, sync_other.go, syncrep.go, syncrep_parse.go, tail_publisher.go, timeline_history.go, wal_buffer.go, wal_buffer_publish_tail.go, wal_write_lock.go, writer.go, xlog_assemble.go, xlog_emit.go, xlog_page.go, xlog_record.go
Findings count: 31 real findings across 70 entries (39 "no significant issues" notes)

### append_xlog_payload.go:appendXLogPayload — no significant issues
- **Issue**: none of note. `predictXLogRecordLen` returns two values but only `paddedLen` is used (one wasted return, negligible).
- **Why**: single-shot composer, not in a hot loop.
- **Suggestion**: leave as-is.
- **Severity**: low

### archive_recovery.go:RunArchiveRecovery — repeated string allocation per segment
- **Issue**: in the loop `FormatSegmentNameTLI(nextSeg, 1)` + `filepath.Join(walDir, segName)` allocate strings per iteration; `ReadSegmentRecords` reads the whole segment into memory (`readStreamFrom`) each pass.
- **Why**: per-segment (not per-record), so impact is small, but the loop runs until the archive is exhausted and the segment is fully buffered even when only a handful of records are needed.
- **Suggestion**: acceptable for archive recovery frequency; if desired, hoist the join using `fmt.Sprintf` with a pre-sized buffer. Not worth changing.
- **Severity**: low

### archive_restore.go:RestoreArchivedFile — two-pass replace, low impact
- **Issue**: `strings.ReplaceAll(cmd, "%f", …)` then a second full pass `strings.ReplaceAll(cmd, "%p", …)` scans the command string twice and allocates twice.
- **Why**: restore_command is invoked once per missing segment — not hot.
- **Suggestion**: use `strings.NewReplacer("%f", segmentName, "%p", destPath).Replace(restoreCommand)` for a single pass.
- **Severity**: low

### checkpointer.go:runCheckpoint / volumeTriggerFires — no significant issues
- **Issue**: nothing obviously wasteful; `segmentSize()`/`checkpointSegments()` are recomputed but only on poll ticks / checkpoint cycles. `updateCheckPointDistanceEstimate` and `CheckPointDistanceEstimate` take a mutex but checkpoints are infrequent (acknowledged in code comments).
- **Why**: cadence is seconds-to-minutes, so the overhead is negligible.
- **Suggestion**: leave as-is.
- **Severity**: low

### classifier.go:xminFromTuple — could use encoding/binary
- **Issue**: `uint32FromLE(tuple[0:4])` hand-rolls the little-endian decode; every HeapInsert/HotUpdate/Update classified record passes through this on the logical-decoding path.
- **Why**: manual byte math is correct but less idiomatic; the slice `tuple[0:4]` is non-allocating, so the waste is trivial.
- **Suggestion**: `binary.LittleEndian.Uint32(tuple[:4])` (already have encoding/binary available elsewhere in the package). Cosmetic.
- **Severity**: low

### insert_pos_publish.go:reserveAndPublish — no significant issues
- **Issue**: none. One extra atomic store under the existing `posMu` critical section (documented design).
- **Why**: negligible; explicitly traded against a second write.
- **Suggestion**: leave as-is.
- **Severity**: low

### mem_ring_concurrent.go:WriteReserved / PublishUpTo / AdvanceWindow — no significant issues
- **Issue**: none; lock + memcpy, modulo math is cheap.
- **Why**: designed with read-lock parallelism for disjoint ranges.
- **Suggestion**: leave as-is.
- **Severity**: low

### iterator.go:readOneAt — record header bytes read twice
- **Issue**: `readOneAt` first calls `readRecordBytesAt(pos, xlogRecordHeaderSize)` to decode the header, then calls `readRecordBytesAt(pos, paddedTotal)` to fetch the full record — re-reading (and re-skipping page headers for) the first 24 header bytes, i.e. two buffer/disk fetches of the same header region per record.
- **Why**: streaming walsender hot path; each `readBytesAt` may hit the memring twice or issue pread syscalls.
- **Suggestion**: read `paddedTotal` in a single `readRecordBytesAt` call and decode the header from `body[:xlogRecordHeaderSize]`. Halves header reads.
- **Severity**: medium

### iterator.go:readBytesAt / readRecordBytesAt / readSegmentSlice — per-record allocation + per-chunk file open
- **Issue**: each record read allocates a header buffer, a body buffer, a scratch `out` in `readBytesAt`, and a fresh `buf` per segment chunk; `readSegmentSlice` does `os.Open`/`filepath.Join`/`FormatSegmentNameTLI` per call, so a record spanning a segment boundary opens the segment twice (once per read).
- **Why**: the iterator contract returns fresh copies, so full reuse isn't free, but the header/body scratch buffers are transient and the segment handle could be cached across calls.
- **Suggestion**: reuse a scratch buffer on the iterator (reset on Next) and cache the last-opened segment `*os.File` keyed by (segNo, tli) to skip repeated opens.
- **Severity**: medium

### iterator.go:Next — page-header loop recomputes header size per iteration
- **Issue**: the inner page-skip loop calls `pageHeaderSizeAt(it.pos, it.segSize)` and a `%` per iteration; cheap but on the streaming path.
- **Why**: minor.
- **Suggestion**: fine as-is.
- **Severity**: low

### insertion_tracker.go — no significant issues
- **Issue**: `lowestActiveLSN` does 8 atomic loads, bounded and lock-free by design.
- **Why**: not hot enough to matter; the sentinel design avoids branches at call sites.
- **Suggestion**: leave as-is.
- **Severity**: low

### mem_ring.go:Append / ReadAt — no significant issues
- **Issue**: single lock + memcpy; hit/miss counters sharded already.
- **Why**: writer-serial and reader-serial by design.
- **Suggestion**: leave as-is.
- **Severity**: low

### decoder.go — no significant issues
- **Issue**: none. `ApplyCommit` iterates the change list once; errors use `fmt.Errorf` only on failure paths.
- **Why**: orchestration code, not hot.
- **Suggestion**: leave as-is.
- **Severity**: low

### format.go:encodeRecordXLog / wrapXLogMainData — two allocations per WAL record
- **Issue**: `wrapXLogMainData` allocates a fresh `[]byte` for the wrapped main-data chunk, then `encodeRecordXLog` allocates a second `out` buffer of `maxAlignXLog(realLen)` and copies the wrapped bytes in. That is two allocations + one copy per record on the WAL write hot path.
- **Why**: per-record cost on the critical append path; small records are the common case (a 2-byte header + payload allocation each).
- **Suggestion**: size `out` once as `maxAlignXLog(xlogRecordHeaderSize + headerSize + len(payload))` and write the tag/length header directly into `out[xlogRecordHeaderSize:...]`, eliminating the intermediate `wrapped` allocation and the copy.
- **Severity**: medium

### format.go:unwrapXLogMainData — defensive copy that could alias
- **Issue**: both short and long arms allocate `make([]byte, n)` and `copy` the payload out of `data` when `data[2:2+n]` is already a valid subslice.
- **Why**: read/recovery path; the extra allocation+copy is pure overhead unless callers mutate the returned slice (the classifier/decoder treats tuples as immutable).
- **Suggestion**: return `data[2:2+n]` / `data[5:5+n]` directly and let callers copy if they need ownership; or document the defensive-copy intent.
- **Severity**: low

### format.go:FormatSegmentNameTLI — fmt.Sprintf for a fixed-width hex name
- **Issue**: `fmt.Sprintf("%08X%08X%08X", …)` allocates via reflection; also `%08X` uses uppercase which is correct, but strconv would be faster.
- **Why**: called per segment creation / naming, not per record — low frequency.
- **Suggestion**: preallocate a 24-byte buffer and write three `strconv.FormatUint(…, 16)` zero-padded values (or keep for clarity given frequency).
- **Severity**: low

### format_detect.go — no significant issues
- **Issue**: reads dir, sorts candidates — one-time startup detection, tiny slices.
- **Why**: not hot.
- **Suggestion**: leave as-is.
- **Severity**: low

### index_am_refusal.go:preflightIndexAMRecords — no significant issues
- **Issue**: map + sort + `strings.Join`; runs only on the startup refusal path.
- **Why**: error path, executed once per refused start.
- **Suggestion**: leave as-is.
- **Severity**: low

### insert_pos.go:reserve / reserveLocked — no significant issues
- **Issue**: `defer` unlock in the hot reserve path adds minor overhead; two 64-bit divisions per reservation.
- **Why**: per-reservation cost is dominated by encode + buffer write; mutex is the documented design choice (no 128-bit CAS).
- **Suggestion**: could hoist `segSize` into a local and avoid `defer`, but the gain is marginal.
- **Severity**: low

### padded_mutex.go — no significant issues
- **Issue**: none; cache-line padding and 8-stripe array are the point of the file.
- **Why**: dead code until slice B call-site rewrite; tiny.
- **Suggestion**: leave as-is.
- **Severity**: low

### pg_assembled_emit.go — envelope + body + mainData allocation chain per PG record
- **Issue**: each `Encode*PG` builds `mainData`, then `assembleXLogRecord` allocates `body`, then `framePGAssembled` allocates a third buffer copying body behind the 7-byte envelope; `encodeAssembledXLog` then allocates the final padded `out` and copies again. Four allocations + three copies per PG-format record on the append path.
- **Why**: per-record on the WAL write hot path (heap insert/update/delete, xact commit/abort are the most frequent records).
- **Suggestion**: have `assembleXLogRecord`/`framePGAssembled` write directly into a single growable buffer (pre-size to envelope + body), or have `framePGAssembled` take ownership and append the envelope prefix to the body buffer. At minimum, reserve the envelope in the same allocation.
- **Severity**: medium

### pg_assembled_emit.go:heapHeaderPlusData / EncodeHeapInsertPG — duplicate slice construction
- **Issue**: `EncodeHeapInsertPG` manually builds the same `xl_heap_header + tuple-past-fixed-header` payload that `heapHeaderPlusData` builds; the update/delete encoders all use `heapHeaderPlusData`. Duplicated 4-line append sequence (also seen inline in `EncodeHeapDeletePG`).
- **Why**: code duplication, not runtime waste, but the fix is trivial.
- **Suggestion**: use `heapHeaderPlusData` in the insert and delete encoders too.
- **Severity**: low

### pg_xact_parse.go — no significant issues
- **Issue**: single linear chunk walk, bounded subxact allocation. Fine.
- **Why**: commit/abort frequency is low.
- **Suggestion**: leave as-is.
- **Severity**: low

### pg_xlog_decode.go:parseXLogRecordData — cloneXLogBytes per block/main-data chunk
- **Issue**: every block's `Data` and the `MainData` are cloned via `cloneXLogBytes` (allocate + copy) even though `wrapped` is itself already a fresh per-record slice from `decodeRecordXLogDetailed`'s caller (readers own it, CRC already verified).
- **Why**: decode path on recovery/streaming; doubles per-record memory traffic for the data regions.
- **Suggestion**: alias (`wrapped[payloadOff:payloadOff+len]`) if the caller's buffer lifetime outlives the decoded record (verify each caller); otherwise document why the defensive copy is required.
- **Severity**: medium

### pgoutput.go:pgoPhysEpoch — reconstructs the PG epoch per column decode
- **Issue**: `pgoPhysEpoch()` calls `time.Date(2000,1,1,…)` on every `timestamp/timestamptz/date/time/timetz` column decode inside `pgoDecodePhysicalValue`, i.e. once per temporal column per row on the logical-replication encode path.
- **Why**: `time.Date` is not free (calendar math); replicated tables with temporal columns hit it per cell.
- **Suggestion**: hoist to a package-level `var pgoPhysEpoch = time.Date(2000,1,1,…)` (and likewise for the `pgoTimestamp` epoch used in Begin/Commit).
- **Severity**: medium

### pgoutput.go:appendCString — `[]byte(s)` conversion before append
- **Issue**: `append(b, []byte(s)...)` converts the string to a byte slice; `append(b, s...)` appends a string directly without the intermediate conversion/allocation.
- **Why**: called per column name and per replicated string value on the pgoutput hot path.
- **Suggestion**: use `append(b, s...)`.
- **Severity**: low

### pgoutput.go:writeRelation — 'd' replica-identity + flag byte per column
- **Issue**: none of significance; the per-column loop appends into a pre-sized buffer.
- **Why**: relation messages are once-per-relation-per-session.
- **Suggestion**: leave as-is.
- **Severity**: low

### pgoutput_decoder.go — minor defensive copies
- **Issue**: `reader.bytes` allocates a fresh slice + copy for every text column value, and `decodeTupleBody`/`decodeRelation` pre-allocate `make` slices; all on the apply-worker decode path. Error paths allocate via `fmt.Errorf` (fine). `cstring` does a byte scan (fine).
- **Why**: the input payload is a freshly read message owned by the decoder, so `bytes` could alias instead of copy.
- **Suggestion**: return `r.buf[r.off:r.off+n]` if callers never mutate the decoded columns (verify apply worker), or keep as-is for safety. Low priority.
- **Severity**: low

### predict_emitted_size.go / predict_xlog_record_len.go — no significant issues
- **Issue**: pure arithmetic, zero allocation, correctly documented as mirrors of the emit/encode size math.
- **Why**: designed explicitly to avoid encode-twice on the hot path.
- **Suggestion**: leave as-is.
- **Severity**: low

### publish_visibility.go — no significant issues
- **Issue**: three composed calls, each nil-safe; no redundant work.
- **Why**: dead code until slice B rewrite, tiny.
- **Suggestion**: leave as-is.
- **Severity**: low

### reader.go:readStreamFrom — stream slice grows without a capacity hint
- **Issue**: `stream := make([]byte, 0)` then `append(stream, b...)` per segment; the backing array reallocates and copies the whole prefix on each growth step, so loading an N-segment WAL is O(N²) copying (amplified by 16 MiB segments).
- **Why**: crash-recovery startup path — the whole retained WAL is loaded into RAM anyway; the growth pattern adds avoidable copies.
- **Suggestion**: first pass to count segment files, or grow by a large factor (e.g. `make([]byte, 0, segSize*2)` and keep appending), or `bytes.Buffer` which amortises.
- **Severity**: medium

### reader.go:readAllPageAware — header extracted and decoded twice per record
- **Issue**: the loop calls `extractRecordBytes(…, xlogRecordHeaderSize)` + `DecodeXLogRecordHeader` for the header, then `extractRecordBytes(…, paddedTotal)` which re-assembles the first 24 bytes again, and `decodeRecordXLogDetailed(fullBytes)` decodes the header a second time (plus CRC).
- **Why**: in-memory walk, so the redundancy is a 24-byte copy + a cheap decode per record, not syscalls.
- **Suggestion**: skip the standalone header extract when `total` is available cheaply, or reuse the header decoded from `fullBytes[:24]`; low payoff.
- **Severity**: low

### reader.go:openSegmentFile — FormatSegmentName computed twice on the slow path
- **Issue**: slow path computes `FormatSegmentName(segNo)` twice (once for `suffix`, once for the error message) and does a full `ReadDir` scan per missing segment.
- **Why**: only hit on non-TLI1 clusters and gap ends; not hot.
- **Suggestion**: compute the name once; cache the directory scan. Trivial.
- **Severity**: low

### reader.go:isPreallocatedTail — 64 KiB stack array per call
- **Issue**: allocates a `var zeros [64 * 1024]byte` stack array on each call; only used on error/EOS paths, so impact is minimal but the stack footprint is notable.
- **Why**: error path only.
- **Suggestion**: use `bytes.Equal(seg, make([]byte, len(seg)))` once, or a small package-level zero buffer with a mutex — or leave as-is given frequency.
- **Severity**: low

### reader_early_end.go — no significant issues
- **Issue**: page-by-page scan only on the refusal path (once per failed walk); a fresh `xlogPageValidator` per page is cheap.
- **Why**: error/refusal path.
- **Suggestion**: leave as-is.
- **Severity**: low

### recovery_cache.go — no significant issues
- **Issue**: mutex-guarded memoization decoding the WAL once for all startup modules; `filepath.Clean` recomputed per ReadAll is trivial.
- **Why**: startup path, single decode intended.
- **Suggestion**: leave as-is.
- **Severity**: low

### relmap.go — no significant issues
- **Issue**: fixed 524-byte buffers, single-pass encode/decode.
- **Why**: not hot (CREATE DATABASE / recovery).
- **Suggestion**: leave as-is.
- **Severity**: low

### reorder.go:foldChanges — allocates a copy even when nothing folds
- **Issue**: `foldChanges` allocates `out := make([]Change, 0, len(in))` unconditionally whenever `len(in) >= 2`, so a commit of N inserts (no delete/insert pairs — the common case on the logical-decoding path) pays a full copy + allocation of the whole change slice.
- **Why**: per-commit on the logical-decoding path; the fold is a no-op for typical append-only xacts.
- **Suggestion**: first scan for a (Delete,Insert) pair; if none, return `in` unchanged.
- **Severity**: medium

### recovery.go:Decode* helpers — defensive tuple copies per record
- **Issue**: `DecodeHeapInsert`/`DecodeHeapDelete`/`DecodeHeapHotUpdate`/`DecodeHeapUpdate`/`DecodeBtreeInsert` and the `Decode*` payload helpers (`DecodeBtreeVacuum`, `DecodeBtreeNewRoot`, `DecodeHeapMultiInsert`, …) all `make` + `copy` the tuple/item bytes out of `payload` on the recovery path, where the payload slice is already freshly decoded per record (owned by the caller, immutable during dispatch).
- **Why**: recovery is a startup path, but this is per-record (multi-GB WAL replays) — the copies are pure overhead unless the caller mutates the slices.
- **Suggestion**: alias the payload region directly; document that decoded slices must be treated as read-only (the classifier and replay already treat tuples as immutable).
- **Severity**: medium

### recovery.go:redoHeapPageForBlock — page buffer allocated per call
- **Issue**: `page := make(storage.Page, storage.BlockSize)` plus a `zero := make(...)` per call, even for the common skip path; every replayed heap record allocates an 8 KiB buffer.
- **Why**: per-record on the recovery hot path.
- **Suggestion**: reuse a single buffer when skipping, or take/return a pooled page (e.g. sync.Pool of `[8192]byte`) across the replay loop. Note `WriteBlock`/`ReadBlock` may force copies anyway — measure before refactoring.
- **Severity**: low

### repllog.go — no significant issues
- **Issue**: just event-name string constants.
- **Suggestion**: leave as-is.
- **Severity**: low

### replmon.go — no significant issues
- **Issue**: per-entry atomics + snapshot sorting on read; register/unregister take the registry mutex (rare).
- **Why**: view reads are infrequent.
- **Suggestion**: leave as-is.
- **Severity**: low

### reserve_emitted.go — no significant issues
- **Issue**: two `predictEmittedSize` calls max, only on the rare cross-segment path; pure arithmetic.
- **Why**: documented cost trade-off.
- **Suggestion**: leave as-is.
- **Severity**: low

### retention.go — no significant issues
- **Issue**: slot listing + unlink loop per checkpoint only.
- **Why**: not hot.
- **Suggestion**: leave as-is.
- **Severity**: low

### rmgr_map.go — no significant issues
- **Issue**: static switch table.
- **Suggestion**: leave as-is.
- **Severity**: low

### segment_pad.go:buildSegmentPadRecord — two allocations per pad
- **Issue**: allocates `body` then `out` and copies; pads are rare (segment crossing) so the impact is tiny.
- **Why**: rare path.
- **Suggestion**: build directly into `out` (header+body in one buffer) for cleanliness; low value.
- **Severity**: low

### segment_pad_emit.go — no significant issues
- **Issue**: rare cross-segment path; allocations bounded.
- **Suggestion**: leave as-is.
- **Severity**: low

### seq_log.go — no significant issues
- **Issue**: sequence rewrites are infrequent; single allocations.
- **Suggestion**: leave as-is.
- **Severity**: low

### slot_decoder.go — no significant issues
- **Issue**: one iterator per slot, sequential loop.
- **Suggestion**: leave as-is.
- **Severity**: low

### slots.go:writeSlotLocked — full rewrite + double fsync per slot update
- **Issue**: `AdvanceConfirmedFlushLSN`/`AdvanceCatalogXmin`/`InvalidateLagging` each call `writeSlotLocked`, which does tempfile write + `tmp.Sync()` + rename + open-final + `f.Sync()` + `d.Sync()` — a state-file rewrite with 2–3 fsyncs per standby status update / commit advance.
- **Why**: walsender status replies arrive frequently (per second or faster under streaming), so this is a real per-reply disk write chain. PG also persists slot state, but goopg writes on every LSN advance rather than on a cadence.
- **Suggestion**: debounce/batch persistence (e.g. write on a timer or on change-threshold), or at least skip the dir fsync when the file exists and only rename; verify crash-safety trade-off first.
- **Severity**: medium

### slots.go:readSlotFile — double string/[]byte conversion on JSON fallback
- **Issue**: `[]byte(strings.TrimSpace(string(body)))` converts body→string→[]byte for the legacy JSON path.
- **Why**: only legacy files, at startup.
- **Suggestion**: use `bytes.TrimSpace(body)`.
- **Severity**: low

### slots_pg.go — no significant issues
- **Issue**: single fixed-size buffer + CRC; infrequent.
- **Suggestion**: leave as-is.
- **Severity**: low

### snapshot.go — no significant issues
- **Issue**: full catalog copy once per slot creation.
- **Suggestion**: leave as-is.
- **Severity**: low

### stream_replayer.go — no significant issues
- **Issue**: per-record `mu` lock + atomic counters; `ApplyRecord` dominates.
- **Suggestion**: leave as-is.
- **Severity**: low

### stripe_append.go / stripe_append_emitted.go / stripe_writer_core.go — no significant issues
- **Issue**: all dead code until the slice B call-site rewrite; nil-checks and error paths are the bulk.
- **Suggestion**: leave as-is.
- **Severity**: low

### subscriber_mon.go — no significant issues
- **Issue**: mirror of replmon, view-read infrequent.
- **Suggestion**: leave as-is.
- **Severity**: low

### sync_linux.go / sync_other.go — no significant issues
- **Issue**: thin syscall wrappers.
- **Suggestion**: leave as-is.
- **Severity**: low

### syncrep.go:rule.satisfied (ANY mode) — allocates + sorts on every release check
- **Issue**: the `syncRepRuleAny` branch builds a fresh `vals []uint64` and `sort.Slice` on every `releaseWaitersLocked` pass — which runs on every `UpdateStandbyProgress` (per standby status reply) and on every waiter list iteration.
- **Why**: sync-rep release path; ANY quorum with N names does an O(N log N) sort per status update.
- **Suggestion**: keep the named standbys' LSNs in the `syncRepRule`/registry in a way that avoids reallocating per check (e.g. reuse a scratch slice on the SyncRep struct), or compute the k-th largest without a full sort (partial selection) when N is small. Low impact at typical N (≤ a handful).
- **Severity**: low

### syncrep_parse.go — no significant issues
- **Issue**: parser runs only on GUC reload.
- **Suggestion**: leave as-is.
- **Severity**: low

### tail_publisher.go — no significant issues
- **Issue**: CAS loop + 8 atomic loads; documented.
- **Suggestion**: leave as-is.
- **Severity**: low

### timeline_history.go — no significant issues
- **Issue**: history files are tiny and rare.
- **Suggestion**: leave as-is.
- **Severity**: low

### wal_buffer.go — no significant issues
- **Issue**: single lock-free memcpy paths; modulo arithmetic cheap.
- **Suggestion**: leave as-is.
- **Severity**: low

### wal_buffer_publish_tail.go — no significant issues
- **Issue**: CAS-max loop, minimal.
- **Suggestion**: leave as-is.
- **Severity**: low

### wal_write_lock.go — no significant issues
- **Issue**: TryLock fast path + generation channel; group-commit design.
- **Suggestion**: leave as-is.
- **Severity**: low

### writer.go:Append / tryAppend — predictXLogRecordLen + AppendXLogPayload predict again
- **Issue**: `tryAppend`/`appendPGCompat` call `predictXLogRecordLen(payload)` to size the reservation, then `AppendXLogPayload` → `appendXLogPayload` → `predictXLogRecordLen(payload)` again (and `predictEmittedSize` again inside the reserve). The prediction is recomputed on the hot append path.
- **Why**: per-record redundant arithmetic on the critical path (twice per append). It's pure int math, so the cost is small, but it's exactly the kind of duplicate work the review targets.
- **Suggestion**: thread the already-computed `paddedLen` (and a known `prev`) into the append call to avoid re-predicting, or have `AppendXLogPayload` accept the predicted length. Low payoff unless profiling shows it.
- **Severity**: low

### writer.go:walSyncStage — allocates and sorts a dirty-segment slice per flush barrier
- **Issue**: every flush allocates `dirty := make([]uint64, 0, len(s.dirty))` and `sort.Slice`s it; a single-segment flush (the common case) still allocates + sorts a 1-element slice.
- **Why**: per-commit/flush critical path (group commit). Small (few segments) but per-barrier allocation.
- **Suggestion**: reuse a scratch slice on `state` (reset length each call) to avoid the allocation; skip the sort when `len(dirty) <= 1`.
- **Severity**: low

### writer.go:detectWritePos / loadState — repeated directory scans at startup
- **Issue**: `loadState` calls `DetectWALFormat` (one `ReadDir`), then `detectWritePos` does another `ReadDir` and a per-segment `os.ReadFile` scan of the last segment; `scanLastSegmentEnd` may be called twice for the same last segment (once in the phantom-drop loop, once in the main loop).
- **Why**: startup only; not hot, but the duplicate `ReadDir` + duplicate last-segment scan is avoidable.
- **Suggestion**: pass the already-listed entries / last-segment size between the calls, or scan once. Low value.
- **Severity**: low

### xlog_assemble.go:assembleXLogRecord — header/payload built via repeated append with no capacity hint
- **Issue**: `header` and `payload` are grown with bare `append` (no pre-sizing); for an FPI record `payload = append(payload, imgBytes...)` reallocates a few times as images grow, and the final `append(header, payload...)` copies the whole payload again into the header slice.
- **Why**: per-record on the WAL write path (all assembled PG records). The double-copy of the payload region is the notable waste.
- **Suggestion**: pre-size the final buffer to `len(header)+len(payload)` and `copy` once, or build into a single growable slice; at minimum give `payload` a capacity hint.
- **Severity**: medium

### xlog_emit.go — no significant issues
- **Issue**: `emitWithPageHeaders` pre-sizes output; `extractRecordBytes` was already fixed to cap allocation.
- **Suggestion**: leave as-is.
- **Severity**: low

### xlog_page.go / xlog_record.go — no significant issues
- **Issue**: fixed-size encode/decode helpers; CRC tables are singletons.
- **Suggestion**: leave as-is.
- **Severity**: low

