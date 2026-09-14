# Milestone 0143 — Engine correctness carry-overs from the parity programme

**Status:** planned
**Filed:** 2026-09-14 (user directive, from
`METHODOLOGY3/04-forward-plan.md` §3 "Continuous — engine correctness, not
gated on anything")
**Priority placement:** in the plan-parity group. **Not gated on anything** —
these may be selected whenever a higher item is blocked, and no parity decision
should hold them up. See the `## Current Priority` banner.
**Reference plan:** `.ralph/fix_plan.md` (M0143 section)
**Harness:** `AGENT.md` §"Plan-parity harness — applies ONLY to M0137–M0143"
**Prerequisites:** none.

## Why these are grouped here

These are **real engine defects the plan-parity programme discovered as a side
effect**, not parity work. `METHODOLOGY3/01` §F20 makes the argument for
treating them as a first-class milestone rather than footnotes: **two genuine
wrong-rows bugs were found by parity work, not by the correctness gates** — the
Q13 33-vs-34 Memoize-over-RIGHT-JOIN bug (R63/R64, latent since 2026-09-03, and
*that round's gate should have caught it*) and the Limit-below-Unique
truncation (R83, masked on current data only because `78 < 100`).

The items below are the ones still open.

## Scope

**N3 — no in-process test crosses a DATABASE boundary. (Highest leverage.)**
`CREATE DATABASE` is a dispatch-layer statement the in-process parser rejects,
so `pgConstraintTableRel`'s per-DB branch and the reload's `ListDatabases` loop
— the exact paths TPC-H rides — have **manual psql evidence only**. This is
*why* two per-DB defects were found in two consecutive rounds. Closing it makes
the rest of this list findable by the suite instead of by a parity round.

**N1 / N2 — `ALTER TABLE … DROP CONSTRAINT` on an FK reports success and does
nothing.** `InMemory.DropForeignKeyConstraint` hardcodes `DefaultDBOid`
(`catalog.go:22241`) and `execAlterTableDropConstraint` discards the result
(`operators_ddl.go:13303`). Pre-existing — and **R126 made it worse in effect**,
because such an FK now also survives restarts. `HasPrimaryKey`
(`catalog.go:22261`) has the same hardcoded shape. Six `deleteCatalogRowsForOID`
sites were filed for the same check (N11) and never confirmed.

**N4 — `pg_constraint` returns 0 rows of any contype after a restart**,
including the `'p'`/`'u'` rows synthesised from indexes that demonstrably
survive. A second, independent reload gap; R126 explicitly did not touch it.

**N5 — `PhysicalTypeIsVarlena` has no `IsArray` arm**
(`physical_align.go:85-107`) — latent for ordinary user `int4[]` columns, not
just catalogs.

**C1 — `ParamRef` LIMIT + DISTINCT still returns wrong rows.** R83 fixed the
`IntegerConst` case and pinned it; the `ParamRef` allowlist was **deliberately
not extended**. Fail-closed with zero corpus impact today — which is exactly why
it will stay invisible until someone writes the case.

**N30 — `internal/parser` fails 60 tests**, pre-existing, verified unrelated to
R126, and **unowned**.

## Per-task discipline (READ FIRST — binding)

1. **Design note when the task is selected** for any non-trivial fix, indexed
   in `docs/design/README.md` in the same commit. A one-line catalog fix with a
   regression test does not need one; a reload-path change does.
2. **Each fix lands with a test that fails before it.** Every item here exists
   because nothing in the suite crossed the boundary that would have caught it —
   R126's own diagnosis was *"nothing in the suite crossed an `Open -> Close ->
   Open` boundary with an FK declared."*
3. **These are not parity tasks.** No category movement is expected or reported;
   the values and unit gates are the bar.

## Definition of Done

- An in-process test exists that crosses a DATABASE boundary and exercises the
  per-DB catalog paths TPC-H rides.
- FK `DROP CONSTRAINT` and `HasPrimaryKey` operate on the addressed database;
  the `deleteCatalogRowsForOID` sites are confirmed or fixed.
- `pg_constraint` returns its rows after a restart, for every contype it
  synthesises.
- `PhysicalTypeIsVarlena` handles arrays; a user `int4[]` column round-trips.
- `ParamRef` LIMIT + DISTINCT returns correct rows, pinned by a synthetic case.
- `internal/parser`'s failing tests are either fixed or triaged into filed,
  owned tasks — "unowned" is not an end state.
