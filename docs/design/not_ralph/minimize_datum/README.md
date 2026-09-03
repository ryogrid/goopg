## Status: NOT APPROVED TO START. Read this section first.

An adversarial review on 2026-09-03 found three blockers in the bundle's own
framing. They are recorded in `REVIEW.md` and fixed in the text below, but they
change the bundle's conclusion, so they are stated up front rather than buried.

**1. This bundle does not have a licence from take3; it is asking for one.**
An earlier draft claimed take3 `13-executor-target-design.md` §10 grants a
re-proposal path. It does not. §10 reads: *"`Datum` re-layout below 48 B **and
JIT** are declined in §0.1 with reasons; re-proposing **either** requires new
measurement, not new argument."* "Either" is a two-element list —
`Datum` re-layout, and JIT. §0.1's **first** clause, *"a new row
representation"*, has **no stated re-proposal path at all**, and
`PackedTuple`/`PackedSlot` is squarely a new row representation. §0.1's
parenthetical also shows take3 declined it *while already holding* the
measurement this bundle cites: *"(12 §12: narrowing without shrink still halved
batches; shrink without narrowing is not proposed)"* — 12 §12 row 1 **is** the
FINDING. The measurement is not new; it is the evidence of the refusal.

So this bundle must be argued on merits and accepted by whoever owns take3 13.
It may not proceed on a claimed permission.

**2. The premise was modelled, and the measured answer is different — and
smaller.** See §"What this is actually worth" below.

**3. Sequencing violates take3 13 §8.2.** See §"Sequencing" below.

---

# minimize_datum — packed in-memory tuples with lazy deform

**What this bundle proposes.** Retained rows become packed PostgreSQL-format
tuples. Only the row currently in flight is deformed — lazily, up to the highest
attribute asked for — into a per-slot scratch `Datum` array. This is
PostgreSQL's slot duality, reached with machinery goopg already owns.

**What it does not propose.** `Datum` does not shrink. It stays exactly 48 bytes
and stays the working format; what changes is its *role*, from storage format
multiplied by row count to per-operator working buffer one row deep.

---

## What this is actually worth

The bundle originally rested on `FINDING-p401-alone-is-not-enough.md`: at
PostgreSQL's `work_mem`, narrowing Q9's build side moves the hash join 128 → 64
batches, "not to 1". Two problems with using that as the premise:

- **It was modelled, not measured.** The FINDING states its own method: it *fed*
  `hashsize.Choose` hand-derived column counts. No query ran. The formula it
  evaluated — `EntryBytes = ncols×48 + 24 + avgVar` — is the formula MD-04 (04
  D-3) proposes to declare wrong and replace. The evidence and the first
  deliverable are the same artifact pointed in opposite directions.
- **The FINDING's own caveat was answered, against this bundle.** It says:
  *"Once `neededCols` is consulted at the join levels, re-run this table with the
  collector's real answer before trusting the 2–4× figure."* take3
  `09-verification-and-acceptance.md:48,190` records that answer — Q9 P4-01
  witness, **`Batches:` 8 → 1**, narrowed width **≈100, not 6**. Real narrowing
  reaches one batch.

The measured evidence is `.ralph/deferral_ledger.md:2036`
(`take2-executor-residual`, 2026-09-03, *"measured at equal cardinality for the
first time"*): TPC-H Q9, same `work_mem`, same row counts — goopg widths
**1098/1642/2090/3164 B** against PostgreSQL's **23/32/54/81 B**, peak hash
memory 97 MB vs 38 MB, **8 batches vs 1**, **63.8 s vs 6.2 s**. Its disposition
is explicit: *"This is P4-01's PathTarget/projection. **Resume there.**"*

**The arithmetic that follows is the honest case for this bundle, and it is a
weaker case than the FINDING implies.** 1098 B / 48 ≈ 22 columns. Packing 22
columns of TPC-H data yields roughly 150–250 B — not PostgreSQL's 23 B. So:

| lever | 1098 B → | closes |
|---|---|---|
| packing alone (this bundle) | ~150–250 B | ~5× of a ~48× gap |
| narrowing alone (P4-01 / EX1) | ~100-wide row ≈ 5 KB→ far fewer columns | the measured 8 → 1 batches |
| both | approaches PG's 23 B | the remainder |

04 §10 calls the two "independent and multiplicative". That is true of the
arithmetic and misleading about the magnitudes: **narrowing is the larger term
and owns the measured gap; packing is the second lever.** This bundle is worth
doing to reach byte-parity with PostgreSQL, not as the way to close the Q9
residual.

Three cheaper interventions are compared against it in 05 §1.5, and at least one
— deleting the duplicate build map — is a plausible 2× for a single commit.

---

## Sequencing

take3 13 §8.2: *"**EX1 before EX3's geometry.** Batch counts, arena sizing, and
spill thresholds are computed over narrowed widths. EX3-02/03/06 on pre-EX1
widths are **premature by rule**."* And 12 §12's preservation rule for the very
row this bundle cites: *"narrowing and footprint move together (13 §8: EX1 sizes
the batching math EX3 implements)."*

