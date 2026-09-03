# 05 — Work estimate

**What this document is.** The size of the change proposed in 04, derived from
the tree rather than from intuition, with the method and its error bars stated so
a reader can disagree with the number instead of with the feeling.

**Every count below names the command that produced it.** Where grep and the
compiler disagree, both are reported.

---

## 1. The distinction that decides the number

There are two different changes people call "minimize Datum", and they differ by
an order of magnitude.

| | **(A) Re-lay-out `Datum`** | **(B) 04's design** |
|---|---|---|
| what changes | the `Datum` struct — `Kind`, `Int`, field layout, size | the *retention* format; `Datum` is byte-identical |
| who is affected | every site that reads `d.Kind` or `d.Int`, every `Datum{…}` literal | the sites that **hold** rows, and the slot type switches |
| reach-into sites | **~3,090** non-test (§3) | **~70** non-test (§2) |
| status | declined — take3 13 §10, and 04 §0.1 | proposed |

The repo's standing lesson is that *"grep oversizes a Go type change by 10× — 52
sites by grep vs 4 by the compiler"*. **That lesson does not transfer to (A)**,
and §3 shows why with numbers. It does apply to (B), and §2 gives the compiler-
checkable surface.

Confusing the two is the single most likely way to misprice this work. 04 does
**not** change `Datum`.

### 1.5 Three cheaper interventions, priced against this bundle

The review that produced this section asked what else buys the same thing for
less. Three candidates, all recorded elsewhere in the repo, none previously
compared here:

| intervention | evidence | est. size | est. value |
|---|---|---|---|
| **Delete the duplicate build map** | 02 §3: `lazyHash` **and** `lazyIntHash` are both maintained, so "peak build memory on the int-key path is ~2×". take2 07 §6 lists it separately | **one commit** | **~2× on the build side** |
| **Fix probe-seam re-materialisation** | take2 07 §6: "the hash cascade re-materialises its probe input at every level, twice, on both execution paths; the pooled row is never released on the legacy path… ~18 M pool round-trips and ~2×2.3 GB of `Datum` traffic on a Q9-class query" | bounded, unscoped | large, same query |
| **The 24 B pointer-free `Datum`** | `docs/design/perf-optimize/02-datum-pointer-free.md` targets **24 B (2×)**, and is partially landed (Kind→1 byte, `Big` removed, `ArenaID` added). What remains is `Buf []byte`, which §3 shows is already hidden behind ~43 non-test references | small, by §3's own numbers | **2×**, not the ~33 % 04 §0.1 prices |
| **this bundle** | 04 §0.3 | ~1,750–3,050 LOC, 14 commits | ~5× of a ~48× width gap |

**04 §0.1's dismissal of shrinking is aimed at the weakest variant.** It prices
an `unsafe` union at ~32 B / ~33 % and declines it. The repo's own design targets
24 B — a 2× — and 04 §0.1 does not engage with it. That does not make shrinking
right; `Datum` re-layout was attempted and reverted (04 §11.2). It does mean the
comparison in 04 §0.1 is not the comparison that matters, and this table is where
it belongs.

Two further observations from the same review:

- **The premise lives entirely in the `work_mem` 4 MB regime.** At goopg's
  shipped 512 MB × 2, the FINDING's own table reads NBatch = 1 in every row but
  one. Nothing establishes that goopg needs PostgreSQL's `work_mem` default for
  anything except GUC-default parity — and P2-02b (adopting it) is unlanded and
  costs +23.1 %.
- Deleting the duplicate build map is **not** in 04 §4.1. That section instead
  lists both maps as two things to *convert*, doubling this bundle's own Tier A
  work.

---

## 2. The surface 04 actually touches

### 2.1 Retention sites — 48 struct fields

```
git ls-files 'internal/*.go' | grep -v '_test\.go$' \
 | xargs grep -nE '^\s+\w+\s+(\[\]\[\]Row|\[\]Row|map\[[^]]+\]\[\]Row|\[\]executor\.Row|map\[[^]]+\]\[\]executor\.Row)\b' \
 | wc -l
→ 48
```

Full triage in 04 §4.1. By tier:

| tier | fields | what they are |
|---|---:|---|
| A | 6 | hash join (serial ×2, parallel ×2), sort, materialize — `work_mem`-bounded, planner-modelled |
| B | ~28 | window, memoize, CTE caches, recursive worktable, lateral CTE, outer-join sweep, RETURNING ×4, Gather ×3, `conn_tx.Rows`, and ~13 small `rows []Row` in distinct/setop/SRF/catalog-view operators |
| C | 6 | aggregate group representative + four `[][]Datum` accumulators (accumulators stay Datums — 04 §4.1) |
| D | 1 | `spill.go`'s in-memory drain arm |

