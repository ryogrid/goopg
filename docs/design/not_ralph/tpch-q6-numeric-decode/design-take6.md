# Take 6 — the three allocations per scanned tuple

**Status:** implemented and measured — results in [benchmark-results-take6.md](benchmark-results-take6.md)
**Date:** 2026-08-30
**Branch:** `perf-opt-take4`
**Baseline:** `1cfcdede9` (take 5)
**Oracle:** PostgreSQL 18.3, TPC-H SF=1, port 65432
**Raw artefacts:** `tmp/take4/runs/q6-t6base/`

---

## 1. What the profile says

[benchmark-results-take5.md §6](benchmark-results-take5.md) named
`evalExprSlot` and `storage.PageGetHeapTuple`/`ParseHeapTuple` as what remained.
Re-profiled at `1cfcdede9` (30 s window, parallel Q6):

| | CPU (cum) | allocations (**flat**) |
|---|---:|---:|
| `evalExprSlot` | 31.88 % | — |
| `storage.PageGetHeapTuple` | 20.39 % | **32.59 %** |
| `storage.ParseHeapTuple` | 7.44 % | **32.45 %** |
| `executor.evalExpr` | — | **32.35 %** |
| everything else | — | 2.6 % |

**The allocation column is flat, deliberately** — `PageGetHeapTuple`'s
allocation *cum* is 65.05 % because it contains `ParseHeapTuple`. Summing the
flat figures is what makes 97.4 % meaningful; summing cum would double-count.
(take-5 §6 quoted the pair as 57.9 %, which was the cum reading of the same
two — the numbers agree, the denominators differ.)

**Three call sites account for 97.4 % of every allocation the query makes**, and
each is almost exactly one allocation per scanned tuple (122.2 M / 121.7 M /
121.3 M in the same window). None of the three is doing useful work: two are
redundant copies and one is boxing a slice into an interface.

`runtime.memmove` is 7.35 % of CPU overall, but **only 0.36 points of that is
removable here**: 6.80 points belong to `PageGetHeapTuple`'s copy of the tuple
out of the page, which §3.2 keeps (it turns the allocating `append` into a
`copy` into a reused buffer — the same memmove). The removable CPU is
`growslice`, not `memmove`; see §4.

---

## 2. Root cause

### 2.1 The heap tuple is copied three times

`internal/storage/heap.go`:

```go
func PageGetHeapTuple(p Page, slot uint16) (HeapTuple, error) {
    …
    raw := append([]byte(nil), p[off:off+ln]...)   // copy 1 — the whole tuple
    return ParseHeapTuple(raw)
}

func ParseHeapTuple(raw []byte) (HeapTuple, error) {
    t, err := parseHeapTupleAlias(raw)             // Data/Bitmap alias raw
    …
    t.Data = append([]byte(nil), t.Data...)        // copy 2 — the data portion
    if len(t.Bitmap) > 0 {                         // copy 3 — the null bitmap
        t.Bitmap = append([]byte(nil), t.Bitmap...)
    }
    return t, nil
}
```

`ParseHeapTuple` is a public entry point whose contract is "the returned tuple
owns its memory", so copies 2 and 3 are right *for a caller that hands it a
page alias*. They are **pure waste for `PageGetHeapTuple`**, which has already
made `raw` a private, freshly-allocated copy that nothing else will ever touch.
Copying a private buffer into another private buffer buys nothing.

(Q6's `lineitem` has no NULLs, so `parseHeapTupleAlias` leaves `Bitmap` **nil**
and the explicit `len(t.Bitmap) > 0` guard skips copy 3 — which is why the
measurement shows one allocation per tuple in `ParseHeapTuple`, not two.)

### 2.2 `evalExpr` boxes the row into an interface on every call

```go
func evalExpr(e optimizer.Expr, row Row, ctx *Context) (Datum, error) {
    var slot SlotView
    if row != nil {
        slot = rowSlotView(row)      // ← convTslice: a slice does not fit an
    }                                //   interface word, so this allocates
    return evalExprSlot(e, slot, ctx)
}
```

`rowSlotView` is `type rowSlotView Row` — a slice. Converting it to the
`SlotView` interface allocates a 3-word slice header on the heap every call
(`runtime.convTslice`, 3.94 s in the take-5 profile).

All 121.3 M of these come from **one** call site: the scan prefilter added in
take 5 calls `evalExpr(pred, o.scanRow, o.ctx)` once per scanned row. The
underlying slice is `o.scanRow`, whose identity does not change from row to
row — so the boxing is not merely wasteful, it is recomputing a constant.

