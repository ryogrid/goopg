# goopg Parser Rewrite — goyacc / PostgreSQL-Grammar Port

This directory carries the design and execution plan for replacing goopg's
hand-written recursive-descent SQL parser (`internal/parser`, ~35k lines of
source) with a **goyacc-generated LALR parser** ported from PostgreSQL 18.3's
grammar sources. The upstream tree at `./postgres/` (READ-ONLY oracle) is used
strictly as a specification:

```
PostgreSQL source                     goopg target
─────────────────                     ────────────
gram.y   ─┐                           grammar/pg_grammar.y ──┐
scan.l   ─┼── as spec ──►  Go lexer (adapted internal/parser lexer) +
kwlist.h ─┤                cmd/gen-kwlist-go generated keyword tokens
parsenodes.h ─┘                                          │
                                                         ▼
                                   goyacc LALR parser (actions build the
                                   EXISTING internal/parser AST structs)
                                                         │
                                                         ▼
                                   internal/parser/analyzer (unchanged)
```

## Document map

| file | contents |
|---|---|
| [01-architecture.md](01-architecture.md) | Current-state inventory, target architecture, package layout, what does not change |
| [02-grammar-porting-guide.md](02-grammar-porting-guide.md) | gram.y → goyacc translation conventions: actions, %union, locations, keywords, precedence, extension policy |
| [03-strangler-migration.md](03-strangler-migration.md) | Deterministic dispatch routing, per-wave cutover, legacy deletion plan, rollback story |
| [04-testing-and-gates.md](04-testing-and-gates.md) | Differential old-vs-new AST harness, gate matrix per phase, regression policy |
| [05-risks.md](05-risks.md) | Conflict risk, base_yylex filter subtleties, mmgr threading, known sharp edges |
| [TODO.md](TODO.md) | Execution checklist — one checkbox ≈ one commit |

## Ground rules

1. `./postgres/` is never modified and never imported at runtime. Grammar,
   keyword data, and semantics are *transcribed* into Go sources; every
   generated file carries a provenance header naming its upstream origin file.
2. Vanilla-PG compatibility is absolute: where goopg currently diverges from
   upstream syntax, the divergence is either preserved behind an explicitly
   tagged rule in `grammar/goopg_ext.y` or fixed to match upstream.
3. The existing 541 parser tests are black-box (they exercise `Parse()` /
   `ParseExpr()`), so they survive the rewrite as the primary behavioral
   safety net — unchanged except explicitly documented behavior deltas
   (04-testing §2 ledger).
4. One checkbox in TODO.md = one commit = one push. No `--no-verify`.
