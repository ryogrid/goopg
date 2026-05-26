# 0107-0003d — Parser token allocation: GC-safety of the `tokenSlicePool` fast path

- **Status:** Accepted (closes the M0107-0003 Phase C.3 token-arena attempt).
- **Date:** 2026-05-26
- **Related:** commit `adfb935` (`fix(M0097-crash): parser Token GC bug + cluster
  crash recovery`), [03-executor-concrete.md](perf-optimize/03-executor-concrete.md)
  §6, [01-memory-context.md](perf-optimize/01-memory-context.md),
  [0107-0003c-maybeforce-gc-hotpath-fix.md](0107-0003c-maybeforce-gc-hotpath-fix.md).

## 1. Summary

Phase C.3 of the performance refactor (M0107-0003) proposed allocating the
parser's `[]Token` backing array — and eventually the whole AST — from an
`mctx` memory context instead of the heap-backed `tokenSlicePool`, so that
"by end of statement, every parse / plan / operator allocation is bulk-reclaimed
by `stmtCtx.Release()`" (03-executor-concrete.md §6). The token half of that
plan shipped as a conditional **fast path**:

```go
// pre-adfb935 internal/parser/parser.go
if sctx != nil {
    toks, err = lexInto(mctx.AllocSlice[Token](sctx, 64)[:0], input) // arena
} else {
    sp = tokenSlicePool.Get().(*[]Token)                             // heap pool
    toks, err = lexInto((*sp)[:0], input)
    *sp = toks
}
```

It crashed the regress suite with `runtime: found pointer to free object`
after ~5 minutes and was removed in `adfb935`, which made both `Parse` and
`ParseExpr` always use the heap-backed `tokenSlicePool`.

**This document records why the arena fast path is _fundamentally_ unsafe in
Go — not merely buggy — and why the heap-backed `tokenSlicePool` is the correct,
already-allocation-free fast path.** The conclusion is a permanent guardrail:
`parser.Token` (or any type that embeds a Go pointer such as a `string`) must
never be stored in an `mctx` byte arena.

## 2. Background: what `mctx.AllocSlice[Token]` actually produces

`mctx.Context` is a bump allocator over 64 KiB / 4 KiB / 256 KiB **`[]byte`**
slabs (`internal/mctx/mctx.go`, `getChunk` → `make([]byte, 0, cs)`,
`mctx.go:149`). `AllocSlice[T]` (`mctx.go:372`) carves `size*n` bytes out of a
slab and reinterprets them with `unsafe.Slice((*T)(unsafe.Pointer(&buf[0])), n)`.

`parser.Token` (`internal/parser/token.go:395`) is:

```go
type Token struct {
    Kind    TokenKind
    Keyword Keyword
    Value   string // <-- a Go string: {ptr, len}. ptr is a GC-managed pointer.
    Pos     int
}
```

So `AllocSlice[Token]` returns a `[]Token` whose backing array lives inside a
slab that was **allocated as `[]byte`**.

## 3. Root cause #1 — a `[]byte` slab is a *no-scan* span

Go's garbage collector decides whether to scan an object for pointers from the
**type used to allocate the underlying heap object**, recorded in the span's
metadata — *not* from the static type of the slice header that currently points
at it. The `mctx` slab was allocated by `make([]byte, 0, cs)`; `[]byte` has no
pointers, so its span is marked **no-scan** (`noscan`). Reinterpreting that
memory as `[]Token` via `unsafe` does **not** change the span's pointer bitmap.

Consequence: during the mark phase the GC treats the slab as opaque bytes and
**never follows `Token.Value`'s string pointer**. A `Value` string whose only
live reference is a `Token` sitting in the arena is therefore invisible to the
collector and is reclaimed while still in use → `found pointer to free object`.

This is exactly the population the lexer allocates fresh on the heap (every
other `Value` is a substring of the input or a static string and is rooted
elsewhere — see §5):