### 2.3 What is *not* worth attacking

`evalExprSlot` is 31.88 % of CPU **cumulative** but allocates nothing
measurable. Its *flat* cost — the interpreter's `switch` over `optimizer.Expr`
kinds — is **14.19 %**; the rest is the arithmetic underneath it
(`evalBinary` 14.27 %, `compareDatum` 5.17 %, `addTimeInterval` 4.04 %,
`evalTypedStringLit` 1.96 %).

That distinction matters for what could ever be won here. Compiling expressions
(PG's `ExecReadyExpr`/JIT) attacks the ~14 % dispatch half, not the arithmetic,
so the prize is smaller than the 31.88 % headline suggests. It is also a large,
separate piece of work with its own risk surface. **This design does not touch
it**, and §6 says so rather than pretending otherwise. What §2.2 removes is the
*allocation* in its caller, not the evaluation itself.

---

## 3. Design

Three changes, in increasing order of risk.

### 3.1 One copy, not three (no semantic change)

`PageGetHeapTuple` calls `parseHeapTupleAlias(raw)` instead of
`ParseHeapTuple(raw)`. `raw` is already private, so the returned tuple owns its
memory exactly as before.

`ParseHeapTuple` itself is **unchanged** — it stays the copying entry point for
its other callers, whose input may well be a page alias. Only the one caller
that provably does not need the copies stops paying for them.

One consequence worth stating rather than calling it "no semantic change":
`Data` and `Bitmap` now share one backing array, so `cap(t.Bitmap)` runs to the
end of the buffer and an `append(t.Bitmap, …)` would clobber `Data`. Nothing in
`internal/` appends to either field, so it is unobserved — but it is a real
narrowing of what a future caller may do.

Maintainability: three lines, and the reason is a one-sentence comment. No new
concept.

### 3.2 Reuse the tuple buffer across iterations

The remaining copy still allocates a fresh `[]byte` per tuple. Add

```go
// PageGetHeapTupleInto is PageGetHeapTuple with a caller-supplied scratch
// buffer. The returned tuple's Data/Bitmap alias buf, so they are valid until
// the next call that reuses it.
func PageGetHeapTupleInto(p Page, slot uint16, buf []byte) (HeapTuple, []byte, error)
```

and let `seqScanOp` own one buffer for the life of the scan. Allocation per
tuple drops to zero once the buffer reaches the largest tuple on the relation.

**This is the change that needs an argument**, because it narrows a lifetime:
today every tuple's bytes live until the GC collects them, so a `Datum` that
aliased them would still be readable. With a reused buffer, such a `Datum`
would be corrupted by the *next* tuple.

The invariant it depends on: **no `Datum` produced by the heap decoder aliases
the tuple bytes.** That holds today, and by construction rather than by luck —
every varlena arm of `decodePhysicalPGValueMctxStyled` copies:

| arm | how it copies |
|---|---|
| `text` / `varchar` / `bpchar` / `unknown` / `name` | `sctx.AllocBytes(payload)` into the arena, or `NewStringDatum(string(payload))` |
| `bytea` | `NewBytesDatum(append([]byte(nil), payload...))` |
| `numeric` | `int64` mantissa + scale, or `string(payload)` |
| `uuid`, `pg_lsn`, `float4`/`float8` | **produce strings**, via `AllocBytes` / `NewStringDatum` — they copy |
| `int2/4/8`, `bool`, dates, timestamps | decoded into scalar fields; nothing to alias |
| toast pointer | `make([]byte, 12)` + `copy(…, data[1:13])` |
| `pg_node_tree` VARTAG_ONDISK | 18 bytes explicitly copied |
| arrays (`decodeArrayValuePGStyled`) | rendered through a `strings.Builder`, no `unsafe` |
| `aclitem[]`, the `default` varlena arm | route through the same copying helpers |
| `MissingValue` (fast default) | a catalog-owned Datum; never references the tuple |

Note the reason is **"every arm copies"**, not "fixed-width arms decode into
scalars". That weaker phrasing was in an earlier draft and is wrong: `uuid`,
`pg_lsn`, `float4`/`float8` and `name` are all fixed-width *and* produce
strings. They are safe because they copy, which is the only property that
matters.

`decodePhysicalPGVarlena` itself **does** return an alias into `data`
(`data[1:total]` / `data[4:total]`) — every one of its call sites copies before
building a Datum. That is the single place a future edit is most likely to go
wrong, which is why the test below scribbles the buffer rather than trusting
this table.

An audit is not a guarantee that survives future edits, so §5 backs it with a
test that **scribbles over the source buffer after decoding** and asserts every
`Datum` is unchanged. That converts the invariant into something a future arm
that starts aliasing will trip over immediately.

`seqScanOp` is additionally the safest possible first caller: `Next` has exactly
one row-yielding return, and everything derived from the tuple is consumed
before the loop advances. `HeapTuple.Header` is a value struct (all scalars), so
the header copy is unaffected.

**`cloneRowOwned` is NOT part of this safety chain, and it is worth being blunt
about that.** It calls `Datum.MaterializeArena`, which returns any Datum with
`ArenaID == 0` *unchanged* — `Buf` included. A Datum pointing into the tuple
bytes would sail straight through the clone and out to the consumer. So the
non-aliasing invariant is the *only* thing standing between §3.2 and silent
corruption; there is no second line of defence. Hence the test.

Two further precisions, since "retains nothing" is too strong: `o.scanRow`
keeps the previous tuple's Datums in `[MaxCols:]` between iterations, and
`o.gistScratch` is never cleared. Both are benign — nothing reads them — but
the invariant that actually holds is "nothing derived from the tuple bytes
escapes the iteration", not "nothing is retained".

Note this is strictly **safer than the existing `PageGetHeapTupleNoCopy`**
(used by `operators_index.go`), which aliases the shared page itself and is
only valid while the content RLock is held. The scratch buffer survives the
`RUnlock`.

### 3.3 Hoist the slot boxing out of the per-row path

`seqScanOp` caches the boxed `SlotView` and `evalPrefilter` calls
`evalExprSlot` directly:

```go
if o.scanSlot == nil {
    o.scanSlot = rowSlotView(o.scanRow)
}
return evalExprSlot(o.prefilter.pred, o.scanSlot, o.ctx)
```

with `o.scanSlot = nil` at the **single site that reassigns `o.scanRow`**.

That placement is the whole correctness argument, and an earlier draft got it
wrong by claiming `o.scanRow` "is reallocated at most once per `Open`". It is
not: it is released to a `sync.Pool` by `releaseScanState` (Close) and by
`rewind` (**rescan** — e.g. a nested-loop inner side), and re-acquired from
`acquireRow`, which may hand back a *different* pooled backing array. Invalidating
only in `Open` would leave `scanSlot` boxing a stale row after a rescan, and
because the prefilter's `false` is authoritative that would **silently drop
qualifying rows** — a wrong answer, not a crash.

Nilling at the assignment site is both correct and simpler than comparing
backing-array addresses (`&o.scanRow[0]` also panics on a zero-length row, and
retaining the old row to compare against would pin memory the pool has
reclaimed).

One residual, stated rather than glossed: a cached interface freezes
`{ptr,len,cap}`, so a *reslice* of `o.scanRow` would not be noticed. Not
reachable today — `o.scanRow` is only ever assigned wholesale from
`acquireRow(len(o.cols))`, and the resjunk-ctid `append` targets the cloned
row, not the scan row.

`evalExpr` is untouched for its 138 other call sites in `internal/`; only this
one hot site stops re-boxing a constant. **The allocation class is not
eliminated repo-wide** — the same per-row boxing is still paid by
`joinOp.evalHashKey`, `joinOp.joinPredicateMatch`, and
`sortOp.lessRows`→`evalSortKeyValue` (the last being O(n log n), not O(n)).
Those are out of scope here because Q6 has no join and no sort, but they are
the obvious next callers.

Maintainability: one function call becomes one function call plus a cached
field with a single invalidation point.

## 4. Expected effect

| change | allocations removed (flat) | CPU removed |
|---|---:|---|
| 3.1 one copy not three | 32.45 % | `ParseHeapTuple`'s `growslice` 3.07 % + `memmove` 0.36 % |
| 3.2 buffer reuse | 32.59 % | `PageGetHeapTuple`'s `growslice` 3.80 % |
| 3.3 slot boxing | 32.35 % | `convTslice`→`mallocgc` 2.55 % |
| knock-on | — | `runtime.bgsweep` up to 2.32 % |
| **total** | **≈ 97 %** | **≈ 12.1 %** |

Allocation count per query should fall from 18.8 M toward ~0.5–1 M (the 2.6 %
that is none of the three).

**Wall clock: ≈ 1.10–1.14×.** Amdahl on 12.1 % gives a ceiling of
`1/(1−0.121)` = 1.14×, so anything above that would have to come from second-order
effects. An earlier draft claimed 1.15–1.35×, which is above the ceiling and was
simply invented; the corrected range is what the profile permits.

Two CPU items are **structurally retained** and must not be counted:
`PageGetHeapTuple`'s 6.80 % `memmove` (the copy out of the page survives §3.2 —
it just stops allocating) and `ParseHeapTuple`'s 3.54 % flat, which is
`parseHeapTupleAlias`'s header decode.

