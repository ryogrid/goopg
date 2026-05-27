# Approach: cxgo + SDK Headers + Go Shim + Boundary Marshaling

Date: 2026-05-27

This chapter specifies the recommended porting line for **leaf** extensions (see
[03-symbol-footprint-classifier.md](03-symbol-footprint-classifier.md) for the
precise leaf definition). It is a four-stage pipeline applied per extension,
operating on **unmodified** `.c` sources and unmodified `.control`/`.sql`
artifacts.

## The pipeline (waterfall)

```
unmodified contrib .c
        │
        ▼
[1] custom goopg SDK headers      ← redefine the MACRO layer of the extension API
        │                           (postgres.h / fmgr.h / varlena macros)
        ▼
[2] cxgo transpile                ← C → pure Go for the algorithm body
        │
        ▼
[3] hand-written Go shim          ← implement the FUNCTION layer of leaf symbols
        │                           (palloc, ereport, cstring_to_text, ...)
        ▼
[4] boundary marshaling           ← PG varlena/Datum byte layout  ⇄  goopg Datum
        │
        ▼
goopg-native callable function, bound to the unmodified .sql via MODULE_PATHNAME
```

### Stage 1 — Custom goopg SDK headers (the macro layer)

Instead of rewriting `.c`, we substitute the headers the `.c` includes. The
PostgreSQL extension API is heavily **macro**-based, and macros are header-only —
they can be redefined to expand into shim-friendly forms:

- Call-boundary macros: `PG_FUNCTION_INFO_V1`, `PG_FUNCTION_ARGS`,
  `PG_GETARG_*`, `PG_RETURN_*`, `PG_FREE_IF_COPY`, `PG_GET_COLLATION`.
- varlena access macros: `VARDATA`, `VARSIZE`, `VARDATA_ANY`, `SET_VARSIZE`.
- Datum conversion macros: `DatumGetTextPP`, `CStringGetTextDatum`, etc.

Working at this documented boundary is far more robust than rewriting arbitrary
`.c`: it touches the same surface PostgreSQL itself documents for extension
authors, and it leaves the algorithm body untouched. The functions these macros
ultimately call (`palloc`, `errstart`/`errfinish`, …) are **not** macros and are
handled in Stage 3.

### Stage 2 — cxgo transpile of the algorithm body

[cxgo](https://github.com/gotranspile/cxgo) converts C to **pure Go** (no cgo),
using a libc-style runtime and `unsafe.Pointer`-based pointer arithmetic. We use
it to mechanically port the extension's algorithmic core — the part that is
tedious and error-prone to re-derive by hand (e.g. `fuzzystrmatch`'s
double-metaphone state machine, `pgcrypto`'s primitives).

### Stage 3 — Hand-written Go shim (the function layer)

The transpiled Go calls leaf backend functions that must exist as Go. We
hand-write a small shim package providing PG-compatible behavior:

- `palloc`/`palloc0` → allocate from a per-call Go arena (a `[]byte`/slice pool);
  `pfree` → no-op (arena is released at call boundary).
- `ereport`/`elog(ERROR, …)` → convert to a Go `error` returned to goopg's
  executor (no `longjmp`).
- `cstring_to_text`/`text_to_cstring`/`pg_detoast_datum` → varlena builders/readers
  over the arena.

The shim implements **only** the leaf symbol set; the classifier's hard gate
guarantees leaf extensions reference nothing outside it.

### Stage 4 — Boundary marshaling

goopg's native `Datum` (8 Go kinds) and PostgreSQL's in-memory `Datum`/varlena
layout are different representations. At each call boundary we marshal:

- in: goopg `Datum` (e.g. a Go string) → PG `text*` varlena built in the arena.
- out: the returned PG `Datum` → goopg `Datum`.

For leaf extensions the inputs/outputs are scalars (text, bytea, int, numeric),
so marshaling is bounded. The moment a function takes/returns composites,
arrays, or records — or calls back into the catalog/SPI — marshaling would have
to reproduce the whole type system's binary layout, which is exactly the
non-leaf boundary the classifier excludes.

## What cxgo removes vs. what it does not

cxgo is the **strongest** option among C-derived approaches because it is pure Go
and thus eliminates the CGo-specific blockers from
[00-problem-and-constraints.md](00-problem-and-constraints.md):

**Removed by cxgo:**
- **longjmp ↔ goroutine-stack corruption** — no cgo boundary exists, so there is
  no Go-runtime-corrupting `longjmp`. (Error handling is instead modeled as Go
  error returns in the shim.)
- **crash isolation** — a fault becomes a recoverable Go `panic` rather than a
  process-killing `SIGSEGV` (modulo `unsafe.Pointer` misuse, which is not fully
  memory-safe).
- **toolchain** — pure-Go output keeps cross-compilation and static binaries.

**NOT removed by cxgo (the conserved invariant holds):**
- **Memory-context lifecycle** — the transpiled `palloc` still expects
  resettable-context semantics; the shim + executor must drive that.
- **Datum/varlena impedance** — still marshaled at the boundary; cxgo does not
  reconcile the two type systems.
- **fmgr / SPI / syscache closure** — a transpiled function that calls
  `SPI_execute` or `SearchSysCache` still needs a Go implementation that bridges
  into goopg's executor/catalog. cxgo transpiles the *consumer*, never the
  *provider*. Transpiling the provider too (`mcxt.c`, `elog.c`, `fmgr.c`, SPI,
  syscache) yields a second redundant Go backend that must agree bit-for-bit with
  goopg's native one — a reductio that ends at "run a transpiled PostgreSQL,"
  not "extend goopg."

## cxgo's own practical limits

cxgo is explicitly a "transpile then fix by hand" tool. Against PostgreSQL it
faces:
- **Macro density** — PG headers are among the most macro-dense C in existence;
  raw transpilation is poor, which is *why* Stage 1 pre-shapes the macro layer.
- **Varargs** — variadic backend calls (`errmsg`, `errdetail`) transpile poorly;
  the shim must expose fixed-arity wrappers.
- **setjmp/longjmp** — weak/unsupported; any extension using `PG_TRY`/`PG_CATCH`
  is a transpile blocker (the classifier flags it as `blocked`).

## Precedent

The split is instructive. Systems that wanted **PostgreSQL-extension
compatibility** achieved it by **embedding the real PostgreSQL backend C code**
(e.g. YugabyteDB reuses PG's query-layer C). Systems that **reimplemented** the
engine in another language **dropped C-extension support** (e.g. CockroachDB).
goopg is in the latter camp. This bundle's cxgo+shim line is a *narrow,
leaf-only* compromise — not a route to general C-extension hosting, which
effectively requires the real backend to be present.

## Honest summary

- "unmodified `.c` + cxgo" is the best available **implementation tactic** for
  leaf extensions, clearly superior to CGo dynamic linking.
- It is **not** a framework that hosts all of contrib. It addresses only the
  function-implementation slice of leaf extensions; the framework slice
  ([02](02-scope-mechanisms-and-tiers.md)) is common to any approach, and for
  small/clean leaves a **native Go reimplementation is often less total work and
  lower risk** than maintaining a transpile+shim pipeline.