Tier A is 6 fields in 4 files. Tier B's ~13 catalog/SRF `rows []Row` fields are
mechanical and low-value (they hold tens of rows); the eight that matter are
window, memoize, the two CTE caches, the worktable pair, the sweep pair,
RETURNING, and `conn_tx.Rows`.

### 2.2 The ownership boundary — 19 call sites, 14 files

```
grep -rn "cloneRowOwned(" --include=*.go internal/ | grep -v _test.go | wc -l
→ 19   (14 files)
```

04 §4: packing happens where `cloneRowOwned` happens. This is the *whole* seam,
and it is already the arena-promotion point (`MaterializeArena`,
`datum.go:433-478`). The files:

`slot.go`, `datum.go`, `opnode.go`, `executor.go`, `spill.go`,
`operators_storage.go`, `operators_join_agg.go`, `operators_material.go`,
`operators_bitmap.go`, `operators_lockrows.go`, `operators_ddl.go`,
`join_lateral_stream.go`, `join_nl_stream.go`, `parallel_runtime.go`.

### 2.3 The type switches — 6, all mandatory

04 §9.1. Six `switch`/assertion sites over `TupleSlot`/`SlotView` need a
`*PackedSlot` arm. Five of the six fail **silently** without it, and the bug has
been committed once already (`slot.go:247-252`).

### 2.4 New code

| unit | file | est. LOC |
|---|---|---:|
| `TupleDesc` + descriptor fields on `colTypeInfo` (04 D-1, 03 TD-1) | `coltypeinfo.go` (62 LOC today) + new | 150–250 |
| `PackedTuple` — MinimalTuple header build/parse, hash prefix | new file | 150–250 |
| `PackedSlot` — `TupleSlot` impl, `(nvalid, off)` watermark | `slot.go` (259 LOC today) + new | 150–250 |
| `attcacheoff` fast path (04 D-4) | `codec.go` / `coltypeinfo.go` | 80–150 |
| `hashsize` re-derivation (04 D-3) | `hashsize.go` (335 LOC today) | 60–120 |
| conditional alignment, both directions (03 D1) | `codec.go` + `catalog/codec.go` pattern | 80–150 |

**Total new: ~700–1,200 LOC.** Against `internal/executor`'s 146,242 source
lines, this is not a large body of new code. The cost is in conversion and in
verification, not in authorship.

### 2.5 The bottom line for (B)

**~70 non-test sites** — 48 retention fields (of which ~20 are worth converting),
18 clone-boundary calls plus the definition, 6 type switches — and ~700–1,200 LOC
of new code.

**That number is a declaration census, not a change surface, and the difference
is large.** It counts *where a field is declared*, not where it is used.
Converting one field means rewriting every use of it: `lazyHash` has 51 non-test
reference lines, `lazyIntHash` 28, `sortOp.rows` ~72 in `operators.go`. **Tier A
alone — six "sites" — is on the order of 150+ reference lines before any new code
is written.** §1's invocation of the "grep oversizes 10×" lesson does not rescue
this: that lesson is about types behind a facade, and a `[]Row` field has none.

Treat ~70 as the count of *decisions* and §4's LOC bands as the count of *edits*;
they are different quantities and only the second predicts effort.

---

## 3. Why (A) is not that number — the measurement, since it is often assumed

Field references, obtained with `gopls references` on each declaration line
(type-precise, not textual):

| field | non-test | test |
|---|---:|---:|
| `.Kind` | **859** | 984 |
| `.Int` | **775** | 1,321 |
| `.Scale` | 92 | 129 |
| `.Buf` | 24 | 40 |
| `.ArenaID` | 19 | 14 |
| `.TimeSub` | 19 | 17 |
| `.Flags` | 13 | 6 |
| `.Hi` | 2 | 1 |
| **total** | **1,803** | 2,512 |

Construction (`grep -oE`, non-test): `Datum{` = 1,028, of which 14 are container
literals → **~1,014 genuine struct literals**; `Row{` = 154; `make(Row,` = 115;
`make([]Datum,` = 62; `[]Datum{` = 12.

Row indexing, approximated by `\b[a-zA-Z]*[Rr]ow\[` (see §5 for this pattern's
two error modes): ~274 non-test in executor + initdb.

**Category (b), reaching into the representation: ~3,090 non-test sites.**

**The grep-oversizes lesson checked directly, and it cuts both ways:**

- `.Kind`: grep `-o '\.Kind\b'` = 2,195 non-test; gopls = 859. Grep **oversizes
  2.6×** — other types have `Kind` fields.
- `.Buf`: grep `-o '\.Buf\b'` = 15 non-test; gopls = 24. Grep **undersizes 1.6×**
  — `Buf: x` inside a composite literal is a reference with no leading dot.