This is deliberately a smaller claim than takes 4 and 5, which removed real
computation. Note the collector *is* doing more than the 0.013 %
`GCCPUFraction` suggests — `runtime.bgsweep` is 2.32 % of this profile, which
`GCCPUFraction` does not count — so removing 97 % of allocations buys back both
allocator and sweeper time.

## 5. Correctness and test plan

| risk | mitigation |
|---|---|
| A future decode arm starts aliasing the tuple bytes | `TestDecodedRowDoesNotAliasSourceBuffer` — decode a row of every storage class, overwrite the source buffer with `0xFF`, assert every `Datum` unchanged |
| `parseHeapTupleAlias` vs `ParseHeapTuple` divergence in `PageGetHeapTuple` | `TestPageGetHeapTupleOwnsItsMemory` — read a tuple, scribble the page, assert the tuple is unchanged (pins the *existing* contract, which §3.1 must not weaken) |
| The scratch buffer grows incorrectly for a larger tuple | `TestPageGetHeapTupleIntoGrowsBuffer` — feed ascending tuple sizes through one buffer and compare against `PageGetHeapTuple` |
| Stale `SlotView` after `o.scanRow` is reallocated | the cache keys on the backing array; covered by the existing scan tests plus `tpch-spotcheck` |
| Silent row-count regression | `scripts/tpch-spotcheck.sh` (Q12 = 2, Q13 = 34) |
| Concurrency (the buffer is per-operator, and parallel workers each build their own) | `go test -race ./internal/executor/` |

