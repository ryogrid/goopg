# 05 — Risks & Mitigations

| # | risk | impact | mitigation |
|---|---|---|---|
| 1 | **goyacc ≠ bison behavioral deltas** on a grammar as large as gram.y (19.7k lines). CRITICAL INSTANCE: **goyacc lacks `%expect` and EXITS 0 even with unresolved conflicts**, silently emitting a broken parser — upstream's `%expect 0` invariant does not transfer | silent mis-parses land undetected | `make gen-parser` MUST grep goyacc stderr and y.output for `conflicts:` and fail the build (02 §6; gated at P0.5 by a seeded-conflict test). Feasibility otherwise de-risked: full gram.y passes goyacc v0.49.0 with 0 conflicts / 6,501 states (probe, 2026-08-25) |
| 2 | **_LA lookahead filter port errors** (NOT_LA/WITH_LA/NULLS_LA/FORMAT_LA/WITHOUT_LA, UESCAPE triple) | subtle mis-acceptance/mis-parse of `WITH TIME`, `NOT BETWEEN` etc. | ported verbatim from parser.c base_yylex with table-driven tests over all substitution pairs incl. UESCAPE validation errors |
| 3 | **mmgr.Context threading**: AST constructors accept optional mmgr contexts; yacc stack values must carry them without leaking or double-free semantics | memory-context correctness regressions | union carries the context once at parse start; support helpers pass it through; existing tests (which exercise mc variants) stay green; no new allocation patterns beyond today's |
| 4 | **Lexer divergences vs scan.l** surfacing only under LALR | parity bugs blamed on grammar | P0 lexer-conformance checklist diffed against scan.l; differential harness catches downstream effects early. Specifics below (§ new risks 11-12) |
| 5 | **Error-message drift** (byte Position, wording, raw-source echo per M0134-0070) | visible client-facing regressions; oracle-diff noise | error contract tests per statement class (04-testing §5); syntaxErrorMsg path RELOCATED into internal/parser during P0 (currently lives in postmaster — layering forbids sqlparser→postmaster import), postmaster consumes it from there |
| 6 | **Dispatch edge cases** (leading-keyword routing): parenthesized queries `(SELECT...)`, WITH-led DML (`WITH ... INSERT`), multi-statement strings mixing classes, ident-led statements arriving as TokenIdent | statements routed to wrong parser | token-stream dispatch with follower-keyword scanning for WITH, '(' entry for parens, per-statement routing after top-level ';' split, case-insensitive ident matching (03 §2); dispatch unit tests pin the wrapper invariant |
| 7 | **Performance regression** from generated parser (stack traffic, interface boxing in union) | TPS / latency dips | concrete-typed union fields (no boxing on hot paths); P0 micro-bench baselines compared at every flip (04-testing §3); pgbench smoke each commit |
| 8 | **Legacy/new keyword-table skew during migration** (legacy Kw* vs generated set disagreeing on a word's identity) | same word treated differently by the two parsers in one batch | single source of truth: keywords_gen derives BOTH the token set AND a compatibility list; a startup-time test asserts no category/identity conflicts for words legacy knows |
| 9 | **Scope creep into analyzer/planner** ("while we're here") | unbounded surface, review risk | hard non-goal (03-strangler §6); commits touching outside internal/{parser,sqlparser}, grammar/, cmd/gen-kwlist-go need explicit justification in the message |
| 10 | **Subagent/tooling instability** observed during planning (provider outages) | slower reviews | reviews retried with scoped prompts; fallback is self-review checklist appended to TODO items |
| 11 | **Named multi-char operator terminals vs legacy lexer shape**: scan.l emits TYPECAST/DOT_DOT/COLON_EQUALS/EQUALS_GREATER and the comparison family (LESS_EQUALS etc.) as DISTINCT terminals; goopg's lexer folds them into generic operator strings (lexer.go:489-575) | adapter feeds wrong terminals → grammar misfires | the yyLexer adapter splits by string value before mapping to gram.y terminals; conformance tests over every named-operator spelling |
| 12 | **scan.l operator post-processing** beyond greediness: embedded `/*`/`--` truncation, SQL trailing `+`/`-` stripping rule, NAMEDATALEN op-length error (scan.l:893-943, :987-988) | obscure parity bugs | each rule becomes a differential-test fixture at P0 |
| 13 | **Toolchain drift**: goyacc conflict-report format/exit behavior is version-dependent | gate silently weakens across upgrades | pin x/tools version in go.mod + tools.go; note the pinned version next to the conflict-gate code |
| 14 | **RawParseMode injection** if PL/pgSQL-style modes are ever routed through the new parser | future integration surprise | base_yylex port keeps the MODE_* branch stubbed but present (02 §1) |

## Known upstream quirks to respect (found while surveying)

* `%expect 0` upstream; goyacc ignores `%expect` entirely — our zero-conflict
  guarantee comes ONLY from the build-time conflict grep (risk #1).
* Keyword categories are grammar-side lists — FIVE of them including
  `bare_label_keyword` (02 §4).
* `base_yylex` also handles `UESCAPE`'s third-token SCONST and validates
  escape strings — easy to miss.
* Precedence attaches to char-literal terminals (`=`, `<`, `>` from scan.l's
  self-set) and to NAMED comparison tokens (LESS_EQUALS ...) — NOT to the
  generic `Op` value; there is no per-value precedence mechanism to emulate.
* Upstream gram.y has ZERO mid-rule actions — porting stays within
  plain-rule territory end to end.
