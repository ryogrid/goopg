# WAL (XLog) — Bug Review 2026-08-31

Files: append_xlog_payload.go, archive_recovery.go, archive_restore.go, checkpointer.go, classifier.go, decoder.go, format.go, format_detect.go, index_am_refusal.go, insert_pos.go, insert_pos_publish.go, insertion_tracker.go, iterator.go, mem_ring.go, mem_ring_concurrent.go, padded_mutex.go, pg_assembled_emit.go, pg_xact_parse.go, pgoutput.go, pgoutput_decoder.go, predict_emitted_size.go, predict_xlog_record_len.go, publish_visibility.go, reader.go, reader_early_end.go, recovery.go, recovery_cache.go, relmap.go, reorder.go, repllog.go, replmon.go, retention.go, rmgr_map.go, segment_pad.go, segment_pad_emit.go, seq_log.go, slot_decoder.go, slots.go, slots_pg.go, snapshot.go, stream_replayer.go, stripe_append.go, stripe_append_emitted.go, stripe_writer_core.go, subscriber_mon.go, sync_linux.go, sync_other.go, syncrep.go, syncrep_parse.go, tail_publisher.go, timeline_history.go, wal_buffer.go, wal_buffer_publish_tail.go, wal_write_lock.go, writer.go, xlog_assemble.go, xlog_emit.go, xlog_page.go, xlog_record.go, pg_xlog_decode.go

Findings count: 5

---

### `append_xlog_payload.go:appendXLogPayload` — error from emitWithPageHeaders swallowed
- **Bug**: `out, _ := emitWithPageHeaders(...)` discards the returned error. If page-header emission fails, the record silently proceeds with a nil/truncated `out`.
- **When it triggers**: Any failure path inside `emitWithPageHeaders`. Currently dead code (unused foundation), so latent.
- **Fix**: Propagate the error.
- **Severity**: low

### `insert_pos.go:reserveLocked` — onCrossSegment return value (padded bool) always discarded
- **Bug**: The `onCrossSegment` hook returns `padded bool` — true when a real pad record was written, false when the gap was only zero-filled. In `reserveLocked` the return value is unconditionally discarded via `_ = t.onCrossSegment(...)`, and `t.prev = boundary` is set regardless. When the gap is too small for a real pad record, the next reservation's `xl_prev` points to `old` (the gap start) — a zero-filled region with no real record, breaking the `xl_prev` chain.
- **When it triggers**: Segment crossing where the gap is smaller than the minimum record size (< ~24 bytes).
- **Fix**: Only set `t.prev = boundary` when `onCrossSegment` returned true.
- **Severity**: low

### `pgoutput.go:encodePgoTuplePhysical` — column-offset walk can mis-advance on null columns when bitmap is present
- **Bug**: Null columns are handled via the bitmap check. When the bitmap says NOT-NULL but a column has no bytes (off >= len(body)), the column is silently treated as null while `off` was aligned (`pgoPhysicalAlign`) but not advanced — desynchronizing every subsequent column's offset by up to 7 bytes. This produces silently-wrong replicated values.
- **When it triggers**: Any tuple where a bitmap bit disagrees with the body length — e.g. a cross-engine or mis-sized tuple. In practice goopg's own encoder keeps them consistent.
- **Fix**: Return a hard error when the bitmap says NOT-NULL but `off >= len(body)`.
- **Severity**: low