| `Value` source | site | heap-allocated? |
|---|---|---|
| `strings.ToLower(src[a:b])` (identifier / keyword) | `lexer.go:119` | only when input has uppercase; otherwise returns the input substring |
| quoted identifier `b.String()` | `lexer.go:142` | **yes** |
| string literal `b.String()` | `lexer.go:160` | **yes** |
| `E'…'` escape string `b.String()` | `lexer.go:490` | **yes** |
| integer / numeric / param / dollar-body | `src[a:b]` | no — substring of `input` |
| single-char symbol `string(c)` / 2-char op | `lexer.go:423,457,460` | no — `runtime.staticbytes` / input substring / rodata |

During lexing these heap `Value` strings are referenced **only** from the
arena `[]Token`. A GC cycle mid-parse (or while the slice waits in a pool's
victim cache) frees them. That is the observed crash.

## 4. Root cause #2 — the cross-session plan cache retains `Value` by reference

Even if root cause #1 were patched by *copying* each `Value` into the same
arena (so the bytes live in a slab kept alive via the context registry rather
than the GC), a second, independent lifetime violation remains.

`internal/server/plancache.go` is a **cross-session** cache:
`map[string]planner.Node` keyed by normalized SQL, 512 entries, living far
beyond any single statement's `mctx` lifetime. Tracing the planner shows
cached plan nodes keep parser `Token.Value` strings **by reference**:

- SELECT target alias → `parser.ResTarget.Alias` (`select.go:565`, from
  `identText(tok)` → `tok.Value`) → `targetMeta` (`planner.go:~4290`) →
  `SchemaColumn.Name` (`plan.go:~38`).
- string literal constant → `parser.StringConst.Value` (`select.go:~1547`) →
  `planner.StringConst.Value` (`planner.go:~2555`).
- FROM-clause alias → `parser.RangeVar.Alias` → `SeqScan.Alias` (`plan.go:~409`).

(Table and column *names* are **not** retained from the AST — they are resolved
through the catalog and the plan stores a `*catalog.Table` pointer plus
catalog-owned column-name strings. Only the three classes above leak AST
strings into cached nodes.)

If any of those `Value` strings were arena-backed, `stmtCtx.Release()` /
context reuse would recycle the bytes underneath a live cached plan →
use-after-free / silent corruption on the next session that hits the cache.
The Phase C.3 premise that "every parse/plan allocation is bulk-reclaimed by
`stmtCtx.Release()`" simply does not hold once a cache outlives the statement.

## 5. Why the heap-backed `tokenSlicePool` is correct *and* fast

The retained path (`internal/parser/parser.go`) is:

```go
sp := tokenSlicePool.Get().(*[]Token)
toks, err = lexInto((*sp)[:0], input)
*sp = toks
...
tokenSlicePool.Put(sp)
```

It is correct on both axes:

1. **GC visibility.** The backing array comes from `make([]Token, 0, 64)`
   (`parser.go:17`), a *pointer-containing* (scan) span. The GC follows each
   `Token.Value` pointer, so heap `Value` strings stay alive for as long as the
   slice — or any AST/plan node that copied the string header — references them.
2. **Lifetime.** `Value` strings are ordinary GC-managed objects. Substring
   values keep the `input` (`sql`) backing array alive; transformed values are
   independent heap strings. Either way the cross-session plan cache holds
   normal GC roots, so cached plans never dangle.

It is also **already a fast path**: in steady state `sync.Pool` returns a
recycled 64-capacity `[]Token` (sized for the 10–20 tokens a typical pgbench
statement produces, `parser.go:11-14`), so the common parse performs **no**
`[]Token` heap allocation. The only residual cost over the arena variant is
`sync.Pool`'s `Get`/`Put` (a per-P fast path, tens of ns). Per
[0107-0003c](0107-0003c-maybeforce-gc-hotpath-fix.md) the dominant parse-path
cost at pgbench rates was GC thrash from a per-query `runtime.ReadMemStats`,
not token allocation; with that fixed (4 k → 42 k TPS), the token-pool overhead
is in the noise.

