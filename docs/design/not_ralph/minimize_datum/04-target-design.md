# 04 — Target design: packed retention, lazy deform

**Status:** design. No code has been written. Every symbol cited is verified in
the tree at 2026-09-03.

---

## 0. Thesis

**Retained rows are packed PostgreSQL-format tuples. Only the row currently in
flight is deformed — lazily, up to the highest attribute asked for — into a
per-slot scratch `Datum` array.**

`Datum` does not shrink. It stays exactly 48 bytes and stays the *working*
format. What changes is its role: from **storage format**, multiplied by row
count, to **per-operator working buffer**, one row deep. That demotion is why the
48 bytes stop mattering, and it is the whole of the design.

This is PostgreSQL's slot duality (01 §§4-6), reached with machinery goopg
already owns (02 §5, §11).

### 0.1 Non-goals, and why

| non-goal | reason |
|---|---|
| **Shrinking `Datum` below 48 B** | An `unsafe` union can reach ~32 B GC-safely by overlaying non-pointer words. It buys ~33 % on a quantity that, after this design, is no longer multiplied by row count. Wrong investment; take3 13 §10 declines it and this bundle agrees. |
| **Columnar or vectorised execution** | Different project, different plan shapes. Nothing here forecloses it; nothing here requires it. |
| **Changing `Datum`'s field layout, `Kind`, or `Int`** | 05 §3 shows those have ~1,600 reach-into sites. This design touches none of them, and any proposal that does is a different, larger project. |
| **Byte-identity with PostgreSQL as a precondition** | 03 §7.2: separable, and separated. |
| **PG-format TOAST pointers** | 03 §4, out of scope, stated rather than implied. |
| **Plan parity or plan changes** | The plan-shape pin (06 §2) is a gate, not an objective. |

### 0.2 Standing with take3 — no licence is claimed

An earlier draft of this section claimed take3 `13-executor-target-design.md`
§10 grants a re-proposal path for this work. **It does not, and the claim is
withdrawn.** §10's sentence is *"`Datum` re-layout below 48 B **and JIT** are
declined in §0.1 with reasons; re-proposing **either** requires new measurement"*
— "either" ranges over a two-element list that does not include §0.1's first
clause, *"a new row representation"*. That clause has no stated re-proposal path,
and `PackedTuple`/`PackedSlot` is squarely within it.

Worse for the original argument: §0.1's parenthetical shows take3 declined this
*while holding the measurement being offered* — *"(12 §12: narrowing without
shrink still halved batches; shrink without narrowing is not proposed)"*, and
12 §12 row 1 **is** `FINDING-p401-alone-is-not-enough.md`.

**This bundle therefore argues on merits and requires acceptance by whoever owns
take3 13. It may not proceed on a claimed permission.** README's status section
states the same thing where a reader will hit it first.

### 0.3 What the measured evidence says, and what this bundle is worth

The FINDING's 128 → 64 figure is **modelled, not measured**: it states that it
*fed* `hashsize.Choose` hand-derived column counts, and the formula it evaluated
(`EntryBytes = ncols×48 + 24 + avgVar`) is the one D-3 proposes to replace. Its
own caveat asked for the collector's real answer before the figure was trusted,
and take3 `09-verification-and-acceptance.md:48,190` records it: Q9 P4-01
witness, **`Batches:` 8 → 1**, narrowed width **≈100, not 6**.

