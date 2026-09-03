# REVIEW — findings and resolutions

Three reviewers, 2026-09-03, run against the first complete draft:

- **(a) PG-source accuracy** — verified 01 and 03's PostgreSQL claims against
  `./postgres/` (PG 18.3). ~60 citations sampled.
- **(b) goopg-source accuracy** — verified 02, 03 and 04's goopg claims against
  the tree. 60+ citations sampled.
- **(c) design and feasibility** — adversarial, against take3's recorded no-gos,
  the deferral ledger, and the repo's gate conventions.

Reviewer (c) returned **"do not start MD-01"** with three blockers. They are
resolved by changing the bundle's conclusion, not its wording. Everything below
is applied unless the disposition says otherwise.

---

## Blockers

### B1 — the licence claim was a misreading (reviewer c)

The draft claimed take3 `13-executor-target-design.md` §10 grants a re-proposal
path. §10 reads: *"`Datum` re-layout below 48 B **and JIT** are declined in §0.1
with reasons; re-proposing **either** requires new measurement, not new
argument."* "Either" ranges over a two-element list. §0.1's **first** clause,
*"a new row representation"*, has no stated re-proposal path — and
`PackedTuple`/`PackedSlot` is squarely within it.

Worse: §0.1's parenthetical shows take3 declined this *while holding the
measurement being offered* — *"(12 §12: narrowing without shrink still halved
batches; shrink without narrowing is not proposed)"* — and 12 §12 row 1 **is**
`FINDING-p401-alone-is-not-enough.md`.

**Resolved.** Claim withdrawn. README opens with a `## Status: NOT APPROVED TO
START` section; 04 §0.2 is retitled "Standing with take3 — no licence is
claimed"; `TODO.md` opens `## Status: BLOCKED`. The bundle now asks rather than
asserts.

### B2 / B3 — the premise was modelled, and the measured answer is different (reviewer c)

The FINDING states its own method: it *fed* `hashsize.Choose` hand-derived
column counts. No query ran, and the formula it evaluated is the one MD-04
proposes to replace. Its own caveat #3 asked for the collector's real answer
first — and take3 `09-verification-and-acceptance.md:48,190` records it:
**`Batches: 8 → 1`**, narrowed width **≈100, not 6**.