So the naive grep total is wrong in both directions, and the compiler-truth
figure is still ~3,090 — because `Kind` and `Int` are **raw public fields with no
accessor in front of them**. The 4-vs-52 pattern holds for types with a facade.
`Datum` has a facade for *construction* (~2,093 constructor calls vs ~1,014 raw
literals, so ~67 % mediated) and for *payload reads* (~1,745 accessor calls), but
**none at all for the discriminator**. There is no `d.Is(KindInt)` and no
`d.Type()`; every `switch d.Kind` reaches straight into the struct. There is
`NumericMantissaValue()` but no `Int64Value()`.

Two further facts worth carrying:

- **The work would be concentrated.** `expr.go` holds 566 of the ~1,014 non-test
  struct literals (55 %); `expr.go` + `plpgsql_runtime.go` hold 74 %.
- **`.Buf` and `.ArenaID` are already well-hidden** — 43 non-test references
  between them, behind `StringValue`/`BytesValue`/`MaterializeArena`. A change
  confined to *how bytes are stored* touches ~43 sites. A change to the
  discriminator or the inline word touches ~1,600.

That asymmetry is the quantitative reason 04 leaves `Datum` alone.

---

## 4. Slice-by-slice estimate

"Judgement" means the work is deciding what is correct; "mechanical" means the
decision is made and the edit follows. Bands are honest ranges, not commitments.

| id | slice | files | new/changed LOC | mechanical vs judgement | risk |
|---|---|---|---:|---|---|
| MD-01 | `TupleDesc` + descriptor on `colTypeInfo`; one shared `pg_type.dat` transcription with `userTypeAttrsForOID` | 3 | 150–250 | mostly mechanical; **judgement** in not creating a second transcription (03 §5) | low |
| MD-02 | R-1 audit: derived-column type fidelity, with counts, on both suites | 0 (doc) | — | pure judgement | — |
| MD-03 | `PackedTuple` + `PackedSlot` + 6 type-switch arms + `attcacheoff` + exhaustiveness tests. **No producer.** | 6–8 | 400–650 | judgement in the watermark invariants; mechanical in the switch arms | **medium** — R-0 lives here, and it is bought off entirely by tests in this slice |
| MD-04 | Hash join, serial + parallel together, with the `hashsize` re-derivation | 3 | 250–400 | **judgement** — this is the measurement slice | **high** — R-3, R-6; the planner moves |
| MD-05 | Sort | 1 | **re-price** | **judgement** — see 04 §9.12 (R-11) | high — a `PackedSlot` has one scratch `Row` and a comparator needs two operands' keys; needs a hoisted-key design (PG's `SortTuple.datum1`, or retaining `sortKeyVals` alongside the packed tuple). Plus `operators.go:898-900`'s comparator warning |
| MD-06 | Materialize | 1 | 60–120 | mechanical | low |
| MD-07 | Window (Tier B, unbounded, no spill) | 1 | 80–150 | mechanical | low |
| MD-08 | Memoize + CTE caches + worktable + lateral CTE | 4 | 150–250 | mechanical | medium — recursive-CTE lifetimes |
| MD-09 | Gather + Gather Merge | 2 | 100–180 | mechanical | medium — R-0 in a worker is a wrong answer |
| MD-10 | RETURNING ×4 + `conn_tx.Rows` | 5 | 120–200 | mechanical | low |
| MD-11 | Outer-join sweep, aggregate group representative | 1 | 80–150 | judgement — Tier C boundary (04 §4.1) | medium |
| MD-12 | Small `rows []Row` sites (~13, distinct/setop/SRF/catalog views) | ~13 | 100–200 | mechanical, low value | low |
| MD-1x | Conditional alignment, both directions (03 D1) | 2 | 80–150 | judgement — on-disk format change | **high** — 06 §5 goldens |
| MD-last | `spill.go` payload conversion | 1 | 100–200 | mechanical; **judgement** in retiring the private codec's tests correctly | medium |

**Totals: ~1,750–3,050 LOC across ~35 files, in 14 commits.** Tests are extra and
are not a rounding error — 06 requires a values-diff run per slice and a
sibling-parity test per pair; in this repo, test LOC for executor work runs close
to source LOC (`internal/executor` is 146 k source / 129 k test).

---

## 5. Method, and where it is weak

**What is compiler-grade.** The 48 retention fields, the 19 clone-boundary calls,
the 6 type switches, and the field-reference counts in §3 (gopls, not grep).

**What is approximate.**