`.ralph/deferral_ledger.md:2036` (2026-09-03, *"measured at equal cardinality for
the first time"*) is the executed A/B: TPC-H Q9, same `work_mem`, same row
counts — widths **1098/1642/2090/3164 B** vs PostgreSQL's **23/32/54/81 B**, peak
hash memory 97 MB vs 38 MB, **8 batches vs 1**, **63.8 s vs 6.2 s** — and its
recorded disposition is *"This is P4-01's PathTarget/projection. Resume there."*

1098 B / 48 ≈ 22 columns; packing 22 columns of TPC-H data yields ~150–250 B, not
23 B. **Packing closes roughly 5× of a ~48× width gap. Narrowing closes the
rest.** §10's "independent and multiplicative" is true of the arithmetic and
misleading about magnitudes, and is corrected there.

This bundle's honest justification is **byte-parity with PostgreSQL and a single
retention format**, not closing the Q9 residual. 05 §1.5 prices three cheaper
interventions against it.

### 0.4 Two corrections the earlier draft claimed, withdrawn

The earlier draft said the FINDING "named only the hash table" and "implied the
lazy slot was never built in any form". Both are wrong on re-reading it:

- The FINDING says, in its own words, *"**The hash table is not special** — it is
  simply where the pain was measured first"*, followed by a four-site table, and
  carries the slot-duality thesis and the ~33 % unsafe-union dismissal verbatim.
  This bundle's framing is largely reproduced *from* it.
- Its "the lazily-deforming slot was anticipated; it was never built" is still
  true. `DecodeRowRangeIntoMctxPGTupleStyled` is a decoder function, not a slot,
  and the FINDING's sibling sentence already credits the packed-encoding half.

What survives as a genuine addition: the retention census (02 §3, 05 §2.1) is
**48 struct fields**, not four sites — including `windowOp` (a whole partition
set, no spill path) and `conn_tx.Rows` (a whole result set per connection).

---

## 1. The shape of the change

```
today                          target
─────                          ──────
producer  → Row=[]Datum        producer  → Row=[]Datum          (unchanged)
              │                              │
              ▼                              ▼
   retention: []Row              retention: PackedTuple ([]byte)
              │                              │  Get(col) ─┐
              ▼                              ▼            │ deform [nvalid, col]
   consumer  → Row=[]Datum       consumer  ← scratch Row ─┘  (per-slot, reused)
```

Producers still build `[]Datum` — that is where values come from, and 02 §8.3
shows hashing and comparison need Datums anyway. Consumers still read Datums.
Only the *middle* changes, and only where rows are held.

---

## 2. `PackedTuple` and `PackedSlot`

### 2.1 The stored form

```go
// PackedTuple is a retained row in PG MinimalTuple layout (03 §7.1).
// It owns its bytes; nothing it references can be invalidated by a
// producer's next Next() or by an arena reset.
type PackedTuple struct {
	buf []byte // t_len | mt_padding | t_infomask2 | t_infomask | t_hoff | t_bits | pad | data
}
```

One allocation per retained row, holding header, bitmap and data — as
`heap_form_minimal_tuple` does (01 §2). Accessors are `hoff`-relative; there is
no negative-offset trick (03 §7.1).

For the hash join, the 4-byte hash value is stored immediately ahead of the
tuple in the same allocation, mirroring `HashJoinTupleData` (01 §6) and
`spill.go:104 WriteRowHashed`'s existing framing (02 §6).

### 2.2 The slot

```go
// PackedSlot deforms a PackedTuple on demand. It implements TupleSlot.
// The (nvalid, off) pair is PG's tts_nvalid + HeapTupleTableSlot.off
// (01 §4): nvalid columns are valid in values, and off is the byte
// offset in the tuple's data area that column nvalid starts at.
type PackedSlot struct {
	schema optimizer.Schema
	desc   *TupleDesc  // §3
	tup    PackedTuple

	values Row // scratch, len == len(schema); REUSED across tuples
	nvalid int
	off    int
	mctx   *mmgr.Context     // §7 D-5
	style  array.OutputStyle // GUC, fixed at Open

	ctidBlock uint32 // §7
	ctidOff   uint16
	hasCTID   bool
}

func (s *PackedSlot) Get(col int) Datum {
	if col >= s.nvalid {
		s.deformTo(col + 1)
	}
	return s.values[col]
}

func (s *PackedSlot) deformTo(n int) {
	off, err := decodeRowRangeInfo( // NOT the exported wrapper — see below
		s.values, s.desc.cols, s.desc.info, s.tup.data(), s.tup.bitmap(),
		s.tup.natts(), s.mctx, s.style, s.nvalid, n, s.off)
	// err handling: §9.3
	s.off, s.nvalid = off, n
}
```

**`deformTo` is a call to a function that already exists**, with the argument
order and return type it already has. This design's contribution on the deform
side is an *owner* for `(nvalid, off)` across operator calls — which is exactly
what `seqScanOp` carries by hand today within a single `Next()` (02 §4).

**One correction to an earlier draft.** It called the exported
`DecodeRowRangeIntoMctxPGTupleStyled` (`codec.go:1331`). That wrapper's entire
body is `return decodeRowRangeInfo(dst, cols, nil, …)` (`:1332`) — it **hardcodes
`info = nil`**, so it would discard the `colTypeInfo` that D-1's descriptor and
D-4's `attcacheoff` both live on, and re-derive alignment per column per row.
`deformTo` must call the unexported `decodeRowRangeInfo` (`codec.go:1340`), or a
new exported wrapper that takes `info`.

Two rules the existing decoder's doc comment (`codec.go:1315-1330`) already
imposes, restated as slot invariants:

- **A suffix can be skipped; a prefix cannot.** `Get(5)` on a fresh slot deforms
  columns 0..5, not column 5. There is no offset array (01 §4 — PostgreSQL has
  `attcacheoff` and goopg does not; §6).
- **A partially-filled `values` must never escape.** Entries past `nvalid` hold
  the *previous* tuple's values. `Row()` must call `deformTo(len(schema))` first;
  nothing may index `values` directly.

### 2.3 `Row()` and `Materialize()`

`Row()` deforms fully and returns `s.values` — which **aliases the slot's
scratch** and is therefore valid only until the slot is reloaded. That is the
same contract `MaterializedSlot` acquired at M0092-0002 (`slot.go:95-110`:
"slot valid until next Next() unless materialized"), so it is not a new hazard
class, but it is a new *instance* of one and 06 §6 tests it per seam.

`Materialize()` returns a `*MaterializedSlot` over `cloneRowOwned(Row())`,
preserving today's contract exactly.

### 2.4 What does not change

- `SlotView` (`Get`/`IsNull`) is unchanged, so **expression evaluation is
  untouched**: ~488 `evalExprSlot` call sites, both the interpreted
  (`expr.go:413`) and compiled (`exprnode.go:288`) evaluators, already read
  columns through it (02 §8.2).
- `compareDatum`, `datumKey`, `sortKeyVals` and the aggregate transition
  functions are untouched. They consume deformed values, and 02 §8.3 explains
  why they must: a packed tuple's raw bytes would key `1` and `1.0` differently,
  the exact bug `canonicalNumericKey` exists to prevent. PostgreSQL does the same
  — `SortTuple.datum1` hoists one deformed key (01 §6).
- The client output loop is untouched. It is text-only and GUC-driven (02 §7),
  and it reads columns in ascending order, immediately, without retaining — the
  ideal access pattern for a prefix-walk deform.

---

## 3. `TupleDesc` — the feasibility crux

02 §9 is the blocker: **scans hold `[]catalog.Column`; join, aggregate and sort
outputs hold only `optimizer.Schema`**, which is `{Name, Type catalog.Type,
SourceTableIdx}` and carries no width, no by-value flag, no storage class. And
`catalog.Type` is `{Name string, Args []int64, IsArray bool}` — a *string*.

Retention lives on the intermediate rows. So this must be solved first.

**Decision D-1.** Introduce an executor-local `TupleDesc`:

```go
type TupleDesc struct {
	cols []catalog.Column // for the existing codec entry points
	info []colTypeInfo    // extended per 03 §5 (TD-1): + len, byVal, storage
}

func NewTupleDesc(s optimizer.Schema) (*TupleDesc, error)
```

- `catalog.Column` is a large struct (`internal/catalog/catalog.go:201-315`),
  but the codec reads exactly **three** of its fields: `c.Type` (7 sites, the
  load-bearing one), `c.Name` (3 sites, all error text) and `c.MissingValue`
  (1 site, `:1361`). `Ordinal` and `NotNull` are never read. So a `SchemaColumn`
  needs only `Name` and `Type`; `MissingValue` stays nil, which is correct — an
  intermediate row has no `ALTER TABLE` history.
- `colTypeInfo` (`internal/executor/coltypeinfo.go:26-40`) is extended per 03
  TD-1 and gains the descriptor fields.
- Built once, at operator `Open`, honouring `colTypeInfo`'s stated staleness
  contract: *"MUST be derived wherever the column list itself is resolved (an
  operator's `Open`), never cached against a table across DDL"* (`:12-25`).

### 3.1 The codec does not fail on an unknown type — it silently retypes

This is the sharpest edge in the whole design, and it was found by reading the
encoder rather than by reasoning about it.

`encodeValuePGCtx` (`codec.go:440`) dispatches on
`strings.ToLower(t.Name)` — about 60 arms — and each arm then switches on
`d.Kind` with an **error** default (`"expected bool, got kind %d"`). But the
**outer** switch's default arm (`codec.go:1055-1063`, cited as `:1039-1046`
before the D-02 audit re-verified it) does not error. It falls back to
`coerceTextLikeDatum` + `varlenaTextBytes`: it packs the value **as text**.

