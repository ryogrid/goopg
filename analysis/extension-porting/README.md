# goopg contrib Extension Porting — Feasibility Analysis

Date: 2026-05-27

This bundle analyzes whether and how goopg (a from-scratch Go reimplementation of
PostgreSQL) could run PostgreSQL `contrib` extensions with **unmodified source
artifacts**, focusing on one concrete line of attack:

> **cxgo (C→Go transpile) + custom goopg SDK headers + hand-written Go shim +
> boundary marshaling**, scoped to "leaf" extensions, gated by a static
> **symbol-footprint classifier**.

It also specifies the classifier as an implementable design skeleton, with
cxgo-feasibility evaluation built in.

## Bottom line

- Hosting **unmodified compiled C** (`.so` loading, or compiling extension C
  against a goopg-exported backend C ABI) is impractical and conflicts with
  goopg's "from-scratch Go / no-CGo" premise.
- Source transformation — including cxgo — changes the *character* of the hard
  part (it removes the CGo-specific blockers) but **not its size**. Porting cost
  is proportional to the **backend API surface** an extension transitively
  touches; no transformation removes that surface, it only relocates the binding.
- The viable line is **cxgo+shim for *leaf* extensions** (those whose entire
  backend footprint is memory/varlena/error symbols), with **native Go
  reimplementation** preferred for small/clean leaves and **defer** for
  non-leaf extensions (anything touching SPI / syscache / the fmgr call graph /
  custom-GUC / background-worker / hook infrastructure).
- The **framework cost** (a `CREATE EXTENSION` install runtime, a binding-keyed
  fmgr, an extensible type/operator system, pluggable index AMs, hooks, FDW) is
  **separate from the porting method and largely unavoidable** — cxgo+shim only
  addresses the function-implementation slice of leaf extensions.
- The recommended first step is the **classifier (P0)**: it turns the
  leaf/non-leaf tiering from estimate into measurement over all 48 installable
  contrib extensions and produces the actionable porting queue.

> Scope note: `.ralph/specs/GOAL_AND_REQUIREMENTS.md` currently lists extensions
> as **out of scope**. This bundle is exploratory; it does not change that
> boundary. Any implementation would require a `docs/design/` document first.

## Contents

| Chapter | Topic |
|---|---|
| [00-problem-and-constraints.md](00-problem-and-constraints.md) | What "unmodified" can mean; why `.so` / C-ABI hosting is impractical; the conserved-invariant principle; current goopg state |
| [01-approach-cxgo-sdk-shim-marshaling.md](01-approach-cxgo-sdk-shim-marshaling.md) | The recommended pipeline; what cxgo removes vs. what it does not; precedents |
| [02-scope-mechanisms-and-tiers.md](02-scope-mechanisms-and-tiers.md) | contrib tiering; the framework mechanisms required regardless of porting method |
| [03-symbol-footprint-classifier.md](03-symbol-footprint-classifier.md) | The classifier design skeleton with cxgo-feasibility evaluation |
| [04-roadmap-and-poc.md](04-roadmap-and-poc.md) | Phased path, PoC targets, acceptance gates, risks |
