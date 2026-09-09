# R28 — goopg materialises CTEs where PG inlines them (111 vs 68)

*2026-09-09. Started as "diagnose `outer-over-derived`" and found the
structural cause underneath it.*

## 1. The decline is a deliberate firewall, not a bug

`outer-over-derived` (3 of the 9 surviving declines) is guarded on
purpose, with its resume condition already written down:

> *"Derived inputs — CTE scans, FROM-subqueries, function scans — carry
> no statistics … Q78's outer problem costed Nested Loop 3.07 against
> Hash 3.09 with every path at rows=1 … the epsilon victory ran Nested
> Loop with a Join Filter over full multi-year CTE outputs (15 s Hash
> shape → 327 s timeout). Resume: lift when B-06 wires CTE-output
> stats."*

So unlike `outer-link-no-sjinfo` (which masked a real bug, K29), this
one is protecting against a genuine catastrophic misplan. It should not
be lifted as-is.

## 2. But the framing "wire CTE-output stats" is the wrong fix

PG's Q77 plan contains **no `CTE Scan` nodes at all.** PG 12+ inlines a
non-recursive CTE referenced once (`NOT MATERIALIZED` is the default),
so `ss`, `sr`, `cs`, `cr`, `ws`, `wr` become ordinary sub-selects and
their underlying tables enter the planner directly, with real
statistics.

goopg materialises them and plans a `CTE Scan` over synthesised rows
(12, 12, 144, 14, 60, 60 on Q77).

Corpus-wide, TPC-DS:

| | `CTE Scan` nodes |
|---|---|
| goopg | **111** |
| PG 18.3 | **68** |

A 43-node structural gap, and every one of those nodes is a derived
input PG does not have.

## 3. goopg has the effect, not the shape

`cte_inline_pushdown.go` is explicit about what it is:

> *"goopg's analogue of the composition PG 12+ applies to the same
> shape: `inline_cte` … turns a single-reference CTE into an ordinary
> sub-select, and subquery qual pushdown then moves an outer
> restriction below the sub-select's GROUP BY"*

`pushQualsThroughSingleRefCTEs` reproduces the **qual-pushdown half**.
It carries the restriction into the body — and leaves the `CTE Scan`
node standing. PG's `inline_cte` *replaces the reference*, so the node
ceases to exist.

That is the difference between matching PG's row counts and matching
PG's plan, and this goal needs the second.

## 4. Why this is bigger than the 3 declines it was found under

Every un-inlined CTE reference is a **table-less derived input**, and
that has three consequences the parity work keeps running into:

1. **The firewall** (§1) fires on it — 3 declines, and those queries
   then cannot converge at all (K27).
2. **Statistics.** A derived input's rows are synthesised; an inlined
   sub-select's come from the real tables. The goal's premise is "the
   same statistics" — this is a place goopg structurally cannot have
   them.
3. **Join order.** The search sees one opaque derived rel instead of
   the tables inside it, so orders PG can reach are unreachable —
   plausibly a contributor to `join-order`'s 95/99, though that is
   **not measured here and must not be assumed**.

## 5. What the next round must establish first

Do NOT start by writing an inliner. Measure:

1. **How many of goopg's 111 are single-reference?** PG only inlines
   those; a multi-reference CTE is materialised by PG too. The 43-node
   gap is an upper bound on what inlining could close, not a target.
2. **Does `pushQualsThroughSingleRefCTEs` already identify them?** It
   gates on refcount==1, so its own gate may be the census.
3. **What does PG's `inline_cte` decline** that goopg would also have
   to (`cte_inline_pushdown.go`'s header says it already tracks some of
   this — read it rather than re-deriving).

Then decide whether inlining is a plan rewrite goopg can make, given
that its CTE body Node is SHARED between references and the executor
materialises the first reference into `ctx.CTERowCache` — the header
warns that mutating a shared body filters every other reference twice.
Single-reference makes both channels vacuous, which is exactly why the
existing pass gates there.

## 6. Not claimed

- That inlining closes 43 nodes: only the single-reference subset is
  eligible, and that count is unmeasured.
- That it moves any match: `ROADMAP-to-all-match.md`'s conjunction rule
  applies.
- That the firewall can then be lifted: it would no longer fire on
  inlined CTEs, but FROM-subqueries and function scans are also derived
  inputs and are untouched by this.