One correction the audit added: the default arm is **not total**.
`coerceTextLikeDatum` (`codec.go:132`) has arms for `KindString`,
`KindBytes`, `KindInt`, `KindNumeric`, `KindBool`, `KindTime` and
`KindInterval`, and a hard error default (`codec.go:156-157`). So a
`KindEnum` or `KindToastPointer` reaching the default arm fails LOUDLY
rather than retyping silently — see the ledger row for the enum case,
which is reachable from the index-scan output path.

The decoder is symmetric by design and says so (`codec.go:2033-2047`, cited
as `:1981-1987` before the D-02 audit re-verified it):

> Unknown type (e.g. "point", "path", custom types). goopg's `encodeValuePG`
> stores them as PG varlena text (the default branch calls `varlenaTextBytes`).
> Decode symmetrically.

and returns a `KindString` Datum.

So an unknown-typed value **round-trips, without error, as a different
`DatumKind` than it went in as**. A value that entered as `KindInt` under a type
name outside the 60 arms comes back `KindString`. Nothing reports anything.

For the on-disk path this has been acceptable: a column's type name comes from
`CREATE TABLE`, so it is inside the switch, and the fallback exists for genuinely
unsupported column types where text is the honest representation.

**For intermediate rows it is not acceptable.** Derived column types come from
the planner's `exprType` inference, not from `CREATE TABLE`, and a retyped Datum
in a hash table is exactly R-5 (§9.6) — the class that cost this project TPC-DS
Q72 at small `work_mem` and a silently-dropped hash-join pair.

**Decision D-2: `NewTupleDesc` validates against an explicit allow-list and
declines. It does not rely on the encoder to fail, because the encoder does not
fail.**

```go
// packableType reports whether a Datum of t's natural Kind survives the
// encode/decode round trip AS THAT KIND. It must be derived from the same
// lists the two switches use — a third transcription is a sibling-path bug
// waiting to happen (03 §5).
func packableType(t catalog.Type) bool
```

**Correction (D-02 audit, 2026-09-05): "has a named arm" is the WRONG
predicate and would have produced a false STOP verdict.** Read literally it
declines `text`, `varchar`, `character varying`, `bpchar`, `character`,
`json` and `jsonb` — every text column in both benchmark suites (13
`varchar` columns in TPC-H, 50 in TPC-DS) — because those types have no
*named encoder* arm: the shared default at `codec.go:1055` already IS their
correct encoder, and they round-trip `KindString → KindString`.

The predicate is instead:

> the union of the two switches' named arms, PLUS the text-like spellings
> the shared default handles correctly, MINUS the arms that are not
> Kind-stable.

The not-Kind-stable set found by the audit (22 spellings against 51
packable, both derived mechanically from the two switches): the eleven
encoder-only blob arms (`oid[]`/`_oid`, `int2[]`/`_int2`,
`float4[]`/`_float4`, `anyarray`, `char[]`/`_char`, `oidvector`,
`int2vector`), plus `text[]`/`_text`, plus `unknown`, plus `pg_node_tree`
(content-dependent), plus two the first pass missed:

- **bare `"char"`** — `packableType` must therefore read `t.Args`, not just
  `t.Name`: `char(n)` is varlena text and stable, while bare `"char"` is
  one raw byte, accepts `KindInt` on encode, and always decodes
  `KindString`.
