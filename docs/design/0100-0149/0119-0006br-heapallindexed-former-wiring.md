# M0119-0006 — heapallindexed wire-up: the HeapEntryFormer adapter

**Status:** landed.

## Context

`bt_index_check(index, heapallindexed, checkunique)` and
`bt_index_parent_check(index, heapallindexed, rootdescend, checkunique)`
accept `heapallindexed` for call-shape compatibility with pg_amcheck but run
the tier never — `operators_bt_index_check.go` hard-wires it off
("heapallindexed needs MVCC-aware heap-tuple → index-key extraction … forming
the heap entry set is the missing piece"). The engine half is already landed
(`internal/access/amcheck`):

- `CollectBtreeLeafEntries(idxSrc, keyFmt)` — fingerprint set from the index
  leaf level (`heapallindexed_relation.go`).
- `CollectHeapIndexEntries(heapSrc, nblocks, form)` — probe set from the heap
  block walk (`heapallindexed_heapscan.go`), driven by an injected
  `HeapEntryFormer`.
- `VerifyBtreeHeapAllIndexedRelation(idxSrc, keyFmt, heapEntries, idxName,
  tblName, seed)` — Bloom-filter probe; reports the upstream-verbatim
  `heap tuple (%u,%u) from table %q lacks matching index tuple within index %q`.

This slice is the missing wire-layer adapter: the catalog-/MVCC-coupled
`HeapEntryFormer` plus its invocation from `evalBtIndexCheck`.

## Upstream contract (postgres/contrib/amcheck/verify_nbtree.c)

- `bt_check_every_level` registers `GetTransactionSnapshot()` before
  fingerprinting (`:443`), then — after the structural level walk — runs
  `table_index_build_scan(heaprel, …, ii_Concurrent=true, …,
  bt_tuple_present_callback)` (`:549-589`). The scan's snapshot is an MVCC
  snapshot (even under `ii_Concurrent` — `heapam_handler.c:1255`); tuples
  are yielded only when `HeapTupleSatisfiesVisibility`/MVCC-visible, so
  in-flight-xmin tuples are NEVER probed. HOT members are yielded under
  the chain-root TID with the live member's values
  (`heapam_handler.c:1662-1700`, root gathered via `heap_get_root_tuples`,
  `pruneheap.c:1785-1838`).
- `bt_tuple_present_callback` (`:2782`) forms `index_form_tuple` output,
  normalizes (`bt_normalize_tuple`), and Bloom-probes; a miss raises
  ERRCODE_DATA_CORRUPTED "heap tuple (%u,%u) … lacks matching index tuple
  within index %q".
- Bloom seed: `pg_prng_uint64` per run — anti-adversarial only; any seed is
  correct, worst case affects the false-positive rate, never the verdict's
  soundness direction (a true missing entry is always reported).

## Design

New `btIndexHeapAllIndexed` in `internal/executor/operators_bt_index_check.go`,
called from `evalBtIndexCheck` only after the structural tiers (and
`btIndexCheckUnique`) return zero findings — mirroring upstream, where an
earlier `ereport(ERROR)` aborts before the heapallindexed phase starts.

1. **Argument.** `heapallindexed` is positional arg 1 in both call shapes
   (`bt_index_check(index, heapallindexed, checkunique)`;
   `bt_index_parent_check(index, heapallindexed, rootdescend, checkunique)`).
   Absent/NULL/false → tier skipped. Named-arg spellings arrive in the
   declared order already (M0097-0003), same as `btIndexCheckUnique` relies on
   for `checkunique`.
2. **Snapshot gate.** `ctx.Snap.Xmax == 0` (unseeded snapshot) → skip, same
   guard `btIndexCheckUnique` uses (`operators_bt_index_check.go:267`).
3. **Heap source.** `ctx.Catalog.RelFileNode(idx.Table)` + `Pool.NBlocks` +
   the same pin-and-copy `PageSource` the index side uses.
4. **Former** (closure over ctx/idx/tbl/cols/keyExprs/predExpr/sctx), per
   `CollectHeapIndexEntries` LP_NORMAL visit, mirroring the CREATE INDEX build
   path `collectBTreeEntries` (`operators_ddl.go:14785+`) — because the probe
   set must equal "tuples a fresh build would index", the same predicate the
   build applies is the faithful inclusion rule:
   - `storage.ParseHeapTuple` the raw bytes (header included per the former
     contract).
   - `transam.TupleVisible(t.Header, ctx.Snap, ctx.Tx.XID, ctx.CmdID,
     ctx.comboStore(), ctx.MultiXact)` — snapshot visibility, identical to
     `btIndexCheckUnique`'s predicate and the faithful analog of upstream's
     MVCC scan. Agent-review correction: the earlier draft used
     `isLiveForUniqueCheck`, which reports in-flight-xmin tuples live —
     upstream's MVCC snapshot never probes them, so under concurrent
     writers that predicate could raise a spurious "lacks matching index
     tuple" before the inserter's index write lands.
   - `DecodeHeapTupleRowInto(row, tbl.Columns, t, sctx)`; decode failure →
     former error (probe-set completeness invariant — a silently dropped
     tuple manufactures a false "lacks matching index tuple"; the engine
     contract requires surfacing). Maps to XX000 like other read errors.
   - enum KindString→KindEnum fixup on key columns (same as the build —
     without it a varchar-typed enum key re-encodes to different bytes).
   - `predExpr` (from `idx.Predicate` via `optimizer.ResolveIndexPredicate`)
     false/NULL/error → excluded, exactly as the build skips non-matching
     rows.
   - `ctx.indexBuildEntryKey(idx, cols, keyExprs, row, tid, pos)` —
     `hasNullKey` → excluded (goopg stores no NULL-keyed entries; the build
     skips them). `key == nil` → excluded as well (all-expression index
     whose eval/encode produced nothing — the build skips those at
     `operators_ddl.go:14917`); probing an empty key would manufacture a
     false positive.
   - **HOT root substitution.** `t.Header.IsHeapOnly()` → the index entry
     points at the chain-root line pointer, not the member: re-pin the block
     and find the root by walking each candidate root
     (`eachHeapChainMember`, operators_index.go:133 — follows LP_REDIRECT
     stubs and CTID links): candidates are LP_REDIRECT stubs plus non-heap-only
     LP_NORMAL items (every non-member tuple is a root of a length-1 chain;
     only a chain containing our member matters). Root found → emit
     `LeafEntry{Key: key, TID: {blk, rootOff}}`; not found → treat as
     corruption-class error (a heap-only tuple with no reachable root is
     heap damage).
   - Non-heap-only tuples emit their own TID.
5. **Compose.** `VerifyBtreeHeapAllIndexedRelation(idxSrc, keyFmt,
   heapEntries, idx.Name, idx.Table.Name, fixedSeed)`; findings map to the
   existing `XX002` report path (`ExecError{Code:"XX002", Detail:
   btIndexReportDetail}`).

Deliberate divergences, recorded not hidden:

- Fixed Bloom seed (constant) instead of per-run `pg_prng_uint64`: upstream
  randomizes against adversarial collision; goopg's fingerprints are internal
  — determinism is preferred for testability.
- Index entries written by a then-aborted INSERT linger (no abort-time
  index cleanup, matching PG); the dead heap tuple is not probed, and the
  lingering entry never false-positives. The reverse hazard — a
  snapshot-visible heap row with no index entry because goopg could not
  encode its key (e.g. an unencodable expression result the write path
  silently skips) — reports a genuine divergence, not corruption: PG
  would have written the entry.
- TOAST/normalization (`bt_normalize_tuple` exists for compressed-vs-not
  datum divergence) is unnecessary: goopg's key encoding is value-based, not
  storage-representation-based, so index_form_tuple equivalence is byte-exact.

## Tests (internal/executor/operators_bt_index_check_test.go)

- `TestBtIndexCheck_HeapAllIndexedClean` — healthy table+index, all call
  shapes with `heapallindexed := true` → no error (the no-false-positive
  gate).
- `TestBtIndexCheck_HeapAllIndexedDetectsUnindexedTuple` — append a heap
  tuple directly via `Pool`/`PageAddHeapTuple` bypassing index maintenance →
  XX002 "lacks matching index tuple".
- HOT arm: UPDATE a row after indexing (heap-only member) → still clean —
  exercises root-TID substitution.
- Partial-index arm: row failing the predicate must NOT report (build-side
  exclusion parity).

## Deferred (not this slice)

- `rootdescend` tier (upstream guards it to heapkeyspace v4 indexes).
- Multi-database pg_amcheck orchestration and unsupported-AM/type corruption
  arms of 003/004 (feature-blocked: hash/gist/gin/brin/spgist, box/int4range/
  int4[], STORAGE-EXTERNAL TOAST corruption layout — goopg's TOAST is
  chunk-relation divergent).
- HEAP_UPDATED-stamped invariant in verify_heapam (engine comment,
  verify_heapam.go:70-73 — goopg never stamps HEAP_UPDATED).

## References

- `postgres/contrib/amcheck/verify_nbtree.c` — `:414-462` (snapshot +
  fingerprint setup), `:543-589` (build-scan), `:2782-2839` (callback).
- `docs/design/0100-0149/0110-0007-amcheck-heapallindexed.md` (engine).
- `internal/executor/operators_ddl.go` `collectBTreeEntries` (build recipe).
