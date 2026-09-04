# EX3-02 — Dense-chunk build rows (design)

```
label: EX3-02-design | date: 2026-09-04
review: proceed-with-changes 2026-09-04 — F1 (GC-noscan Cut-2 blocker),
  F2 (shared-build teardown), F3 (build-only helper, MaterializeArena
  untouched), F4 (§3.7 cooperative-build correction), F5 (statement-context
  parenting + Generation check), F6 (ContextID ABA), F7 (pack assertion),
  plus citation drifts — all folded below before any cut starts
scope: hash-join build path only (buildLoopLeft/Right → ownedBuildRow →
       lazyHash/lazyIntHash/fillNullBuild); probe, batch-spill file
       discipline (EX3-03), and sort runs (EX3-04) out of scope
method: read-only (rg + read); no code touched, no commit
inputs: take3 10 §6 (dense_alloc analogue), 13 §5 (EX3 scope);
        internal/executor/operators_join_agg.go, datum.go, row_pool.go,
        slot.go, join_composite_key.go, join_outer_fill.go,
        internal/utils/mmgr/mctx.go;
        analysis/planner-refactor-take3/ex201-audit-20260904/README.md (EX2-01),
        analysis/planner-refactor-take3/ex203-measure-20260904/README.md (EX2-03)
gates: alloc arm (primary) + timing; values (-digest + -diff) + plan-shape pin
behaviour change: none — design only
```

## 0. What EX3-02 is and is not

Pack retained hash-build rows contiguously — the `dense_alloc` analogue
(10 §6: bump-allocate in the current `HashMemoryChunk` of `batchCxt`, one
`palloc` per chunk instead of per tuple, dedicated chunks for oversized
tuples) — replacing per-row build allocations on goopg's hash build path.

Explicitly NOT in scope: batch-file discipline (EX3-03), sort runs/merge
(EX3-04), sort-compare (EX3-05), skew residency (EX3-06), any plan change
(EX-P5), any Datum re-layout below 48 B (13 §0.1 non-goal).

Sequencing: legal now — EX1 outputs are landed (P4-01 build-side `Project`
narrowing 10→7/18→7; EX1-04 Cut 0/1), the EX2-01 audit map exists, and
EX2-03 closed pool sizing as measure-only. Per EX-P7 (narrowing before
batching math) the geometry work starts from narrowed widths, which it now
can.

## 1. Current per-row allocation chain (build path, with file:line evidence)

Two build loops, one retention primitive, three sinks.

**Loop entries.** `buildLoopRight` (`operators_join_agg.go:854-922`):
`o.right.Next()` per row (`:864`), `slotRow(rSlot)` view (`:871`), key eval
against a rebound `VirtualSlot` (`:879`, no copy — M0126-0003), then one of
three retention calls per row: `fileCompositeBuildRow(ownedBuildRow(r))`
(`:889`), `recordBuildNullKey(ownedBuildRow(r))` (`:891`, `:908`), or
`insertBuildRow(kd, ownedBuildRow(r))` (`:918`). `buildLoopLeft`
(`:781-839`) is symmetric (`:819`, `:821`, `:832`, `:835`). Every retained
build row passes through `ownedBuildRow` exactly once per loop — there is no
second copy on this path (the old `drainRowsBounded` double copy was folded
by M0127-P0.2; the `drainRowsCtx`/`drainRowsCtxCTID` folds by EX2-02a at
`:4123-4152`/`:4154-4188` are the same pattern on the non-loop drains).

**The retention primitive.** `ownedBuildRow`
(`operators_join_agg.go:931-938`):

- arena-backed row → `cloneRowOwned(row)` (`:933`);
- else `dup := make(Row, len(row)); copy(dup, row)` (`:935-936`).

`cloneRowOwned` (`datum.go:493-503`) = `acquireRow(len(src))` (`:494`) +
per-Datum `MaterializeArena` (`:500`). `MaterializeArena`
(`datum.go:433-462`) is a no-op for non-arena Datums and otherwise does
`buf := make([]byte, length); copy(buf, src)` per variable-width Datum
(`:459-460`), plus the big-numeric lane via `mmgr.Perm()`
(`:443-445`, `newBigNumericInCtx` at `:611-628` using `ctx.AllocBytes`).