- **the whole float family** (`float4`, `float8`, `real`, `double
  precision`, `double`, `float`) — `floatTextDatum` returns `KindNumeric`
  for finite values but `KindString` for NaN and Infinity, so the decoder
  arm is kind-ambiguous. The blob arms are a deliberate
convention — `internal/initdb/catalog_heap_reload.go:573-596` re-parses that
raw payload by hand — so they are not a bug on the on-disk path, but they
are unpackable for intermediate rows all the same.

Note also that this list is now the **fifth** transcription of the same type
set in tree, not the third: `encodeValuePGCtx`, `decodePhysicalPGValueLowered`,
`pgPhysicalTypeIsVarlena` (`codec.go:1492`) and `catalog.PhysicalTypeAlign`
(`internal/catalog/physical_align.go:18`) already disagree with each other
(see the `float` ledger row). That strengthens 03 §5's argument for deriving
rather than transcribing.

A schema containing any non-packable column makes `NewTupleDesc` decline, and
the operator falls back to `[]Row` retention for that plan node. Declining is
always safe; the risk is declining *often*.

**Two obligations follow.**

1. **The allow-list must be derived from the switches, not written beside
   them.** Three transcriptions of one type list (encoder, decoder, allow-list)
   is the drift hazard 03 §5 already flags for the descriptor table. 06 §3
   MD-01's agreement test covers both.
2. **R-1 must be audited with counts before any conversion slice.** TODO item
   MD-02, and its result is a gate: if derived columns decline at a high rate on
   TPC-H and TPC-DS, the intermediate-row half of this design does not pay and
   the bundle must say so rather than proceed.

A useful secondary result the audit gets for free: **every decline is also a
latent on-disk retyping bug** for any column of that type, on a path that
already ships.

---

## 4. Where packing happens

**Only where `cloneRowOwned` happens today.** That call is already the ownership
boundary (`datum.go:493`, reached from `slot.go:95,113,185-187` and
`spill.go:614`), and it is already the point at which arena-backed Datums are
promoted to owned storage (`MaterializeArena`, `datum.go:433`).

(`slot.go:187`'s own comment cites `spill.go:395` for this; that reference is
stale — `:395` is inside the big-numeric encoder. The only `cloneRowOwned` call
in `spill.go` is `:614`.)

Streaming operators — filter, project, limit, the scan pass-through — are
untouched. They neither retain nor pack.

### 4.1 The retention inventory, triaged

02 §3 and 05 §2 list **48** `[]Row`-typed struct fields. They are not equal.

**Tier A — bounded by `work_mem`, sized by the planner, measured by the FINDING.**
These are the design's target and its evidence.

| site | field | file:line |
|---|---|---|
| hash join build | `lazyHash map[string][]Row` | `operators_join_agg.go:53` |
| hash join build | `lazyIntHash map[int64][]Row` | `:71` |
| **parallel** hash build | `hash map[string][]Row` | `parallel_hash_build.go:43` |
| **parallel** hash build | `intHash map[int64][]Row` | `:51` |
| sort | `rows []Row` | `operators.go:769` |
| materialize | `mem []Row` | `operators_material.go:68` |

`parallel_hash_build.go`'s two maps are **siblings of the serial pair** and must
convert in the same commit. This is the repo's most-recorded bug class
(`pattern_sibling_paths_must_agree`), and here it is structural: two hash tables
with two retention formats would differ in `hashsize` accounting as well as in
behaviour.

**Tier B — unbounded, no spill path, no budget.** Converting these is worth more
per line than Tier A, and none of it is modelled by the planner today.

| site | field | note |
|---|---|---|
| window | `rows []Row` (`operators_window.go:22`) | **buffers the entire partition set unconditionally; there is no spill path in this operator at all** |
| memoize | `served`, `filling []Row` (`operators_memoize.go:82,85`) | |
| CTE cache | `CTERowCache map[string][]Row` (`context.go:623`) | |
| recursive worktable | `WorkTableRows` (`context.go:364`), `working`/`output` (`operators_recursive_cte.go:63-64`) | |
| lateral CTE | `innerCTE`, `savedCTE map[string][]Row` (`join_lateral_stream.go:85-86`) | |
| outer-join sweep | `sweepRows`, `fillNullBuild []Row` (`operators_join_agg.go:261,268`) | |
| RETURNING | `retRows []Row` ×4 (`operators_storage.go:2355,4386,6219`, `operators_upsert.go:71`) | |
| result set | `cursorEntry.Rows []executor.Row` (`internal/postmaster/conn_tx.go:44-46`) | **a whole result set per declared cursor** (a connection may hold several) |
| Gather | `rows`, `cur []Row` (`operators_gather.go:44,64`), `pending` (`operators_gather_merge.go:40`) | §8 |
| distinct / setop / SRF / catalog views | `rows []Row` × ~13 | small, mechanical |

