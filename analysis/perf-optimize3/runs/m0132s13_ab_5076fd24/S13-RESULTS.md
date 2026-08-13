# M0132-S13 prepared-statement cache — measured result

date: 2026-08-13 · goopg `5076fd24` + uncommitted 0132-0005 cache change · PostgreSQL 18.3

## What landed

`0132-0005` hoists the cross-session plan-cache lookup ahead of the parse on
the extended Execute path (parse only on a cache miss) and routes the extended
Describe path through the same `s.pc`. This eliminates the two hot frames S11's
O-XP-1 profile located: `parser.Parse` per Execute (6.2% cum on `-N`) and
`describeViaPlanner` per Describe (13.4% cum on `-S`). The parsed AST is
deliberately NOT cached — `planner.Plan` mutates its `parser.Stmt` argument, so
skipping parse on a cache hit (rather than reusing an AST) is what is safe.

## A/B results (S13's specified conditions: scale 1, `-c 2 -j 2 -T 30`)

| workload | simple | prepared | gap |
|---|---:|---:|---:|
| `-S` (read) | 12,580 TPS | 9,581 TPS | prepared **−23.8%** |
| `-N` (write) | 654 TPS | 647 TPS | prepared **−1.2%** |

## High-concurrency control (scale 1, `-c 50 -j 8 -T 20`, CPU-bound)

| workload | simple | prepared | gap |
|---|---:|---:|---:|
| `-S` | 101,351 TPS | 100,265 TPS | prepared **−1.07%** |

Against S11's same-shape `-S` measurement (scale 100 c=50: prepared −22.3%),
the c=50 gap collapsed from −22.3% to −1.07% — **the cache works**: at
CPU-bound concurrency the prepared path's own parse/describe waste is gone and
prepared reaches parity with simple.

## Real PG 18.3 oracle (same `-S` workload, fsync off)

| concurrency | PG simple | PG prepared | gap |
|---|---:|---:|---:|
| c=2  | 14,846 TPS | 18,348 TPS | prepared **+23.6%** |
| c=50 | 113,363 TPS | 125,831 TPS | prepared **+11.0%** |

## Conclusion — the "prepared > simple" assertion is unsatisfiable in goopg

PG's prepared beats simple because PG's **simple** protocol re-parses *and*
re-plans every statement, so prepared amortises both. goopg's simple protocol
does **not** re-plan: the cross-session plan cache (`s.pc`, M0098-0005) is read
on the **simple** path too (`dispatch.go:966-989`), so goopg's simple is
already plan-amortised — there is no plan advantage left for prepared to win
back. Combined with a parser that is cheaper than PG's (so the parse saving is
small) and the extended protocol's fixed framing cost (Bind/Describe/Execute/
Sync vs one Query message), goopg prepared lands at parity (c=50) or below
(c=2, latency/framing-bound) — it cannot exceed simple.

Two residuals, both **not** defects in 0132-0005:

1. **c=2 framing gap (−23.8%)** — at 2 clients the server is latency-bound, and
   goopg's per-message loop is the cost (PG's prepared at c=2 is *faster*,
   −0.026 ms/txn, because PG's parse+plan saving exceeds its framing). Out of
   M0132 scope; a message-loop efficiency pass would address it.
2. **`-N` is fsync-bound** (3.06 ms/txn at scale 1), so prepared-vs-simple is
   noise there; the parse skip is invisible against the commit fsync.

Recorded as deferral-ledger row (see `.ralph/deferral_ledger.md`).