`.ralph/deferral_ledger.md:2036` (2026-09-03, *"measured at equal cardinality for
the first time"*) is the executed A/B: widths **1098/1642/2090/3164 B** vs
PostgreSQL's **23/32/54/81 B**, 97 MB vs 38 MB peak hash memory, **8 batches vs
1**, **63.8 s vs 6.2 s** — disposition *"This is P4-01's PathTarget/projection.
Resume there."*

**Resolved, and it demotes the bundle.** 04 §0.3 and README's "What this is
actually worth" now carry the measured pair and the arithmetic: 1098 B / 48 ≈ 22
columns; packing 22 TPC-H columns yields ~150–250 B, not 23 B. **Packing closes
~5× of a ~48× gap; narrowing closes the rest.** The bundle's justification is
restated as byte-parity and a single retention format — **not** closing the Q9
residual. 06 §3's witness reports against the measured pre-state.

### B5 — sequencing contradicts take3 13 §8.2 (reviewer c)

> §8.2: **EX1 before EX3's geometry.** … EX3-02/03/06 on pre-EX1 widths are
> **premature by rule.**

MD-04 re-derives `EntryBytes`, `bucketSize`, `RowSliceBytes`, `MapSlotBytes` and
`estimatedRowBytes` — that *is* the batching geometry. The draft quoted 12 §12's
result (128 → 64) and dropped its preservation rule (*"narrowing and footprint
move together"*), which is the half that binds it. Chained with §8.6 (P4-01
before EX1), the order is **P4-01 → EX1 → geometry → MD-04**.

**Resolved.** 04 §10 rewritten; README gains a Sequencing section; `TODO.md`
marks MD-04 onward BLOCKED. MD-01/02/03/03.5/1x carry no geometry and proceed.

### B7 — prior art omitted: this seam regressed twice (reviewer c)

`Operator.Next` returning `TupleSlot` was reverted twice — M0069-0001 Stage B/C
(`336550ce0`, `41dd7154b`) and the M0071-0005 re-land (`cf04bce20`, `5d6961d0d`).
Recorded causes: **Q1 +21 %, Q11 +65 %**, attributed to *the consumer-side
`slot.Row()` materialisation*; and **Q12 rows 2 → 0, Q13 rows 35 → 2** from
slot-buffer reuse aliasing.

`PackedSlot.Row()` is precisely a consumer-side full materialisation. And
`Q12=0 / Q13=2` is the signature this bundle uses as its own spot-check tripwire
(06 §2) — **the bundle inherited the symptom while omitting the cause.**

**Resolved.** New 04 §11 ("Prior art: what has been tried here, and how it
failed"), R-12 added to the register pointing at it.

---

## Major

### M10 — the `Datum` packed flip was attempted and reverted (reviewer c)

`docs/design/0050-0099/0074-0003` and `0075-0003` are **DEFERRED M0076** after an
attempt on 2026-05-10: the tight 5-query gate **passed** (Q12=2, Q13=35, Q21=381,
Q22=7, Q9=7), then the 21-query sweep showed seven queries returning **0 rows**.
Root cause: arenaRegistry slot reuse aliasing retained Datums.

**Resolved, and it changes a decision.** 04 §11.2 records it. More importantly:
D-5 option (a) — aliasing `Buf` into `PackedTuple.buf` — is *exactly* that
failure mode, and §7 had deferred it "with the GC arm as its gate". **The GC arm
is the wrong gate**; that bug passed a five-query gate. §7 now states
**D-5(a)'s gate is a full-sweep values diff on both suites.**

Also: 05 §1 presented column (A) as hypothetical. It is not — "declined" here
means "declined after a failure", which is stronger.

### M6 — a fifth hash-join retention lane, missing from the sibling set (reviewer c)

Ledger M0127: the composite multi-key lane *"does NOT batch (packed-byte maps, no
PG-style hashvalue) — multi-key hash join still **unbounded**"*. 04 §4.1 named
four maps and called them the set that must move together.

**Resolved.** 04 §11.3 and `TODO.md`'s MD-04 entry; it belongs in Tier A. In a
design whose central discipline is `pattern_sibling_paths_must_agree`, missing a
sibling in the inventory is the error the discipline exists to prevent.

### M2 — R-3 understates the encode cost (reviewer c)

04 §9.4 priced packing as "a memcpy per retained row". `encodeValuePGCtx` is
~617 lines: per column per row it does `strings.ToLower(t.Name)`, a ~31-arm
string switch, an inner `switch d.Kind`, and returns an `error` per value.
`colTypeInfo` exists to kill exactly that re-derivation — **on the decode side
only**. That is the cost profile that killed M0069 Stage B.

**Resolved** in R-3's text; MD-03.5 (below) exists to measure it before it is
committed to.

### M9 / M-est — ~70 is a declaration census, not a change surface (reviewer c)

Converting one field means rewriting every use: `lazyHash` 51 non-test reference
lines, `lazyIntHash` 28, `sortOp.rows` ~72. Tier A alone is 150+ reference lines.

**Resolved.** 05 §2.5 now says so explicitly and distinguishes *decisions* (~70)
from *edits* (§4's LOC bands). The "grep oversizes 10×" lesson is marked as not
applicable — it is about types behind a facade, and a `[]Row` field has none.

### M-gates — four holes in the per-commit floor (reviewer c)

1. **`CKMISMATCH` omitted.** The TPC-DS script reports value mismatches under a
   status *separate* from row-count mismatches — literally "the right number of
   wrong rows", which is 06 §1's failure mode by name. "PASS=95 MISMATCH=0"
   excluded it. **Fixed**: `CKMISMATCH=0` added, and the `ck=n/a` queries flagged
   as row-count-only.
2. **`make plan-gate` exits 0 on SKIP**, and its default `structural` mode strips
   `cost=/rows=/width=` — so a `hashsize` change that moves costs without moving
   shapes is invisible, while R-6 asks for a cost roll-up. There is also no
   TPC-DS plan pin. **Fixed**: stated in 06 §2 as work MD-01 must close.
3. **No alloc arm exists** — ~13 per-function microbenchmarks, no query-level
   `inuse_space` harness. **Fixed**: 05 §6's instrument table.
4. **The alloc arm is unfalsifiable by construction** — one `[]Datum` allocation
   per row becomes one `[]byte` allocation per row, so allocation *count* is
   unchanged, and the repo's standing finding is that allocator CPU tracks count.
   **Fixed**: 05 §6 replaces "allocs neutral" with retained *bytes* as the real
   check.

### M-stop — the stopping rule fires too late and cannot be evaluated (reviewer c)

Three of MD-04's four numbers are unmeasurable today, the fourth (allocs) is
unfalsifiable, and the ±17 % noise band swallows the timing row. And the rule
triggers only after 600–900 LOC is sunk.

**Resolved.** 05 §6 gains the instrument table, and **MD-03.5** is added: a
throwaway prototype that packs and unpacks the Q9 build side behind a flag with a
hardcoded descriptor, measures, and is deleted.

### M-alt — cheaper interventions not considered (reviewer c)

**Resolved.** New 05 §1.5 prices three against this bundle: deleting the
duplicate build map (~2× for **one commit**, and 04 §4.1 instead lists both maps
as things to *convert*), the probe-seam re-materialisation (~18 M pool
round-trips, ~2×2.3 GB of `Datum` traffic on a Q9-class query), and the repo's
own 24 B pointer-free `Datum` design (**2×**, partially landed) — which 04 §0.1
had dismissed by pricing only the weakest ~32 B variant.

Also recorded there: the whole premise lives in the `work_mem` 4 MB regime, and
at goopg's shipped 512 MB × 2 the FINDING's own table reads NBatch = 1 nearly
everywhere.

### M-risks — six risks missing (reviewer c)

**Resolved.** Added to 04 §9 as R-7…R-12:

| id | risk |
|---|---|
| R-7 | the descriptor has no owner past the operator — `CTERowCache` and cursor `Rows` outlive every `Open`, and a packed tuple without its descriptor is undecodable bytes |
| R-8 | the scratch arena's reset point is unspecified — the single most consequential unstated detail; either it accumulates and gives back the memory, or every reload pays churn |
| R-9 | **`attcacheoff` does not help this bundle's own witness** — PG's cache dies at the first NULL and first varlena, and HammerDB declares TPC-H integer keys `NUMERIC`, so it dies at column 0-1 on `orders`. 04 §6 claimed the opposite |
| R-10 | `Release()` and the width-keyed row pool are undesigned for variable-length buffers; also invalidates take3 EX2-03 |
| R-11 | **sort needs two deformed rows at once** and a `PackedSlot` has one scratch `Row`. 04 §2.4 names PG's `SortTuple.datum1` and then does not propose it. MD-05 is **not** "mechanical" and is re-priced |
| R-12 | the consumer-side `Row()` regression (B7) |

### M-goopg-1 — `FuncCall` is not a `slotToRow` escape hatch (reviewer b)

`expr.go:1307-1308` passes the slot: `evalFuncCall(x, slot, ctx)`. The name
leaked in from `slot.go:247-252`'s historical post-mortem list.

**Fixed** in 02 §8.2, with the correct inventory (six arms at `:479-502`, plus
`InExpr` inside `evalInExpr` at `:10246`, two row-constructor helpers, and
`operators_memoize.go:220`). The cited "doc comment at `:414-418`" does not
exist either — that is the ColumnRef fast-path hoist.

### M-goopg-2 — `userTypeAttrsForOID` IS on the execution path (reviewer b)

02 §9 and 03 §5 claimed nothing on the execution path consults it.
`columnTypeStorageCode` (`operators_ddl.go:1883`) ends
`return userTypeAttrsForOID(typOID).TypStorage` (`:1915`), reaching it **by
name** through `catalog.TypeNameToOID` (`internal/catalog/codec.go:1715`).

**Fixed, and it improves the design.** TD-1 no longer needs a second `pg_type.dat`
transcription — it reuses the existing name→OID→descriptor bridge. 03 §5's
"`attstorage` absent from the executor path" row is corrected to "present". The
table is also 104 arms over ~102 OIDs, not "~40".

### M-goopg-3 — `deformTo` would have discarded the descriptor (reviewer b)

The pseudocode called `DecodeRowRangeIntoMctxPGTupleStyled`. That wrapper's whole
body is `return decodeRowRangeInfo(dst, cols, **nil**, …)` (`codec.go:1332`) — it
hardcodes `info = nil`, discarding the `colTypeInfo` that D-1's descriptor and
D-4's `attcacheoff` both live on.

**Fixed** in 04 §2.2: call the unexported `decodeRowRangeInfo` (`:1340`) or a new
exported wrapper taking `info`. The `PackedSlot` struct also gained the `mctx`
and `style` fields the pseudocode was already passing.

### M-pg-1 (E2) — the peeking rule was misstated, dangerously (reviewer a)

01 §3.1 and 03 §3 said *"a non-zero byte can only be a short header, which is
never padded."* PG's actual comment (`tupmacs.h:104-112`): *"A non-zero byte must
be either a 1-byte length word, **or the first byte of a correctly aligned
4-byte length word**; in either case we need not align."*

On a little-endian host the first byte of a 4-byte header is non-zero whenever
the low six bits of the length are — most of the time. **An implementer reading
01 as an oracle could have written `if peek != 0 { treat as short header }`,
which corrupts tuples.**

**Fixed** in both documents, with PG's comment quoted in full and an explicit
"the peek decides only whether to align, never which header form it is".

### M-pg-2 (E1) — PG's on-disk TOAST pointer is 18 bytes, not 20 (reviewer a)

`varatt_external` is a **16-byte** struct; `VARHDRSZ_EXTERNAL = 2`; so
`VARSIZE_EXTERNAL = 18` (`varatt.h:253`, `:285`). The draft's 20 came from
mistaking `VARTAG_ONDISK`'s *value* (18) for the struct size — a fossil PG's own
comment explains (`varatt.h:56-59`).

**Fixed** in 03 §4, which also now records that the struct is stored **unaligned**
and must be `memcpy`'d before its fields are read, in **native** byte order.

---

## Minor — applied without further comment

**From reviewer (a):** `heap_form_tuple`'s `t_hoff` cite `:1160-1166` → `:1151-1156`
(E4); `slot_getattr` does not route negative attnums — it asserts `attnum > 0`,
and `slot_getsysattr` is the separate inline (E3); `MaxTupleAttributeNumber` is a
round number below the ~1700 `uint8` bound, not the bound itself (M1); tuplestore
skips the 4-byte `t_len` *and* the 6 pad bytes (M2); `varatt_external` is native-,
not little-endian (M3); `SortTuple.datum1` has two documented overrides —
abbreviated keys and the single-Datum case (M4); the `SIZEOF_DATUM == 8` guard is
a sibling of `FLOAT8PASSBYVAL`, not its cause (M5); a paraphrase was presented as
a quotation (M6); the `unlikely()` hints are in `_int`, not the inline (M7); slot
backing-struct line cites 6-11 low.

**From reviewer (b):** `ParseHeapTuple` `:513`→`:500`; `parseHeapTupleAlias`
`:530`→`:516`; `varlenaTextBytes` `:1113`→`:1099`; decode-aligns-unconditionally
is at `:1383` not `:1391` (the claim is true); `rowsOp`/`spillOp` `:659,:683`→
`:651,:674`; `pgRowHasExternal` `:1531`→`:1536`; conditional align
`:1691-1693`→`:1693-1695`; the `dispatch.go` cell-formatting cites 7 low;
`cloneRowOwned` in seqScan `:2181`→`:2179`; `slot.go:246-251`→`:247-252`
(three places); `Datum` struct `:171-186`→`:171-184`; `coltypeinfo.go:26-40`→
struct is `:26-36`; `spill.go:395` is **not** a `cloneRowOwned` site — the only
one is `:614`, and `slot.go:187`'s own comment carries the same stale reference;
`catalog.Column` needs only `Name` and `Type` (`Ordinal` and `NotNull` are never
read by the codec); `conn_tx.Rows` is per **cursor**, not per connection; the
"packed MinimalTuple / goopg has no such thing" sentence is verbatim only at
`hashsize.go:29` — the other three sites say it in their own words.

**Counting.** The retention census is **48**, from 05 §2.1's grep; README and
04 §0.2 said 49 and attributed it to 02 §3 (which is an eight-row illustrative
table). Both corrected. `groups map[string]*groupRuntime` is a **local** in
`aggregateOp.Open`, not a struct field, and is excluded from the 48.
The `cloneRowOwned` count of 19 includes the definition line: 18 calls.

---

## Not applied, with reasons

- **Reviewer (c)'s doubt about §3's gopls figures** (859 `.Kind` / 775 `.Int`
  don't reproduce under grep). The two methods measure different things — that is
  §3's own point, demonstrated in both directions. Left as-is, with the error
  bar §3 already carries. It does not matter: column (A) is declined either way.
- **Reviewer (c)'s "this is formally query-specific forcing"** (MD-04's gate
  names Q9). Recorded rather than silently fixed: 06 §3 now states the EX-P2
  violation and requires the gate to be restated over the operator or the item
  split. The bundle does not propose any benchmark-shaped fast path.
- **Splitting the `attstorage` question out of TD-1.** Reviewer (b) is right that
  it already exists; TD-1 is now *reuse*, which is smaller, so no split is needed.

---

## Verification claims neither reviewer could check

- "~26 type-assertion sites" (02 §8.1, 04 §9.1) — `rowshape_assert.go:43-44` says
  "ten `op.(type)` switches plus … and others"; 26 is not derivable without
  fixing a counting rule. The **six** slot-type switches, which are the ones that
  matter, were verified individually.
- The Q6 "6 of 16 columns / ~2 % of rows" and the Q14/Q3 CPU percentages are
  quoted accurately *from source comments*; neither reviewer re-measured them.
- 03 §3.1(a)'s conclusion that goopg's tuples are PG-readable today depends on
  pad bytes always being zero — reviewer (b) confirmed it
  (`encodeRowPGCtx` grows `out` with `append(out, 0)` at `:110-111`, `:123-124`).

(End of file)