## 6. Decision

1. **Keep the heap-backed `tokenSlicePool` as the canonical fast path** for both
   `Parse` and `ParseExpr`. No conditional arena branch.
2. **`parser.Token` (and any AST node holding a Go pointer) is forbidden from
   `mctx` byte arenas.** This is a hard invariant, documented here and in the
   `AllocSlice` doc-comment.
3. The optional `mc ...*mctx.Context` parameter on `Parse`/`ParseExpr` is now a
   **no-op kept for source compatibility**; it is not used for token storage.
   `internal/server/dispatch.go` no longer acquires a throwaway `KindExpr`
   child purely to pass it in (that `Acquire`/`Release` was dead work whose
   comment falsely claimed it avoided `sync.Pool` overhead). Removing the
   parameter entirely is a safe future cleanup, deliberately deferred to keep
   this change minimal.

## 7. Alternatives considered and rejected

- **Arena `[]Token` + pin transient `Value` strings** (keep `Value` on the heap,
  hold a GC-visible `[]string` keep-alive plus `runtime.KeepAlive` across the
  parse). Defeats root cause #1 and leaves the plan cache safe, but
  reintroduces the exact pattern that caused the multi-minute regress-suite
  crash, adds `runtime.KeepAlive` subtlety, and allocates the keep-alive slice
  whenever transformed tokens exist. The recovered win is marginal (§5).
  *Rejected: risk ≫ benefit.*
- **Full arena + planner string interning** (arena-back `Value`, then deep-copy
  every AST string the planner retains into the GC heap before
  `planCache.Put`). Resolves root cause #2 but requires a recursive,
  easy-to-get-wrong copy of the plan tree's string fields — precisely the
  fragility class that produced the original bug. *Rejected.*
- **Pointer-free `Token`** (store `Value` as an `(offset,len)` into `input` or
  the arena instead of a `string`). Makes `[]Token` arena-safe, but transformed
  values (unquoting / unescaping / lowercasing) are not substrings of `input`,
  and a cached plan still cannot reference arena offsets. Touches every
  `Token.Value` reader. *Rejected as out of proportion to the benefit.*
- **GC-safe reusable per-connection token buffer** (drop `sync.Pool`, reuse one
  `[]Token` field per connection). Safe, but only removes `sync.Pool`
  bookkeeping (§5) at the cost of threading a buffer through the parser and
  re-entrancy reasoning. *Rejected: not worth the churn for a noise-level win.*

## 8. Guardrail for the future

> Any value placed in an `mctx` byte arena must be **pointer-free**. `mctx`
> slabs are `[]byte` (`noscan`) spans; the GC will not trace pointers stored in
> them, and arena memory is explicitly recycled, so it may not back any object
> whose lifetime can exceed the owning context (notably anything reachable from
> the cross-session plan cache). `parser.Token` embeds a `string` and violates
> both conditions.

If a future change reintroduces arena-backed tokens or AST nodes, it must first
remove the `string` pointer from the stored type **and** prove no cache outlives
the arena. The `AllocSlice` doc-comment already warns: "T must not contain
GC-traced fields that point outside c" — `Token.Value` is exactly such a field.

## 9. References

- Go runtime: span `noscan` classification — `runtime/mbitmap.go`,
  `runtime/malloc.go` (`heapBitsSetType` / `mallocgc` `noscan` path).
- Upstream PostgreSQL parse-tree lifetime: `MessageContext` in
  `postgres/src/backend/tcop/postgres.c` holds the raw parse tree for the life
  of a client message and is reset per message — the C analogue works only
  because C has no tracing GC and PG never caches pointers into `MessageContext`
  across messages.
- Code: `internal/parser/parser.go`, `internal/parser/lexer.go`,
  `internal/parser/token.go:395`, `internal/mctx/mctx.go:149,372`,
  `internal/server/plancache.go`, `internal/server/dispatch.go`.