**Tier C — aggregate internals.** `operators_join_agg.go:1831-1959`:
`groupValues`/`passthroughVals` per group (the `groups map[string]*groupRuntime`
that holds them is a **local** in `aggregateOp.Open` at `:2009`, not a struct
field, so it is not in §2.1's 48), plus four `[][]Datum` accumulators
(`arrayElemKeys :1905`, `strElemKeys :1915`, `withinGroupElems :1947`,
`distinctUserAggRows :1959`) for ordered-set, `WITHIN GROUP` and `DISTINCT`
aggregates, which retain unbounded `[]Datum`. The per-group *representative* is
Tier-A-like and converts; the accumulators are agg-transition state and stay
Datums (§2.4).

**Tier D — `spill.go`'s on-disk payload.** Converts **last** (03 TD-5).

### 4.2 There is no tuplestore

`git ls-files | grep -i tuplestore` returns nothing. goopg has no PG-style
`Tuplestorestate`; `spill.go` + `drainRowsBounded` is the only spill mechanism
and only sort and hash join use it. Every Tier B site buffers without a budget.

This is worth stating plainly because it changes what "success" means: for Tier A
the win is *fewer batches at the same `work_mem`*; for Tier B the win is *a
smaller unbounded buffer*, which reduces OOM exposure but is invisible to any
`work_mem`-based gate. 06 §4 measures them differently.

---

## 5. The planner must move in lockstep

`internal/executor/hashsize` hard-codes today's geometry into the **planner's**
cost model:

```go
DatumBytes    = 48   // per column
RowSliceBytes = 24   // one []Row slice header
MapSlotBytes  = 48   // map[K][]Row slot
EntryBytes(ncols, avgVar) = 48*ncols + 24 + avgVar
```

and `Choose` derives `bucketSize = entry + MapSlotBytes` on the multi-batch path.
The package doc states the invariant: it imports neither `executor` nor
`optimizer`, and planner and executor must not drift.

**Decision D-3: the `hashsize` model changes in the same commit as the storage
it models.** A commit that packs the hash table without re-deriving `EntryBytes`
leaves the planner costing a geometry the executor no longer builds — which is
`m0076_q5_cost_model_root_cause` in reverse, and a batch-count gate would not
catch it because the counts would be internally consistent and wrong.

The new model is 01 §6's: `HJTUPLE_OVERHEAD + MAXALIGN(SizeofMinimalTupleHeader)
+ MAXALIGN(tupwidth)`, which is what `hashsize.go:27-29`'s comment already names
as the thing goopg lacks. `RowSliceBytes` and `MapSlotBytes` do not survive
unchanged: replacing the bucket list with packed entries invalidates both, and
whatever replaces them must be *measured*, not assumed (06 §3).

`estimatedRowBytes` (`spill.go:541`) uses the same 48 and moves with it —
`hashsize.go:~45` already says the two must not drift.

---

## 6. Deform cost, and the `attcacheoff` gap

01 §4: PostgreSQL caches a fixed byte offset per attribute (`attcacheoff`) and
falls to a "slow" walk only after the first NULL or varlena. goopg has **no**
`attcacheoff` — `decodeRowRangeInfo` recomputes alignment for every column of
every row (03 D5).

Consequence: `Get(col)` on a packed slot is O(col) in the general case, where
`MaterializedSlot.Get` is O(1). For a probe that reads two key columns out of
twenty this is a win; for one that reads column 19 it is not.

**Decision D-4: add the `attcacheoff` fast path as part of the deform work, not
after it.** `colTypeInfo` (D-1) is the natural home — it is per-descriptor,
built at `Open`, and already carries the DDL-staleness contract. The rule is
PostgreSQL's (01 §4): cache the offset while no NULL and no varlena has been
seen; stop caching after either.

Without it, the deform path's cost is a function of column position, and 06 §4's
CPU arm would be measuring the absence of a cache rather than the cost of the
format.

**But D-4 is not a mitigation for R-3, and §9.10 (R-9) says why:** PostgreSQL's
cache dies at the first NULL and the first `attlen <= 0`, and the Q9 build side
is varlena from column 0 or 1 (HammerDB declares TPC-H integer keys `NUMERIC`;
`o_orderstatus` is bpchar). D-4 helps fixed-width prefixes, which is a real and
common shape. It does not help this bundle's own witness.

---

## 7. Ownership, arenas, and TID

**Arenas.** 02 §1: hot-path `KindString`/`KindBytes` carry `ArenaID != 0` and
resolve `mmgr.Lookup` per access. Packing **copies the bytes into the tuple**, so
a packed tuple is arena-independent by construction. That *retires* the
arena-lifetime hazard at every converted seam — `MaterializeArena`
(`datum.go:433-478`) has nothing to do for a `PackedTuple`, and `VirtualSlot`'s
2026 fix for skipping it (`slot.go:181-188`) has no analogue to get wrong.

The cost moves to the other end: `deformTo` produces Datums that must point
*somewhere*. Two options, and the choice is measured, not assumed:

- **(a) alias the tuple buffer** — `Buf` pointing into `PackedTuple.buf`, as
  PostgreSQL does (01 §7: by-reference Datums alias the tuple, owning nothing).
  Zero copy, but re-introduces `Buf` as a GC-traced pointer on a path that
  currently has none (`docs/design/perf-optimize/02-datum-pointer-free.md`), and
  the alias is invalidated when the slot reloads.
- **(b) decode into an arena** — what `DecodeRowRangeIntoMctxPGTupleStyled`
  already does when given an `*mmgr.Context`. Keeps the pointer-free property;
  costs an arena write per varlena per deform.

**Decision D-5: start with (b)**, because it is what the existing decoder does
and it preserves an invariant that was paid for once.

**Option (a)'s gate is a full-sweep values diff on both suites, NOT the GC arm.**
§11.2: the `Datum` packed-layout flip failed on exactly this aliasing class —
retained Datums pointing into reused arena slots — and it **passed a five-query
gate** before a 21-query sweep showed seven queries returning zero rows. A GC or
allocation arm would not have caught it. See also R-8 (§9.9): (b)'s arena reset
point is itself unspecified.

**TID.** `MaterializedSlot` carries `{ctidBlock, ctidOff, hasCTID}`
(`slot.go:68-74`), injected by `seqScanOp` for `CTIDExpr` and consumed by
`materializeCursor` for `WHERE CURRENT OF`. `PackedSlot` carries the same three
fields. It must, or the two ctid-propagation type switches (§9.1) silently set
`hasCTID = false`.

---

## 8. Parallelism

Gather moves `rowBatch{rows []Row}` over a Go channel (`operators_gather.go:42-45`),
256 rows per batch, depth 2. There is **no serialisation boundary** — rows are
fully materialised and must be arena-independent before crossing
(`parallel_runtime.go`'s ownership contract).

A `PackedTuple` is arena-independent by construction (§7), so it satisfies the
channel's contract more cheaply than `cloneRowOwned` does, and it shrinks the
per-row bytes in flight. Gather is therefore a natural beneficiary with no
compatibility constraint.

Two cautions:

- The parallel hash build's two maps convert **with** their serial siblings
  (§4.1), not separately.
- A Datum-safety bug in a worker is a wrong answer, not a crash;
  `parallel_substrate_test.go:26-80` is the existing guard and 06 §6 extends it.

---

## 9. Risk register

### 9.1 R-0: a new slot kind missing from a type switch — **silent wrong answers**

02 §8.1: six switches over `TupleSlot`/`SlotView` have `default` arms that fail
*silently*.

| site | file:line | failure |
|---|---|---|
| `slotToRow` | `slot.go:230-259` | `default: return nil` — the row becomes nil |
| `evalFastExpr` ColumnRef | `exprnode.go:288` | falls through to an unchecked `Get` |
| `evalExprSlot` ColumnRef hoist | `expr.go:413` | four per-type bounds guards; an unlisted type skips them |
| ctid in `fillFromTupleSlot` | `opnode.go:176` | `default: hasCTID = false` |
| ctid in `projectOp.Next` | `operators.go:367` | same |
| `VirtualSlot` fast path | `opnode.go:153` | performance only |

**This bug has already been committed once**, and its post-mortem is in the tree:
`slot.go:247-252` records that when the `*Slot` arm was missing from `slotToRow`,
`InExpr`/`CaseExpr`/`SubqueryExpr`/`ExistsExpr`/`ExtractExpr`/`FuncCall` fell to
`default` and produced spurious "nil slot" errors.

**Mitigation.** The **first** commit of the implementation adds `PackedSlot` to
all six switches with a test per switch, *before* any operator produces one.
`rowshape_assert.go:39-48`'s post-mortem forbids the wrapper alternative:
capability discovery is by type assertion across ~26 sites, and an opaque wrapper
changed TPC-H results to 7 VALUE-DIFF / 4 ROWS-DIFF with zero assertion failures.

`exprnode.go:288`'s comment forbids the other alternative: replacing the concrete
switch with a `Width() int` capability interface cost ~1.4 ns/eval on
`BenchmarkJoinKeyEval` and made the compiled path slower than the interpreter it
replaced. **Add a fifth arm; do not widen the interface.**

### 9.2 R-1: derived-column type fidelity — **the feasibility risk**

§3.1. Two distinct problems, and the second is worse than the first:

1. If `catalog.Type` for aggregate outputs, computed expressions and
   subquery-derived columns does not reliably name a type the codec has an arm
   for, intermediate-row packing declines — and intermediate rows are where the
   retention is.
2. The codec **does not signal** the condition. Its outer default packs as text
   and decodes as text, so an unrecognised type round-trips as `KindString`
   without error (§3.1). Relying on an encode error to detect this would detect
   nothing.

**Mitigation.** D-2's explicit allow-list, derived from the codec's own switch
arms rather than transcribed beside them. MD-02 is an audit with counts before
any conversion, and its result gates the intermediate half of the design.
Declining is safe (fall back to `[]Row`); declining *often* means the design does
not pay and must be re-scoped in public.

### 9.3 R-2: a decode error mid-`Get`

`Get(col) Datum` has no error return, and `SlotView` cannot grow one without
touching every evaluator call site. `deformTo` can fail — a corrupt tuple, an
unknown type name.

**Mitigation.** Encode-side validation makes decode total: a tuple only exists if
`EncodeRowPG` succeeded on every column, and the descriptor is fixed at `Open`.
A `deformTo` failure is then an invariant violation, and the slot stores it and
surfaces it at the next error-returning boundary (`Next()`), never as a zero
Datum. **Never a `NullDatum` fallback** — that is the shape of a silent
wrong-answer bug.

### 9.4 R-3: encode/decode CPU exceeds the memory win

Real, and not knowable from a design document. Packing costs a memcpy per
retained row plus a deform per probe.

**Mitigation.** The hash join converts first **as a measurement** (§10), with the
CPU arm as a first-class result, not a footnote. If encode/decode dominates on
narrow rows, the design's scope narrows to wide-row sites and says so.

### 9.5 R-4: two retention formats during migration

Unavoidable and time-bounded. The hazard is `pattern_sibling_paths_must_agree`:
`hashsize`'s model, `estimatedRowBytes`, the serial and parallel hash builds, and
encode↔decode are all sibling pairs.

**Mitigation.** D-3 (model with storage, same commit); §4.1 (parallel with
serial, same commit); 03 TD-2 (exhaustiveness tests move with the format);
06 §6 (a sibling-parity test per pair).

### 9.6 R-5: a type discriminator dropped in the packed form

The precedent is exact and expensive: `TimeSubtype` was a `Flags` bit,
`encodeDatum` did not serialise it, every spilled DATE became a bare timestamp,
and TPC-DS Q72 failed at small `work_mem` while passing at 2 GB (02 §1).

**Mitigation.** 03 TD-2: `TestSpillDatumRoundTripCoversEveryKind` and
`…EveryTimeSubtype` — which walk `datumKindCount` and `timeSubtypeCount` and are
the only enforcement of the Datum kind space as a closed set — are pointed at the
PG-format codec **before** anything depends on it.

### 9.7 R-6: an executor change moves a plan

`hashsize` feeds the planner (§5), so this design can move plans by construction.

**Mitigation.** The plan-shape pin (06 §2) on every commit. A moved plan is
reported with its cost roll-up, never "fixed" executor-side by preference.

### 9.8 R-7: the descriptor has no owner past the operator

D-1 builds `TupleDesc` at `Open` and honours `coltypeinfo.go:22-25`'s "never
cached against a table across DDL". But MD-08 converts `CTERowCache`
(`context.go:623`) and MD-10 converts `conn_tx.Rows`
(`internal/postmaster/conn_tx.go:46`) — buffers whose lifetime is the **cursor or
transaction**, not the operator. A `PackedTuple` held in an open cursor outlives
every `Open` that could have built its descriptor, and a packed tuple without its
descriptor is undecodable bytes. `WHERE CURRENT OF` runs through exactly this
path (04 §7's TID discussion).

**Mitigation.** Cross-statement retention sites carry an owned descriptor with
the buffer, or they do not convert. MD-08 and MD-10 must resolve this **before**
they start; if the answer is "carry a descriptor per cursor", its DDL-invalidation
story is new design, not a conversion.

### 9.9 R-8: the scratch arena's reset point is unspecified

D-5 chooses decode-into-arena, and §7 does not say when that arena resets. If it
does not reset per tuple, deforming N rows accumulates N rows of varlena bytes
and gives back the memory this design removes. If it does, every reload pays
arena churn. **This is the most consequential unstated detail in the design** and
MD-03 may not land without answering it, with a test that bounds arena growth
across a scan of known length.

### 9.10 R-9: `attcacheoff` does not help this bundle's own witness

01 §4 is transcribed correctly: PostgreSQL's offset cache dies at the first NULL
and at the first `attlen <= 0`. §6 (D-4) then implies it removes the O(col)
problem. **It does not, for the rows this bundle targets.** HammerDB declares
TPC-H integer keys as `NUMERIC` (varlena) and `o_orderstatus` is `char(1)`
(bpchar), so on `orders` the cache dies at column 0 or 1. §6's own sentence —
"the deform path's cost is a function of column position" — remains true *after*
D-4 on the Q9 build side.

**Mitigation.** §6's claim is corrected in place. D-4 is still worth doing (it
helps fixed-width prefixes, which is a real and common shape), but it is not a
mitigation for R-3, and MD-04's CPU arm must not be read as if it were.

### 9.11 R-10: `Release()` and row pooling are undesigned

`row_pool.go:25` is a **width-keyed** `[maxPooledRowWidth+1]sync.Pool`, with
`acquireRow` (`:42`) / `releaseRow` (`:62`) used across 13 sites.
`PackedTuple` buffers are variable-length and pool-hostile; `MaterializedSlot.
Release()` is a no-op today and `PackedSlot.Release()` is unspecified. Variable-
size allocations also introduce fragmentation where the pool currently serves
fixed widths.

This additionally **invalidates take3 EX2-03 (pool sizing)** if that lands first.
05 prices none of it.

### 9.12 R-11: sort needs two deformed rows at once

A comparator needs both operands' keys; a `PackedSlot` has **one** scratch `Row`.
PostgreSQL solves this with `SortTuple.datum1` — §2.4 *names* `datum1` and then
does not propose it. Without an equivalent, every comparison in an O(n log n)
sort deforms two tuples from scratch.

**MD-05 is therefore not "mechanical, 80–150 LOC"** as 05 §4 prices it. It needs
a hoisted-key design (PG's, or precomputed `sortKeyVals` retained alongside the
packed tuple — which `sortOp` already computes, `operators.go:905`). Re-price
before starting.

### 9.13 R-12: the consumer-side `Row()` materialisation regressed this exact seam, twice

See §11. Not previously in this register; it belongs at the top of it.
---

## 10. Sequencing

1. **MD-01 — `TupleDesc` (D-1) and the descriptor table (03 TD-1).** No behaviour
   change. Unblocks everything, including 03's D1.
2. **MD-02 — the R-1 audit.** Counts, no code. **Gate:** if derived columns
   decline at a rate that makes intermediate packing unprofitable, stop and
   re-scope.
3. **MD-03 — `PackedTuple`/`PackedSlot` + all six type-switch arms + `attcacheoff`
   (D-4) + the exhaustiveness tests (03 TD-2). No producer.** The type exists,
   is complete, and is unreachable.
4. **MD-04 — the hash join, serial and parallel together (§4.1), with the
   `hashsize` model (D-3).** The measurement slice. Its CPU, alloc and
   batch-count results decide 5-7.
5. **MD-05… — Tier A remainder, then Tier B**, one site per commit.
6. **MD-1x — D1 (conditional alignment)**, its own commit, before or after
   MD-03, never inside it (03 TD-4).
7. **MD-last — `spill.go`'s payload** (03 TD-5).

**MD-04 onward is BLOCKED by take3 13 §8.2**, which the earlier draft missed:

> **EX1 before EX3's geometry.** Batch counts, arena sizing, and spill thresholds
> are computed over narrowed widths. EX3-02/03/06 on pre-EX1 widths are
> **premature by rule**.

and 12 §12's preservation rule attached to the very row this bundle cites:
*"narrowing and footprint move together (13 §8: EX1 sizes the batching math EX3
implements)."* The earlier draft quoted the left half of that row (128 → 64) and
dropped the rule, which is the half that binds it.

MD-04 re-derives `EntryBytes`, `Choose`'s `bucketSize`, `RowSliceBytes`,
`MapSlotBytes` and `estimatedRowBytes` — **that is the batching geometry**.
Chained with §8.6 (P4-01 before EX1), the required order is:

    P4-01  →  EX1  →  (geometry)  →  MD-04 …

MD-01, MD-02, MD-03 and MD-1x carry no geometry and may proceed independently.

take3 §8.1 additionally requires EX0 first, and this bundle's gates depend on
EX0-02 (protocol), EX0-04 (per-operator harness) and EX0-05 (batch/width
counters) — none of which exist. 06 §4 records which of this bundle's four
headline numbers are therefore currently unmeasurable.

**EX2-01** (retention-boundary audit) shares MD-04's seam. Either it lands first
and MD-04 consumes its map, or MD-04 produces the audit for the sites it
touches — but **not both in flight independently**, because EX2's
clone-elimination items and MD's packing items would rewrite the same lines
toward different targets.

**On "independent and multiplicative."** EX1 reduces columns and this work
reduces bytes per column, so the arithmetic composes. But §0.3 shows the
magnitudes do not: narrowing owns the measured 8 → 1, and packing is worth ~5× of
a ~48× width gap. Both are needed for byte-parity; only one of them is the
answer to the Q9 residual, and it is not this one.

---

## 11. Prior art: what has been tried here, and how it failed

Omitting this section was the review's most serious finding about the bundle.
Three attempts bear directly on this design.

### 11.1 `Operator.Next` returning `TupleSlot` — reverted twice

- M0069-0001 Stage B (`3398d47a0`) reverted by **`336550ce0`**; Stage C reverted
  by **`41dd7154b`** (2026-05-08).
- M0071-0005 Stage B re-land (`08b1a5c06`) reverted by **`cf04bce20`**; Stage C
  by **`5d6961d0d`**.

Two recorded causes, both of which this design is exposed to:

1. **Performance**: pool `Get`/`Put` plus interface dispatch per row dominated on
   small-row queries — Q1 41.83 → 50.63 s (**+21 %**), Q11 2.96 → 4.89 s
   (**+65 %**) — attributed to *the consumer-side `slot.Row()` materialisation*.
2. **Silent wrong answers**: **Q12 rows 2 → 0, Q13 rows 35 → 2**, attributed to
   group-state corruption from slot-buffer reuse aliasing.

`PackedSlot.Row()` (§2.3) is precisely a consumer-side full materialisation,
reached from `slotToRow` (`slot.go:230`) and the 18 `cloneRowOwned` sites. And
`Q12=0 / Q13=2` is the signature this bundle uses as its spot-check tripwire
(06 §2) — **the bundle inherited the symptom from this history while omitting the
cause.**

The seam did eventually land (today's `Operator.Next` returns `TupleSlot`), so
this is not a proof of impossibility. It is a measured price for the
`Row()`-materialisation shape, and §2.3's aliasing contract plus 05 §4's "sort is
mechanical" both need to be read against it.

### 11.2 The `Datum` packed-layout flip — attempted and reverted

`docs/design/0050-0099/0074-0003-datum-packed-layout.md` and
`0075-0003-datum-packed-flip.md` are marked **DEFERRED M0076** after an attempt
on 2026-05-10 (`aafef4fd4`). The tight 5-query gate **passed** (Q12=2, Q13=35,
Q21=381, Q22=7, Q9=7); the 21-query sweep then showed Q10, Q11, Q12, Q15a, Q16,
Q20, Q21 returning **0 rows** and Q22 returning 210. Suspected root cause:
*arenaRegistry slot reuse* — a dropped operator arena's slot reassigned
round-robin to a later operator, so a retained Datum's `ArenaRef` pointed at
someone else's memory.

Two consequences:

1. 05 §1's column (A) is **not hypothetical**. It was attempted and reverted, and
   "declined" in take3 means "declined after a failure", which is stronger.
2. **That failure mode is exactly what D-5 option (a) reintroduces** — aliasing
   `Buf` into `PackedTuple.buf`. §7 defers (a) "with the GC arm as its gate". The
   GC arm is the **wrong gate**: this failure passed a five-query gate and needed
   a full sweep. §7 is corrected: **D-5(a)'s gate is a full-sweep values diff on
   both suites, not the GC arm.**

### 11.3 A fifth hash-join retention lane, missing from §4.1

Ledger row M0127 (composite multi-key lane) records: *"composite multi-column
lane does NOT batch (packed-byte maps, no PG-style hashvalue) — multi-key hash
join still unbounded … `encodeCompositeKey`: `hashKeyBytes` on packed bytes
build+probe."*

§4.1 names four hash maps and calls them the sibling set that must move together.
There is a **fifth** retention lane, it is **unbounded**, and it already stores
packed bytes. It belongs in Tier A. In a design whose central discipline is
`pattern_sibling_paths_must_agree`, missing a sibling in the inventory is the
error the discipline exists to prevent.

Also uncited and directly informative for D-3: ledger M0127-P5.7-a records that
**the spill wire format is already narrower than the in-memory format**, and that
the planner's `spillPages` over-states wide builds because of it. A narrower
packed form's planner consequences are therefore already partially known.

(End of file)