### `slot_decoder.go:Run` — ConfirmedFlushLSN never advances for PG-format commit records
- **Bug**: The advance logic keys on `len(rec.Payload) > 0 && rec.Payload[0] == RecordKindXactCommit`. PG-format records (assembled path, `r.XLog != nil`) have `Payload == nil` and are dispatched by `classifyDecodedXLog` → `d.ApplyCommit`. Such commit records never match the check, so `AdvanceConfirmedFlushLSN` is never called for them.
- **When it triggers**: A logical slot decoding PG-assembled WAL (`EncodeXactCommitPG`). The slot's ConfirmedFlushLSN stays at creation time, so a restart replays every transaction since slot creation.
- **Fix**: Advance on PG-format commit records too (dispatch through the classifier's commit return).
- **Severity**: medium

### Files with no findings:
- `archive_recovery.go`, `archive_restore.go` — no bugs
- `checkpointer.go` — no bugs
- `classifier.go` — no bugs
- `decoder.go` — no bugs
- `format.go`, `format_detect.go` — no bugs
- `index_am_refusal.go` — no bugs
- `insert_pos_publish.go` — no bugs (same reserveLocked path)
- `insertion_tracker.go` — no bugs
- `iterator.go` — no bugs (WrittenLSN is 1-based, readBytesAt check is correct)
- `mem_ring.go`, `mem_ring_concurrent.go` — no bugs
- `padded_mutex.go` — no bugs
- `pg_assembled_emit.go` — encoders correct
- `pg_xact_parse.go` — chunk walk bounded, negative count rejected
- `pgoutput_decoder.go` — field reads bounded by reader.need
- `predict_emitted_size.go` — matches emitWithPageHeaders' arithmetic
- `predict_xlog_record_len.go` — matches encodeRecordXLog lengths
- `publish_visibility.go` — composition correct
- `reader.go` — page-walk, contrecord skip, tail-skip, CRC/rmid offsets all correct
- `reader_early_end.go` — page-rounding and scan logic correct
- `recovery.go` (encode/decode/replay helpers) — lengths, bounds, endianness consistent
- `relmap.go` — CRC, size checks correct
- `reorder.go` — foldChanges correct
- `recovery_cache.go` — single-decode-under-lock correct
- `repllog.go`, `replmon.go` — registries correct
- `retention.go` — keep-horizon logic correct
- `rmgr_map.go` — mapping table correct
- `segment_pad.go` — pad-record layout correct (short 255 boundary, long chunk header)
- `segment_pad_emit.go` — pad sizing vs page-header accounting correct
- `seq_log.go` — sequence page layout correct (no special-area overlap)
- `slots.go`, `slots_pg.go` — binary layout + CRC, lifecycle correct
- `snapshot.go` — no bugs
- `stripe_append.go` — error handling + END marker defer correct
- `subscriber_mon.go` — registry correct
- `sync_linux.go`, `sync_other.go` — trivial syscall wrappers
- `syncrep.go`, `syncrep_parse.go` — parser + wait/release logic correct
- `tail_publisher.go` — CAS-loop correct
- `timeline_history.go` — read/write/parse correct
- `wal_buffer_publish_tail.go` — CAS-max correct
- `wal_write_lock.go` — generation-close wakeup pattern correct (no lost wakeup)
- `xlog_emit.go` — page-header emit + xlp_rem_len correct
- `xlog_assemble.go` — block-ref layout correct

DONE

---

### `pgoutput.go:encodePgoTuplePhysical` — column-offset walk can mis-advance on null columns when bitmap is present
- **Bug**: Null columns are handled ONLY via the bitmap check (`bitmap[i/8]>>(uint(i)%8))&1 == 0`). When the bitmap is present, a null column hits `continue` without advancing `off` — correct (PG skips null column bytes). However the code also relies on `off >= len(body)` as a second null-detection fallback (`out = append(out, pgoColNull); continue`) for columns whose bitmap bit says NOT-NULL but which have no bytes. If the bitmap and the body disagree, a column is silently reported as null while `off` was aligned (`pgoPhysicalAlign`) but not advanced — desynchronizing every subsequent column's offset by up to 7 bytes. This produces silently-wrong replicated values (adjacent columns shifted) rather than an error.
- **When it triggers**: Any tuple where a bitmap bit disagrees with the body length — e.g. an ALTER TABLE ADD COLUMN column stored without bitmap, or a mis-sized tuple. In practice goopg's own encoder keeps them consistent; the risk is cross-engine tuples.
- **Fix**: When the null-bitmap bit says NOT-NULL but `off >= len(body)`, return a hard error instead of treating it as null and continuing.
- **Severity**: low (internal consistency normally holds; latent silent-corruption path)

---

### Files with no findings (batch 2):
- `pg_assembled_emit.go` — encoders checked for layout/order bugs; block-data assembly, chunk ordering, flag bytes all look correct
- `pg_xact_parse.go` — chunk walk bounded, negative count rejected
- `pgoutput_decoder.go` — field reads bounded by reader.need; `make([]byte, n)` from a uint32 length is a theoretical DoS but matches the local trusted source
- `predict_emitted_size.go` — matches emitWithPageHeaders' arithmetic exactly (verified against xlog_emit.go)
- `predict_xlog_record_len.go` — matches encodeRecordXLog lengths
- `publish_visibility.go` — composition correct; rings nil-safe per contract
- `reader.go` — page-walk, contrecord skip, tail-skip (S30.1), CRC/rmid offsets (header[16]/[17]/[20:24]) all correct
- `xlog_emit.go` — page-header emit + xlp_rem_len arithmetic correct

---

### `append_xlog_payload.go:appendXLogPayload` — error from emitWithPageHeaders swallowed
- **Bug**: `out, _ := emitWithPageHeaders(...)` discards the returned error. If page-header emission fails (e.g. size mismatch / encode failure), the record silently proceeds with a nil/truncated `out`. `appendXLogPayload` then returns nil error to the caller while the emitted bytes are wrong.
- **When it triggers**: Any failure path inside `emitWithPageHeaders` for this composer. Currently this function is dead code (unused foundation), so it is latent — but the caller `AppendBuiltEmitted`'s `len(out)==total` assertion is the only downstream guard, and a nil `out` there would panic rather than error cleanly.
- **Fix**: Propagate the error: `out, err := emitWithPageHeaders(...); if err != nil { return nil, err }`.
- **Severity**: low (dead code today; wrong error propagation when mounted)

### `insert_pos.go:reserveLocked` — onCrossSegment return value (padded bool) always discarded
- **Bug**: The `onCrossSegment` hook returns `padded bool` — true when a real pad record was written (advancing prev), false when the gap was only zero-filled (too small for a record). In `reserveLocked` (the legacy non-emitted path), the return value is unconditionally discarded via `_ = t.onCrossSegment(...)`, and `t.prev = boundary` is set regardless. When the gap is too small for a real pad record and only zero-filled, the next reservation's `xl_prev` points to `old` (the gap start) — a zero-filled region that contains no real record. This breaks the `xl_prev` chain for any tool that walks it.
- **When it triggers**: Segment crossing where the gap is smaller than the minimum record size (< ~24 bytes). The gap is zero-filled but the reservation after the boundary gets `prev = old` pointing at zeros.
- **Fix**: Only set `t.prev = boundary` when `onCrossSegment` returned true (a real pad was written). Otherwise keep `t.prev` unchanged (or set to `gapPrev`).
- **Severity**: low (gap < 24 bytes is rare; xl_prev chain is not used by pg_waldump for navigation, but breaks PG-compat readers that validate xl_prev)

---

### Files with no findings:
- `archive_recovery.go` — no bugs found (TLI=1 hardcode for archive fetches is documented local-only)
- `archive_restore.go` — no bugs found
- `checkpointer.go` — no bugs found (the `int64(lsn)-1` cast in `computeRedo` is safe for realistic LSNs)
- `classifier.go` — no bugs found
- `decoder.go` — no bugs found
- `format.go` — no bugs found
- `format_detect.go` — no bugs found
- `index_am_refusal.go` — no bugs found
- `insert_pos_publish.go` — no bugs found (same reserveLocked path, same documented discard)
- `insertion_tracker.go` — no bugs found
- `iterator.go` — no bugs found (WrittenLSN is 1-based, readBytesAt check is correct; Consumed check matches paddedTotal)
- `mem_ring.go` — no bugs found
- `mem_ring_concurrent.go` — no bugs found
- `padded_mutex.go` — no bugs found