- *Row indexing (§3).* The pattern `\b[a-zA-Z]*[Rr]ow\[` **misses** Rows held in
  variables named `r`, `out`, `dst`, `lr`, `rr`, `left`, `right` — and `r[i]` is
  unusable as a pattern because it matches every slice named `r` in the repo. It
  also **catches** `rows[i]`, which is indexing a `[]Row` by row number, not
  reaching into a representation. Treat ~274 as a floor. This only affects (A);
  04 does not need the number.
- *LOC bands.* Estimated from the size and shape of the files being changed, not
  from a prototype.
- *Tier B's value.* Unbounded buffers have no `work_mem` model, so the benefit is
  reduced OOM exposure rather than a measurable batch count (04 §4.2). MD-07
  through MD-12 are priced as effort, not as expected gain.

**Search universe.** `git ls-files`, excluding `postgres/` and `third-party/`.
Using the filesystem instead would have quadrupled every number: there are stale
full-tree copies at `.claude/waitevent-impl/`, `tmp/ab-head/` and
`worktrees/cte-left-cross-join-fix/`, each containing its own
`internal/executor/datum.go`.

**The rule this document imposes on its own successor.** MD-01 is the first slice
with code. When it lands, **re-derive its site count from the compiler** — build,
read the errors — and record the delta against this document in the TODO's
progress log. If §2's ~70 is wrong by more than 2×, 04's sequencing is re-opened
before MD-03 starts.

---

## 6. The stopping rule

MD-04 is a measurement (04 §10) and it can return a negative result. It must be
allowed to.

**MD-04 reports four numbers** (06 §4): batch count at PostgreSQL's `work_mem`,
retained bytes, wall time, and allocation count — with the plan-shape pin held.

**Three of the four are not currently measurable, and one is unfalsifiable by
construction.** This is the rule's biggest weakness and it must be fixed before
MD-04, not discovered during it:

| number | instrument | state |
|---|---|---|
| batch count | `Batches:` renders only through `EXPLAIN ANALYZE` (`operators_explain.go:210-227`); `cmd/tpch-runner -explain` issues plain `EXPLAIN`, `plan-snapshot` is EXPLAIN-only and its default `structural` mode strips cost/rows/width | **hand run.** And the *parallel* half cannot produce it at all until take3 EX0-03: worker hash counters die with the worker (take3 11 §10) |
| retained bytes | none — the repo has ~13 per-function `b.ReportAllocs()` microbenchmarks and **no query-level `inuse_space` harness** | **absent** (take3 EX0-02/EX0-04) |
| wall time | fine, but 06 §4's noise band is **±17 %**, which swallows most plausible MD-04 outcomes | this row will usually read "neutral" |
| allocation count | **unchanged by construction**: `[]Row` retention is one `[]Datum` allocation per row and `PackedTuple` is one `[]byte` allocation per row (04 §2.1). The repo's standing finding is that allocator CPU tracks alloc *count*, not bytes | **satisfiable by a change that adds pure encode/decode CPU for zero allocator benefit.** "Allocs neutral-or-better → continue" is therefore not a real check; retained *bytes* is |

**And the rule fires too late.** It triggers only after MD-01 + MD-02 + MD-03 —
600–900 LOC by §4 — are sunk.

**MD-03.5 (added):** before MD-04, pack and unpack the Q9 build side behind a
flag, measure the four numbers, and **throw the code away**. It tests the same
hypothesis at a fraction of the cost, and it can be built on a branch without
the descriptor work (a hardcoded descriptor for one query's build side is
acceptable in a throwaway). `TODO.md` carries it.

| outcome | action |
|---|---|
| batches down, time neutral-or-better, allocs neutral-or-better | continue to MD-05 |
| batches down, **time up** by more than the noise band | R-3 is real. Stop. Record which query, which width. Re-scope to wide rows only, or to Tier B (where the constraint is memory, not time), and **rewrite 04 §4.1's triage in public** before continuing |
| batches unchanged | the model in D-3 is wrong. Fix the model before touching another site |
| values differ, either suite | revert. This is not a tuning failure |

And the rule 04 §0.2 exists to enforce: **stopping after MD-04 leaves the
codebase with two retention formats** and the sibling hazard that implies (R-4).
If the measurement says stop, the correct action is to *revert MD-04*, not to
keep it and stop — MD-01/02/03 are additive and can stay.

---

## 7. What this estimate does not cover

- **Take3's EX phases.** EX1 (narrowing) is independent and multiplicative with
  this work; EX2 (clone elimination) shares the seam and must be sequenced
  (04 §10). Neither is priced here.
- **PG-format TOAST pointers** (03 D2) — a storage project, out of scope.
- **Any `Datum` re-layout** (§1 column A) — declined.
- **`attcacheoff`'s benefit.** It is priced as required work (04 D-4) because
  without it the deform cost is a function of column position; the *win* from it
  is not claimed here and is measured in 06 §4.

(End of file)