**Per-row allocation census (non-batched build, narrow case first).** For a
build row of width w with k variable-width/big-numeric Datums:

1. **Row backing: 1 alloc/row, always.** Either `make(Row, w)` (non-arena
   lane, `operators_join_agg.go:935` — EX2-03 measured this lane bypasses
   the pool entirely) or `acquireRow` on the arena lane (`datum.go:494`).
   The pooled buffer is retained into the hash table and never returned, so
   the pool-hit rate on this path is ≈0% **by construction** (EX2-03: "and
   correctly so"). Pool buckets 0–64 pool every width identically, so
   P4-01's 10→7 narrowing only moves traffic between identical buckets —
   sizing cannot fix this slice.
2. **Variable-width payloads: k allocs/row.** One `make([]byte, length)`
   per arena-backed String/Bytes Datum (`datum.go:459`). Hot TPC-H
   string builds (e.g. Q9-class supplier/part names) pay this per row.
3. **Big-numeric payloads: 1 Perm-alloc/row when present.** Routed to
   `mmgr.Perm()` (`datum.go:444`) — permanent, never reset, i.e. a
   per-row leak for the process lifetime on the Q8-shape path.
4. **Bucket append: amortised.** `lazyHashInsertDatum`
   (`operators_join_agg.go:1152-1168`): `append(o.lazyHash[sk], row)`
   (`:1167`); int lane `:1158`; composite lane
   (`join_composite_key.go:236-241`) `:241`. Slice-growth allocs are
   O(log n) per bucket, not per row — not the target.
5. **Key string: 1 alloc/row on the string lane.** `datumKey(keyDatum)`
   (`:1166`) materialises the map key; `demoteIntHash` (`:1184-1197`)
   re-keys by moving Row headers (`:1194`), not payloads.

**Scale anchor.** EX2-03: the hottest Q9 path (`ownedBuildRow`, ~6 M rows)
is all of (1)+(2). The EX0-04 clone slice on Q9 (21.4%) lives in
`ownedBuildRow(make)` + `MaterializeArena` + arena-path `cloneRowOwned` —
none of which consult bucket sizing. That is the slice EX3-02 attacks.

**Sinks (where the rows live).** `lazyHash map[string][]Row` /
`lazyIntHash map[int64][]Row` (decls around `operators_join_agg.go:53-71`), presized at
`:775`/`:772` via `presizeLazyHash` (`:743-776`); NULL-key rows under
RIGHT/FULL in `fillNullBuild []Row` (`:261`, appended at
`join_outer_fill.go:120`); composite keys filed at
`join_composite_key.go:241`. All three sinks share the join lifetime
(§3.1).

**Oracle contrast.** PG's `dense_alloc` (`postgres/src/backend/executor/
nodeHash.c:2896`, via 10 §6) bump-allocates tuples contiguously into
`HASH_CHUNK_SIZE` arenas of `batchCxt`: one `palloc` per chunk, oversized
tuples in dedicated chunks (`HASH_CHUNK_THRESHOLD`). goopg has no chunk
layer — every build row is individually `make`d (slice) plus individually
`make`d per variable-width payload (bytes).

## 2. Dense-chunk layout proposal

### 2.1 What gets packed

Two strata, matching PG's tuple-bytes distinction (PG packs whole
`MinimalTuple`s; goopg's Datum/PRT split forces two strata):

- **Stratum D (Datum cells):** the `Row` backing arrays — `w × 48 B` Datum
  values per row, 8-byte aligned. Replaces census item (1): one chunk
  allocation covers `chunkSize / (w*48)` rows instead of one allocation per
  row.
- **Stratum B (bytes):** the variable-width payloads currently `make`d per
  Datum (`datum.go:459`) — string/bytes bodies and big-numeric
  sign+magnitude bodies. Replaces census items (2)+(3): payloads are
  appended to a byte chunk and referenced by the existing
  `(offset<<32|length)` + `ArenaID` encoding (`datum.go:407-422`), so
  `StringValue`/`BytesValue`/`NumericBigValue` (`:207-241`, `:350-368`)
  keep working unchanged.

A stored build row is then a `Row` slice header whose backing Datum array
lives in stratum D, with `ArenaID≠0` Datums pointing into stratum B. Both
strata are join-bounded (§3.1).

### 2.2 The `dense_alloc` analogue

A small build-local bumper (new file, e.g. `internal/executor/
dense_build.go` — name tentative, Cut 1 may reuse `mmgr` directly instead):

```
type denseBuildChunks struct {
    cells  *mmgr.Context  // stratum D: Datum cells via AllocAligned(w*48, 8)
    bytes  *mmgr.Context  // stratum B: payload bytes via AllocBytes/AllocString
}
```

Key decision — **both strata ARE `mmgr.Context`s** (`mctx.go:350-421` for
bump allocation, `AllocBytes` at `:459-461`):
`Alloc`/`AllocAligned` already implement bump-allocate-with-growth
(`:359-372`, `:391-421`), `Reset` (`:267`) gives whole-chunk release, and
the `(offset,length)` addressing of `AllocBytes` (`:459-461`) matches the
Datum arena encoding exactly. No new allocator is written; the new code is
a lifetime wrapper plus a row-packing helper. Chunk size: start at 64 KB
per stratum (same order as PG's `HASH_CHUNK_SIZE` = 32 KB; exact value is a
Cut-2 tuning knob, not a design axiom).

- **Alignment:** Datum is 48 B; the only GC-traced field is `Buf`
  (`datum.go:171-184`). mmgr chunks are `make([]byte,…)` slabs
  (`mctx.go:193-210`), i.e. GC-noscan spans, and `AllocSlice`'s contract
  is explicit (`mctx.go:507-514`: "T MUST be pointer-free… any Go
  pointer stored in a T placed here… is invisible to the mark phase").
  A nil `Buf` is safe; a non-nil one stored in a chunk keeps its target
  alive invisibly — the label/bytes can be collected while the dense
  row still references them. The "pointer-free when arena-backed"
  premise holds only for `ArenaID≠0` datums (F1). Consequence (Cut-2
  blocker): Cut 2 keeps `Buf`-carrying rows FULLY heap-backed (Row
  header and cells on the existing per-row path), or pins the `Buf`
  targets with an explicit GC-visible root for the join lifetime —
  decided in Cut 2, not here. §3.4's carve-out is therefore structural,
  not an optimisation choice. Stratum D uses `AllocAligned(w*48, 8)`
  (`mctx.go:385-421`; `AllocBytes` itself at `:459-461`). Stratum B
  needs no alignment (byte payloads; `allocBytes` `:423-456` is
  append-only).
- **Oversized rows (PG `HASH_CHUNK_THRESHOLD` analogue):** a row with
  `w*48 > chunkSize/2`, or a single payload larger than half the byte
  chunk, gets a dedicated `mmgr.Context` (same rule PG uses: oversized
  tuples get their own chunk so they cannot fragment the arena). The row
  still dies with the join (§3.1) — dedication is about fragmentation, not
  lifetime.
- **Threshold constant:** `denseChunkSize = 64<<10`,
  `denseOversizeThreshold = denseChunkSize/2`. Chosen so a TPC-H narrowed
  build row (w=7 → 336 B Datum cells) packs ~190 rows/chunk; wide
  pre-narrowing rows still pack dozens. The constant is named, tested, and
  tunable without touching call sites.

### 2.3 Arena interplay

| arena | lifetime | dense-build relationship |
|---|---|---|
| producer page arena (scan decode) | reset at `curBlock++` (`operators_storage.go:2092` discussion, `advanceBlock` at `:2346-2355` with `curBlock++` at `:2354`; C13) | **source only.** Build rows must never alias it (M0097-0058 class). Dense stratum B *copies out of it* (replacing `MaterializeArena`'s `make`), exactly as today. |
| `mmgr.Perm()` | process lifetime | **big-numeric moves OFF it.** Today's `MaterializeArena` big-numeric lane (`datum.go:444`) leaks per row into Perm. Dense build routes big-numeric bodies into stratum B (join-bounded) instead — a leak fix folded into Cut 1, gated by the same values arm. Non-build paths keep Perm. |
| dense strata (new, per-joinOp) | join build → last sweep/batch | **the retention store.** Created at build start (next to `presizeLazyHash`), dropped (`Reset` or deref) when the hash table is dropped (`o.lazyHash = nil` at `:1796`; `fillNullBuild = nil` at `:554`/`:1807`). Never reset mid-join. **Shared-build rule (F2):** `captureSharedBuild` (`parallel_hash_build.go:67-80`) publishes the map headers — which would reference dense chunks — to *other* joinOp instances (`applySharedBuild`, `:87-97`) that probe them after the builder is gone, and the prebuild throwaway tree's operators are never `Close`d on the shared path (`:160-198`). So "drop at Close" is either a use-after-release or a leak. Decision: strata are parented to the **statement context** (F5) and released explicitly at joinOp Close in the serial case; in the shared case ownership transfers to the `sharedHashBuild` (released at statement end). `Context.Reset` cascades to children (`mctx.go:267-278`), so Cut 3's teardown test asserts no mid-join `Reset` can reach the strata (generation check via `Generation()`, `mctx.go:261`). `releaseID` recycles ContextIDs (`mctx.go:167-174`) — a dense row outliving `Release` would resolve `(offset,length)` against a *different* context's chunks (F6: silent wrong bytes, not nil). This is why teardown discipline is load-bearing correctness, and why Cut 3's regression test covers the shared-build lifetime, not just serial Close. |
| per-tuple `ecxt`-analogues | per `Next()` | untouched — dense rows are written once at build time, never through per-tuple scratch. |

Because stratum B is an `mmgr.Context` with a stable `ContextID`, arena-
backed Datums in dense rows are indistinguishable from today's arena-backed
Datums to every reader (`StringValue`, `BytesValue`, `NumericBigValue`,
spill `encodeDatum`). This is what makes Cut 1 (§5) small: readers do not
change.

### 2.4 Pool interplay

Dense build rows **bypass `rowPool` entirely** (no `acquireRow`, no
`releaseRow`): chunk memory must never be `Put` back (pool `Put` boxes the
header + retains the backing — `row_pool.go:62-73`; `Put`ting a chunk view
would pin the whole chunk per row). Consequences:

- Live-heap accounting moves from N slice headers to (#chunks) headers +
  chunk bodies — strictly fewer objects, better locality (PG's "sequential
  bucket-chain memory", 10 §6).
- `releaseRow` is never called on a dense row; a defensive guard (Cut 2:
  debug assertion or width/cap check) must ensure no existing
  `releaseRow(denseRow)` path is introduced by accident. Today no build-row
  site calls `releaseRow` (retained rows correctly never return — EX2-03),
  so this is a "keep it that way" invariant, not a migration.
- `rowPool` itself is untouched (probe-side, scan, and non-hash paths keep
  it).

## 3. Hazards

1. **Row lifetime = end of join (not end of build).** Build rows are read
   through the whole probe phase (`lazyMatches` borrows bucket slices,
   `operators_join_agg.go:133`; `nextLazy` `:1412+`; composite lookup
   `join_composite_key.go:240` returns `o.lazyHash[key]` views), the
   RIGHT/FULL sweep (`fillSweepNext` snapshots bucket slices,
   `join_outer_fill.go:142-189`; `fillNullBuild` drained at `:189-192`),
   and — when batching is active — batch re-reads (EX3-03 territory, but
   the rows must survive until it runs). So: dense strata die at hash-table
   teardown (`:1796` + fill-null nil-outs), never at build-loop EOF.
   Falsifier: a RIGHT/FULL join whose sweep reads a zeroed chunk ⇒ Cut
   gated on TPC-H Q13/Q22-class outer shapes + `owned_build_poison_test.go`
   pattern (EX1-04 Cut 1 precedent).
2. **Probe-phase aliasing (read-only contract).** Probe composes output via
   `VirtualSlot` over the build row + probe row
   (`operators_join_agg.go:154-169`, M0071-0014 Stage D-2) — zero-copy read.
   Dense rows must therefore be **immutable after filing**: no in-place
   Datum patching, no reuse of a filed cell range for the next row. The
   bumper only ever advances; filed ranges are never written again. The
   existing `concatRows` fallback (`:4192-4197`) allocates fresh output, so
   it cannot alias chunk memory either way.
3. **Arena `Reset` discipline.** Two failure modes: (a) resetting a dense
   stratum mid-join (use-after-reset — the 10 §13 classic; EX2 risk
   register) — prevented by exposing no `Reset` on the wrapper until
   teardown; (b) producer-arena reset invalidating a row that was filed
   WITHOUT copying (the M0097-0058 class `ownedBuildRow` exists to close:
   `:924-938` comment). Dense filing keeps the copy — it only changes the
   copy's destination (`make` → chunk bump). The `rowHasArena`
   (`datum.go:471-483`) gate stays: non-arena rows still need the O(width)
   struct copy into stratum D (they alias the producer's reused slot, EX2-01
   C8 "scoped" note — the SOURCE aliases, so a pure transfer is unsafe and
   the copy remains).
4. **Variable-width Datums.** Empty strings (`length==0` → `Datum{Kind}`
   zero, `newStringArenaDatum` never called with len 0 — `NewStringDatum`
   `:392-397` returns bare kind): encode as today, no bytes consumed.
   `KindToastPointer` (12-byte `Buf`, `:630-633`): scans detoast before the
   build loop, but a defensive arm keeps `Buf`-carrying Datums on the
   per-row owned path (never fabricate an `(offset,length)` for a `Buf`
   payload — the `ArenaID==0 + Buf≠nil` shape must survive chunking
   bit-for-bit). `KindEnum` (`Buf` = label, `:725-730`): same rule — Buf-
   backed Datums NEVER enter stratum D (F1 GC-noscan: whole rows stay
   heap-backed, §2.2). Only `ArenaID≠0` payloads are re-homed into
   stratum B. Cut 2's pack helper carries a debug assertion for
   representation drift (F7): `if d.ArenaID != 0 { assert d.Buf == nil }`
   — any Datum with both `ArenaID≠0` and non-nil `Buf` would silently
   keep the `Buf` alias while readers prefer the arena.
5. **Release discipline / pool poisoning.** See §2.4. Additionally:
   `demoteIntHash` (`:1184-1197`) moves `Row` headers between maps — with
   dense rows the headers are chunk views; moving the header is safe (no
   payload copy), but the code must move the header value, never
   `append`-copy Datums out of it. `fillNullBuild` rows are dense rows too
   (same wrapper) — one wrapper instance per joinOp covers all three sinks.
6. **Batch-spill interplay (EX3-03 boundary).** `batches.insertBuildRow`
   (`:843-849`) receives the already-owned row. Dense rows stay valid for
   the spill encoder (`encodeDatum` reads via accessors — chunk-backed
   reads are identical), but EX3-02 does NOT change what spills or when;
   the spill path keeps receiving `Row`s and stays byte-identical. If
   EX3-03 later re-reads batch files, it re-decodes into its own buffers —
   no chunk reference crosses the file boundary.
7. **Parallel build (G4) — corrected model (F4).** The cooperative build
   does NOT run per-worker maps: producers only `transferRowForQueue(slot)`
   at `parallel_hash_build.go:480`, and the **leader runs the single serial
   build loop** (`:515-536`) owning the one hash map. There are no
   per-worker maps to merge. Consequence: the dense bumper is
   single-threaded *by construction* on this path — no merge logic needed.
   What parallel DOES need: shared-*probe* of dense rows by workers
   afterwards. Cross-worker read safety comes from phase separation
   (prebuild completes before workers start, `:125-198`), NOT from
   locking — the `mmgr` shared-context lock (`mctx.go:477-480`) covers
   only `isShared()` contexts (i.e. Perm, `:128`); ordinary contexts are
   explicitly single-owner (`mctx.go:63-65`). And the leader-side rows
   arrive via `transferRowForQueue` (arena-free `VirtualSlot` rows elide
   the copy — EX2-02c), then file into dense chunks in the leader loop.
   Serial first; the shared-probe + shared-build ownership (F2) half is a
   separate cut with the EX-P6 serial control arm.

## 4. What explicitly stays per-row and why

| per-row cost kept | why it stays |
|---|---|
| Map-key strings (`datumKey`, `:1166`; composite `execKeyBuf` copy, `join_composite_key.go:240`) | Go map keys must be stable strings; a chunk-backed string used as a key copies on insert anyway. Key encoding is EX4-02's item, not this one's. |
| Bucket `append` growth (`:1158`, `:1167`, `:241`) | Amortised O(1); pre-sizing (`presizeLazyHash` `:743-776`) already bounds it. Repacking buckets into chunks would couple EX3-02 to map geometry — one variable per commit (EX-P3). |
| NULL-key `fillNullBuild` slice headers (`join_outer_fill.go:120`) | One pointer per row in a rare lane (only under RIGHT/FULL, `fillBuildSide` gate `:117`); chunking the list buys nothing. The ROWS it points at are dense — that is the win. |
| CTID sidecar (`drainRowsCtxCTID` `:4182-4185`) | FOR UPDATE path only (`buildHashRightWithCTID` `:948+`), small result sets by construction; comes from `scanLeaf.currentTID()`, not the row buffer (EX2-01 C10). |
| `KindToastPointer`/`KindEnum` `Buf` payloads (§3.4) | Non-arena lifetimes (page-pin / catalog); re-homing them into a join-bounded chunk would SHORTEN (enum) or mis-scope (toast) their lifetime — and chunk memory is GC-noscan (F1), so `Buf` pointers would be invisible to the mark phase. Whole-row heap path, structurally. |
| Probe-side `VirtualSlot` composition + `concatRows` fallback | Read path, not build path; M0071-0014 already minimised it. |

## 5. Per-commit cut order (smallest safe first)

One variable per commit (EX-P3). Each cut carries alloc+timing+values+pin;
any cut may be declined by its own gate without invalidating the others.

- **Cut 0 — measure-only census (no behaviour change).** Instrument the
  build loop on the Q9-class shape: per-build counts of `ownedBuildRow`
  lane taken (arena vs `make`), `MaterializeArena` bytes/row, bucket-append
  allocs. Closes the "predicted vs actual" gap in §1 before any behaviour
  moves. Artifact: `analysis/planner-refactor-take3/ex302-census-<date>/
  README.md`. (Precedent: EX2-03 measure-only close-out.)
- **Cut 1 — stratum B only (variable-width payloads into a build arena).**
  Smallest behaviour change: a NEW build-only helper (e.g.
  `materializeInto(ctx, …)`, used solely by the build-loop retention
  call) routes payloads into `buildBytes.AllocBytes` (one `mmgr.Context`
  per joinOp, parented to the statement context per F5, dropped at
  teardown per F2); Row headers STILL per-row (`make`/`acquireRow`
  unchanged). `MaterializeArena`/`cloneRowOwned` semantics stay
  BIT-IDENTICAL (F3): `MaterializeArena` (`datum.go:433-462`) is a
  context-free method shared by `cloneRowOwned` (`:493-503`) serving
  `drainRowsCtx` (`:4123`), `Materialize` (`slot.go:181-189`), and
  `transferRowForQueue`/`MaterializeForTransfer`
  (`parallel_runtime.go:42-80`) — retargeting it in place would change
  the cross-goroutine contract `AssertTransferable`
  (`parallel_runtime.go:99-110`) depends on and the big-numeric→`Perm`
  lane other paths rely on. Readers unchanged (same `ArenaID`
  encoding). Includes the big-numeric-off-Perm move for BUILD rows only
  (§2.3; a strict lifetime shortening, process → join — safe exactly
  because build rows never cross a worker queue: producers send
  pre-ownership rows at `parallel_hash_build.go:480` and sharing at
  `:67-97` is intra-statement read-only). Poison tests: arena-reset
  survival (M0097-0058 replay), Perm-independence (no `Perm` growth on
  Q8-shape), RIGHT/FULL sweep over re-homed strings, plus a pin that
  `cloneRowOwned` output still satisfies `AssertTransferable`.
- **Cut 2 — stratum D (Datum cells into chunks).** `ownedBuildRow`'s struct
  copy lands in `buildCells.AllocAligned(w*48, 8)`; the returned `Row` is a
  view over the chunk. BLOCKED on F1: `Buf`-carrying rows stay fully
  heap-backed (whole row, §2.2/F1 rule — no struct-copy of `Buf` Datums
  into noscan chunks), with the F7 pack assertion
  (`ArenaID≠0 ⟹ Buf==nil`) guarding representation drift. `rowHasArena`
  gate preserved (non-arena rows still copied — source aliases the
  producer slot, EX2-01 C8). Guard against `releaseRow` on dense rows.
  Poison tests: probe-after-producer-reset, `demoteIntHash` mid-build
  re-key, composite-lane filing.
- **Cut 3 — oversize-dedicated chunks + teardown discipline.** Threshold
  rule (§2.2) + whole-chunk drop at `:1796`/`:554`/`:1807` with a
  regression test that peak heap after Close ≈ pre-build (no chunk leak,
  no Perm growth) — covering the SERIAL Close AND the prebuild-shared
  lifetime (F2/F6: strata parented to the statement context, ownership
  transferred to `sharedHashBuild`; ContextID-ABA makes this correctness,
  not hygiene). The no-mid-join-`Reset` rule is asserted via
  `Generation()` (F5). Parallel shared-probe (G4, §3.7-corrected) ONLY if
  serial cuts are green — else ledger-defer with resume point.

Deliberately deferred out of EX3-02: key-string interning, bucket repacking,
spill-format changes (all EX3-03/EX4-02 candidates — name them, do not smuggle
them).

## 6. Falsifiable gate

**Shapes (operators, never queries — EX-P2; query names are witnesses).**
Primary: Q9-class multi-join hash cascade (~6 M-row builds, string-heavy —
prices strata B+D together). Secondary: Q21-class anti-join lineitem build
(int-lane, prices stratum D alone); Q13/Q22-class RIGHT/FULL outer shapes
(prices `fillNullBuild` + sweep survival, §3.1). Excluded: Q7/Q13 spill
shapes as TIMING witnesses (spill territory — EX3-01/03; values+pin only
there), TOAST-heavy TPC-DS shapes (EX1-03-dropped).

**Arms (EX-P4).** Alloc arm PRIMARY: build-path allocs/row and bytes/row
from the Cut-0 census harness (before/after per cut); object + byte arms
must both be neutral-or-better, witness-shape alloc totals strictly decrease
(E3). Timing arm: per-query attribution + TOTAL, both suites, fresh server
per arm, ±17% band, no query slower than 1.2× its baseline (E1). Either arm
regressing fails the cut.

**Correctness.** Values `-digest` + `-diff` on TPC-H SF=1 (24/24) and TPC-DS
SF0.5 (PASS=95), never counts alone (R8; P4-01b precedent). Plan-shape pin
`changed=0` both suites (EX-P5). EX0-05 batch counters reported alongside
time (batch geometry must not move — EX3-03 owns it). Unit pins: poison
tests per cut (§5) + existing `owned_build_poison_test.go`,
`join_slot_chain_test.go: TestProbeSeamZeroAllocs`,
`datum_arena_test.go` M0073/M0092 tests green; executor suite green; never
`-count=1` in a gate; never `--no-verify` (ground rules 5–6).

**Falsifiers (any one fails the cut):** alloc/row not down on the Q9-class
build; timing beyond the 1.2× band on any witness; any values-diff mismatch;
pin `changed≠0`; batch counters moved; chunk/Perm growth across Close
(Cut 3).

## 7. Open risks

1. `mmgr.Context` chunk granularity vs 48 B cells — `growChunk` sizing
   (`mctx.go:330-347`) is tuned for byte payloads; cell traffic may grow
   chunks faster than the 64 KB design point assumes. Cut-0 census sizes
   it; the constant is tunable.
2. Shared-build (parallel) merge cost (§3.7-corrected) — the cooperative
   model needs no per-worker strata (leader packs), but shared-probe of
   dense rows + shared-build ownership transfer (F2) remain a separate
   cut; if the serial cuts land but the shared half shows a residual, it
   is ledger-deferred, not forced.
3. `Buf`-carrying Datums (§3.4) cap the stratum-B win on enum/toast-heavy
   builds — bounded by corpus (TPC-DS has no TOAST witness at SF0.5;
   EX1-03-dropped), stated, not solved.
4. Sweep-tail/GC interaction (per CLAUDE.md: post-timeout servers sit at
   GOMEMLIMIT with GOGC=off) — dense chunks concentrate live heap; gate
   hygiene (fresh server per arm, constant server age) is load-bearing for
   timing reads.

(End of file)