Gates: `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh`,
`scripts/tpch-spotcheck.sh`, `go test -race`, plus the full storage package
(`internal/storage`) since §3.1/§3.2 change a public storage entry point's
neighbourhood — the codec/storage practice card requires the broader suite, not
just the touched package.

Acceptance:

1. Q6 result bit-identical: `102513054.4896`.
2. All gates green.
3. Allocations per query materially down (target: ≥ 5× fewer).
4. No wall-clock regression; any improvement is a bonus, not the bar.

---

## 6. Out of scope, and why

- **`evalExprSlot` itself (31.88 % cum, 14.19 % flat).** Interpreted evaluation. The fix is
  expression compilation, which is a design of its own; doing it badly here
  would trade a large maintainability cost for an uncertain win. §2.3.
- **`strings.ToLower` on the type name per value (5.21 %).** Real, and
  independently fixable by resolving a per-column type code once in `Open`;
  left out to keep this round's blast radius to the allocation story.
- **`PageGetHeapTupleNoCopy` for the scan.** This deserves a real answer,
  because it would be *strictly faster* than §3.2: zero copies, so it wins
  §3.2's 32.59 % of allocations **plus** the 6.80 % `memmove` that §3.2
  retains — and `seqScanOp` already meets its "valid only under the content
  RLock" contract, since `cloneRowOwned` runs before the `RUnlock`.

  It is declined on lifetime coupling, not on the constraint. With `NoCopy` the
  tuple aliases the shared page, and `Next` re-acquires that page in **write**
  mode a few lines later to set the xmin hint bit — so the scan would hold
  aliases into a buffer it itself mutates, and the window in which "nothing may
  read `tuple.Data`" is bounded by code that is not obviously about tuple
  lifetime at all. The scratch buffer is private to the backend and stays valid
  across the `RUnlock`, so it degrades gracefully if that ordering is ever
  disturbed. Given the explicit constraint that maintainability must not
  regress, ~7 % is not worth making the scan's correctness depend on a lock
  ordering three screens away. Recorded here as a live option if that 7 % is
  later wanted.


---

## 7. Review record

Adversarial agent review, 2026-08-30, against the pre-implementation draft:
**7 major, 9 minor**, all fixed in place above. The review independently
audited every decode arm and **could not falsify §3.2's non-aliasing
invariant** — the design's central risk — but found the *reasoning* for it
wrong in two places and the CPU story materially overstated.