MD-04 re-derives `hashsize.EntryBytes`, `Choose`'s `bucketSize`,
`RowSliceBytes`, `MapSlotBytes` and `estimatedRowBytes`. **That is the batching
geometry.** Chained with §8.6 (P4-01 before EX1), the required order is:

    P4-01 → EX1 → (geometry) → MD-04

MD-01, MD-02, MD-03 and MD-1x are geometry-free and may proceed independently.
MD-04 onward is **blocked**, and 04 §10 and `TODO.md` now say so. take3 §8.1
additionally puts EX0 first, and this bundle's gates depend on EX0-02/04/05
(protocol, per-operator harness, batch counters) — none of which exist (06 §4).

---

## Doc map

| file | read it for |
|---|---|
| `01-pg-tuple-representation.md` | the oracle: `HeapTupleHeaderData`, `MinimalTupleData`, `heap_fill_tuple`'s per-attribute rules, `slot_deform_heap_tuple` and the `tts_nvalid` watermark, the four `TupleTableSlotOps`, where PostgreSQL actually stores tuples, what its 8-byte `Datum` is |
| `02-goopg-current-representation.md` | what goopg has today: `Datum`'s 48 B and the arena, the slot layer, the 48 retention sites, **the PG-format codec that already exists**, the private spill encoding, the type-assertion surface that makes wrappers unsafe, and the type-metadata gap |
| `03-byte-format-fidelity.md` | the second axis: what is already byte-exact, the five divergences, the target header, and why the byte work is separable from the memory work |
| `04-target-design.md` | the design: `PackedTuple`/`PackedSlot`, where packing happens, the `TupleDesc` crux, the planner lockstep, the risk register |
| `05-work-estimate.md` | 作業量: ~70 sites and ~700–1,200 new LOC across 14 commits, the method, and the stopping rule |
| `06-verification.md` | gates per slice, the measurement protocol, acceptance, the sibling pairs |
| `TODO.md` | the checklist — one checkbox ≈ one commit |
| `REVIEW.md` | reviewer findings and their resolutions |

**Suggested reading order.** For the decision: this file → 05 §1 and §6 → 04 §0
and §10. For implementation: 02 → 04 → 06 → `TODO.md`, with 01 and 03 as
reference.

---

## Ground rules

1. **`./postgres/` is read-only.** Never modified, vendored, or imported at
   runtime.
2. **Values are the gate, not row counts.** Three of the bugs this design must
   not repeat produced correct counts and wrong values (06 §1).
3. **One variable per commit** — never the retention format and the byte layout
   together; never storage without its `hashsize` model; never the serial hash
   build without its parallel sibling.
4. **Plan-shape pin on every commit.** `hashsize` feeds the planner, so this
   bundle can move plans by construction.
5. **`Datum` stays 48 bytes.** This is the bundle's own falsifiable claim: that
   it did not become the change it declined.
6. **No time target in acceptance.** The thesis is about retained bytes per row.
   Whether that converts to wall time depends on the query, and 05 §6 allows the
   answer to be "no" for narrow rows.

---

## Relationship to the other bundles

- **take3 `TODO_EXECUTOR.md`** (entirely unstarted). EX1 (narrowing) is
  independent of and **multiplicative** with this work — EX1 reduces columns,
  this reduces bytes per column, and the FINDING's arithmetic is the reason both
  are needed. EX2-01 (retention-boundary audit) shares this bundle's seam and
  must be sequenced against it (04 §10): either order works, both in flight
  independently does not.
- **take2 `07-gap-analysis.md` §6** lists the 48-byte `Datum` as an executor
  residual, out of scope, with a pointer. This bundle is what that pointer
  points at.
- **`planner_refactor_take2/impl/FINDING-p401-alone-is-not-enough.md`** is the
  measurement. Read it before disagreeing with the premise.

(End of file)
