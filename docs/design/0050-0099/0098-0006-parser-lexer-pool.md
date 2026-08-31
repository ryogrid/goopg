# Design: Parser Lexer Pooling (M0098-0006a)

**Status**: accepted  
**Milestone**: M0098-0006 — Memory allocation hot-path reduction, item (a)  
**Expected gain**: ~700 bytes / 2 allocations eliminated per Parse call

## Problem

The M0092-followup allocation profile showed `parser.Lex` = 22% of all allocs
(88.7 MB / 30 s). Every `Parse(sql)` call allocates:
- A `[]Token` slice: 13–20 tokens × 48 bytes = 624–960 bytes
- A 32-byte `parser` struct

At 443 TPS with 100 connections, this is ~44,300 Parse calls per second =
~28 MB/s of Token slice allocation alone, keeping GC busy.

## Design

### sync.Pool for token slices and parser structs

Add two package-level pools:

```go
var tokenSlicePool = sync.Pool{
    New: func() any {
        s := make([]Token, 0, 64)
        return &s
    },
}
var parserPool = sync.Pool{
    New: func() any { return &parser{} },
}
```

Pre-size token slice to 64 (covers pgbench queries without reallocation).

### lexInto: pool-friendly lexer variant

Add `lexInto(dst []Token, input string) ([]Token, error)` that appends into
a caller-provided slice. `Lex()` becomes `return lexInto(nil, input)`.

### Modified Parse() and ParseExpr()

1. Get `*[]Token` from pool, reset to `[:0]`
2. Call `lexInto((*sp)[:0], input)` — fills pooled backing array
3. Get `*parser` from pool, set `tokens` and `idx`
4. Parse statements (AST built from token data, not from token slice memory)
5. Before returning: nil out `p.tokens`, put both back in pool

### Thread safety

`sync.Pool` is inherently goroutine-safe. Each goroutine gets its own
instance from the pool. Token string values point into the input string
(not the token slice), so the token slice can be safely recycled while
the AST remains valid.

### Measurement

Benchmark (AMD Ryzen 7 5700X, go1.25):
- UPDATE pgbench query: **536 B/op, 15 allocs** (was ~1.7 KB, 17 allocs)
- Eliminated: ~700 bytes + 2 allocations per Parse call

## Files changed

| File | Change |
|------|--------|
| `internal/parser/lexer.go` | Add `lexInto()` |
| `internal/parser/parser.go` | Add `tokenSlicePool`, `parserPool`; modify `Parse`, `ParseExpr` |
| `internal/parser/parse_bench_test.go` | Benchmark + concurrency test |
| `docs/design/README.md` | Index entry |