| # | finding | resolution |
|---|---|---|
| MAJOR-1 | §1's allocation column was labelled `(cum)`/`(within it)`; read that way, `32.59 + 32.45 + 32.35` double-counts and 97.4 % is meaningless. `PageGetHeapTuple`'s allocation cum is 65.05 %. | Column relabelled **flat**, with the flat-vs-cum reconciliation against take-5's 57.9 % spelled out. |
| MAJOR-2 | "`memmove` … the visible cost of exactly those copies" — 92.6 % of that memmove is `PageGetHeapTuple`'s copy out of the page, which §3.2 **keeps**. | Corrected: 0.36 of the 7.35 points is removable; the win is `growslice`, not `memmove`. |
| MAJOR-3 | The 1.15–1.35× estimate exceeds the Amdahl ceiling — removable CPU is ≈ 12.1 %, ceiling 1.14×. | Corrected to **1.10–1.14×** with the line-by-line budget, and the two structurally-retained items named. |
| MAJOR-4 | "`o.scanRow` is reallocated at most once per `Open`" is false — `releaseScanState` and `rewind` return it to a pool and `acquireRow` may hand back a different array. An implementer following the doc would drop qualifying rows after a rescan. | §3.3 rewritten around invalidating at the single reassignment site. The implementation was already correct; the doc was not. |
| MAJOR-5 | "Fixed-width arms cannot alias — they decode into scalars" is false for `uuid`, `pg_lsn`, `float4`/`float8`, `name`, all of which produce strings. | Audit table corrected and the reason restated as **"every arm copies"**, which is the property that actually holds. |
| MAJOR-6 | `cloneRowOwned` was implied to be a safety barrier; `MaterializeArena` passes `ArenaID == 0` Datums through unchanged, `Buf` included. | Stated bluntly: the invariant is the *only* defence, there is no second line. "Retains nothing" also softened — `o.scanRow[MaxCols:]` and `o.gistScratch` do persist. |
| MAJOR-7 | `PageGetHeapTupleNoCopy` was dismissed on a constraint `seqScanOp` already satisfies; it would win §3.2's allocations **plus** the 6.80 % memmove. | Accepted as correct. Still declined, but now on the real ground — the scan re-acquires the same page in **write** mode for the xmin hint bit, so `NoCopy` would couple correctness to a lock ordering three screens away. Recorded as a live option. |
| M1–M9 | `ParseHeapTuple`'s bitmap guard mis-quoted (and `Bitmap` is nil, not empty); toast pointer is 12 bytes not 18; audit table omitted `name`/`pg_node_tree`/arrays/`default`; §3.3's pseudo-code panicked on a zero-length row and referenced an undefined field; `evalExprSlot`'s 31.88 % is cum, flat is 14.19 %; `bgsweep` is 2.32 % so "0.013 % collector" understates by ~180×; "~200 callers" is 138; §3.1's shared backing array is a real narrowing; the boxing fix is Q6-scoped, not repo-wide. | All corrected in place. |

**Bonus finding — a real latent bug in the take-5 code, not in this design.**
The prefilter disarm block listed `typeACLColIdx` and `attrACLColIdx` but
omitted **`dbACLColIdx`**, even though `pg_database.datacl` gets the identical
post-clone `KindBytes → aclitemout` rewrite. A predicate on `datacl` would have
been prefiltered against the raw `_aclitem` blob while `filterOp` saw rendered
text — breaking the "can only remove rows the Filter would remove anyway"
guarantee. Fixed in `operators_storage.go` as part of this round. Exactly the
sibling-path failure mode the block exists to prevent.

Claims the review verified as **correct**, which is what the change rests on:
the triple copy and that copies 2+3 are redundant only because `raw` is
private; that `ParseHeapTuple` must keep them for its five other callers (all
of which pass a live page alias); that `parseHeapTupleAlias` is a safe
substitute because `Data`/`Bitmap` are the entire aliasing surface; **that no
aliasing decode arm exists**; that `seqScanOp` has exactly one row-yielding
return, unconditionally preceded by `cloneRowOwned`; that all 121.3 M
`evalExpr` allocations come from the prefilter (cum matches to the sample);
that `rowSlotView` is a slice type and `convTslice` heap-allocates; and every
cited percentage against `q6-t6base/cpu.pb.gz`.
