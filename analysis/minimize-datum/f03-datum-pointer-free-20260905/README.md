# F-03 — 24 B pointer-free `Datum`: assessed, DROPPED (rule 3)

Date: 2026-09-05. Item: `docs/design/not_ralph/minimize_datum/TODO_ALL.md`
F-03. Evidence base: `docs/design/perf-optimize/02-datum-pointer-free.md`,
05 §3, 04 §11.2. Dropped under TODO_ALL §8 **rule 3 (infeasible in goopg's
architecture)**, with the dominance argument under rule 1 recorded as
supporting rather than load-bearing. Ledger row `take3-F-03-dropped`.

The item's own note says "treat as hostile until the measurement says
otherwise". This is that assessment. It does not report a timing A/B,
because the blocker is reached before a timing question arises.

## 1. The arithmetic is real

`unsafe.Sizeof(Datum{})` measured at HEAD: **48 bytes**.

```
Kind     1 B @ 0     Flags   1 B @ 1     ArenaID 2 B @ 2
Scale    2 B @ 4     TimeSub 1 B @ 6     _pad0   1 B @ 7
Int      8 B @ 8     Buf    24 B @ 16    Hi      8 B @ 40
```

Removing `Buf` — the 24-byte slice header — leaves exactly 24 bytes. The
"2×" claim is arithmetically coherent, not marketing, and F-03 is right
that 04 §0.1's dismissal priced a weaker ~32 B variant.

The mechanical surface also looks small, which is what makes the item
attractive: `.Buf` has **18 non-test references** at HEAD (05 §3 measured
24 by gopls, 43 including the accessors that front it), because the field
already hides behind `StringValue` / `BytesValue` / `MaterializeArena`.

## 2. Why it is not a layout change

`Buf` is not merely *where bytes live*. It is the **escape hatch that makes
retention safe**, and it is load-bearing at exactly the sites this whole
bundle is about.

`Datum.MaterializeArena` (`internal/executor/datum.go`) exists to DETACH an
arena-backed value onto a fresh `Buf` at every retention boundary:

```go
src := ctx.Bytes(offset, length)
buf := make([]byte, length)
copy(buf, src)
return Datum{Kind: d.Kind, Buf: buf}
```

and `cloneRowOwned` calls it for every column of every retained row,
because a producer arena **resets per page** while a hash-join build row,
a sort run or a memoize entry outlives that reset.

A pointer-free `Datum` has nowhere to detach TO. Every retained string or
bytes value would have to stay arena-referenced for its whole lifetime,
which means either (a) retained values live in the permanent arena, which
never resets — converting a bounded, GC-managed lifetime into an unbounded
one, precisely the liability rule 3 names — or (b) producer arenas stop
resetting while any retained reference exists, which is the same
unboundedness wearing a different hat.

## 3. This exact failure has already been paid for

`04 §11.2` records the attempt: commit `aafef4fd4`, 2026-05-10, designs
`0074-0003-datum-packed-layout.md` / `0075-0003-datum-packed-flip.md`, both
marked DEFERRED.

- The tight five-query gate **passed** (Q12=2, Q13=35, Q21=381, Q22=7,
  Q9=7).
- The 21-query sweep then showed **seven queries returning 0 rows**
  (Q10, Q11, Q12, Q15a, Q16, Q20, Q21) and Q22 returning 210.
- Root cause: arena slot reuse — a dropped operator arena's registry slot
  reassigned round-robin, so a retained `Datum`'s arena reference pointed
  at another operator's memory.

That is the wrong-answer class, reached through the same door F-03 must
walk through, and it passed a narrow gate before a full sweep caught it.
"Declined" here means declined **after a failure**, which is a stronger
statement than declined on estimate.

## 4. And the win is dominated on the same sites

Even setting §2 and §3 aside, F-03 competes with the MD packed-row work for
the same bytes:

| | factor on retained bytes/row | applies to |
|---|---|---|
| F-03 (48 B → 24 B `Datum`) | 2× on the Datum array | every Datum, retained or transient |
| MD-04 / D-05 (packed rows) | 1098 B → PG's 23 B on the measured Q9 witness | retained rows |

D-02 returned **PROCEED** on 2026-09-05, so the packed-row path is
sanctioned and open. On the retention sites both target, MD's factor is an
order of magnitude larger, and it achieves it **without** removing the
detach mechanism. F-03's residual benefit would be on transient Datums —
where the arena already avoids the allocation F-03 would save.

Doing both would also mean paying §3's risk to make MD's own payload
smaller by a factor MD has already dwarfed.

## 5. Verdict

**DROP under rule 3.** The architectural fact, stated as the rule requires:
`Datum.Buf` is the detach target that gives retained values a lifetime
independent of a resettable producer arena; a pointer-free `Datum` removes
that target and leaves only unbounded alternatives, and the one previous
attempt produced silent wrong answers on seven queries from exactly this
mechanism.

**Resume conditions** (all three, not any one):

1. Arena lifetimes become explicit and non-reusing — a retained reference
   provably pins its arena, with slot reuse impossible rather than
   improbable. That is a memory-manager change, not a `Datum` change.
2. MD-04 / D-05 has landed and been measured, so the residual win of a
   smaller `Datum` on the retention path can be priced against what packing
   already took.
3. The gate is a **full 21-query values sweep on both suites**, never a
   narrow query set — 04 §11.2 is explicit that the five-query gate passed
   while seven queries were broken.
