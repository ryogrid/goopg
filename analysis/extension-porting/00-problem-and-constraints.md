# Problem and Constraints

Date: 2026-05-27

## What "unmodified" can actually mean

A PostgreSQL `contrib` extension ships in two layers:

1. **Install artifacts** — a `<name>.control` file and versioned
   `<name>--X.Y.sql` scripts. These are plain text. They declare the extension's
   SQL-level objects and bind C functions with
   `AS 'MODULE_PATHNAME', 'symbol' LANGUAGE C`.
2. **Compiled C** — a `.so` shared library built from the extension's `.c`
   sources against the PostgreSQL backend headers.

"Run unmodified" therefore has two readings, and they differ enormously in cost:

- **(A) Load the compiled `.so` as-is** (or compile the unmodified `.c` and link
  it into goopg). The extension's object code is not self-contained: it has
  undefined symbols (`palloc`, `ereport`, `cstring_to_text`, `SearchSysCache`, …)
  that the dynamic linker resolves at load time **against the running backend
  executable**, which exports its symbols (`internal_load_library` in
  `postgres/src/backend/utils/fmgr/dfmgr.c`; the backend is linked with exported
  dynamic symbols). To host this, goopg would have to **export the entire
  PostgreSQL backend C API surface** from a Go process.
- **(B) Keep the `.control`/`.sql` artifacts unmodified** and provide goopg-native
  implementations of the referenced functions, resolving `MODULE_PATHNAME` to a
  native provider rather than a `.so`. This is the only reading that is tractable
  for a from-scratch Go engine.

Everything in this bundle treats **(B)** as the definition of "unmodified."

## Why (A) is impractical for goopg

1. **The symbol surface is not a small, stable contract.** PostgreSQL has no
   minimal "extension ABI." Even the simplest function transitively reaches
   memory management (`palloc`/`MemoryContextAlloc`/`CurrentMemoryContext`),
   varlena/TOAST handling (`pg_detoast_datum`, `cstring_to_text`), error
   reporting (`ereport`/`errstart`/`errfinish`), and frequently the catalog
   (`SearchSysCache`), the function manager (`fmgr_info`, `DirectFunctionCall`),
   and the SQL executor (`SPI_execute`). Supplying these means re-providing a
   substantial slice of the backend.

2. **`longjmp` vs. goroutine stacks (a fundamental conflict, not just labor).**
   PostgreSQL error handling is `ereport(ERROR)` → `siglongjmp` back to the
   nearest `PG_TRY` setjmp point, unwinding the C stack
   (`postgres/src/backend/utils/error/elog.c`, the global `PG_exception_stack`).
   In a Go process, C and Go stacks interleave across the cgo boundary; a
   `longjmp` that jumps across Go frames corrupts the Go runtime's stack
   management, `defer`/`panic`, and scheduler invariants. You cannot safely
   `longjmp` past a `cgocall`. A host would have to set up a `sigsetjmp`
   trampoline at every C entry point and reimplement the elog control-flow
   contract in C.

3. **Memory-context lifecycle mismatch.** Extensions `palloc` freely and rarely
   `pfree`, trusting PostgreSQL to reset/delete contexts at per-tuple /
   per-query / per-transaction boundaries (`postgres/src/backend/utils/mmgr/`).
   A host must implement a real context allocator and drive its lifecycle from
   goopg's executor.

4. **Crash isolation regresses.** PostgreSQL is multi-process: a `SIGSEGV` in one
   backend kills only that backend; the postmaster recovers. goopg runs
   connections as **goroutines in one process**, so a C fault is
   **process-fatal** — one misbehaving extension takes down the whole server.
   This is a real operational regression unique to goopg's threading model.

5. **It contradicts project constraints.** `.ralph/AGENT.md` mandates "No CGo
   unless a specific syscall is unreachable… Justify any introduction in a design
   doc." That rule in turn preserves pure-Go cross-compilation and static
   binaries.

## The conserved-invariant principle

The decisive idea threaded through this analysis:

> **Porting cost is proportional to the backend API surface an extension
> transitively touches — not to the lines of algorithm it contains. No source
> transformation removes that surface; it only relocates the binding.**

This is why later chapters reject "just rewrite/transpile the C" as a *strategy*:
rewriting changes how the extension *names* its dependencies, but whatever
behavior the extension needs (allocate in a resettable context, detoast a
varlena, raise an unwinding error, look up a type, run a query) must still be
*implemented* by something. Transpiling the *consumer* (the extension) does not
generate the *provider* (the backend); transpiling the provider too just
produces a second, redundant Go implementation of concepts goopg already
implements natively — which then must agree bit-for-bit.

## Current goopg state (relevant capabilities)

From codebase exploration (2026-05-27):

| Capability | State | Implication |
|---|---|---|
| `CREATE/ALTER/DROP EXTENSION` | Parser stubs only; no execution. `pg_extension` (OID 3079) seeded empty | No install path exists |
| Function dispatch | Name-based `switch` in `internal/executor/expr.go` (~100 builtins); no OID/handler-keyed fmgr | No place to bind extension functions |
| `CREATE FUNCTION` | SQL / PL/pgSQL only; `LANGUAGE C` rejected with SQLSTATE `42704` | C-language binding path is closed |
| cgo / dlopen / `.so` | None anywhere in the module | (A) has no foundation |
| `Datum` | 8 hard-coded Go kinds (tagged union), not extensible | New types need varlena/opaque backing + marshaling |
| `CREATE OPERATOR` / `pg_operator` | Not implemented / read-only seed | No operator-add path |
| Index AMs | btree only, **not pluggable** (gist/gin/hash/brin absent) | GiST/GIN-dependent extensions cannot be index-accelerated |
| FDW / libpq client | None | Federation extensions have no foundation |
| Hooks (planner/executor/utility/auth) | None (only internal SSI hooks) | Observation/policy extensions have no attach point |
| Triggers | Row-level implemented | `lo`/`tcn` trigger parts have a substrate |

Net: goopg lacks both the **install path** and the **function-binding path**.
These two are prerequisites for *any* extension support and are the subject of
the framework discussion in [02-scope-mechanisms-and-tiers.md](02-scope-mechanisms-and-tiers.md).
