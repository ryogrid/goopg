# 0119-0004ah — Conditional CREATE RULE (`WHERE … DO INSTEAD NOTHING`) round-trip in pg_dump (DU-002 slice 359)

Status: accepted
Milestone: M0119-0004 (pg_dump getter-battery parity; CSV row DU-002)

## Problem

DU-002 slice 324 made the **unconditional** `DO [INSTEAD] NOTHING` `CREATE RULE`
round-trip through pg_dump (parser → `catalog.Table.Rules` → `pg_rewrite`
projection → `pg_get_ruledef` deparse). It explicitly deferred the
**conditional** form — `CREATE RULE r AS ON UPDATE TO t WHERE (qual) DO INSTEAD
NOTHING` — which fell through to the historical `CompatNoopStmt` path and was
therefore **silently dropped** on dump/restore. The action-command form (a real
`DO INSTEAD INSERT …`) needs the full query reverse-compiler and stays out of
scope; the conditional DO-NOTHING form only needs the WHERE *qualification*
deparsed, which goopg already does for trigger `WHEN` clauses and index
predicates.

## Oracle (PG 18.3, `./postgres/local_install`)

For `public.rcond (a integer, b integer)`:

```sql
CREATE RULE rcond_upd AS ON UPDATE TO public.rcond WHERE (old.a <> new.a) DO INSTEAD NOTHING;
CREATE RULE rcond_del AS ON DELETE TO public.rcond WHERE old.b > 0 DO INSTEAD NOTHING;
```

`pg_dump` (single-arg `pg_get_ruledef`, `PRETTYFLAG_INDENT`) emits:

```
CREATE RULE rcond_upd AS
    ON UPDATE TO public.rcond
   WHERE (old.a <> new.a) DO INSTEAD NOTHING;
CREATE RULE rcond_del AS
    ON DELETE TO public.rcond
   WHERE (old.b > 0) DO INSTEAD NOTHING;
```

Layout details that must match byte-for-byte:

- The WHERE clause goes on its **own line** with a **3-space** indent (the event
  line keeps its 4-space indent from slice 324).
- The `DO INSTEAD NOTHING` action trails the WHERE clause on the **same** line
  (in the unconditional form it trails the `ON … TO …` line).
- The qual is rendered in the canonical **single-paren** form `(old.a <>
  new.a)` whether or not the source SQL wrapped it in parens (PG's
  `get_rule_expr` adds exactly one layer around the top-level `OpExpr`).
- The `OLD`/`NEW` qualifiers render lower-cased (`old.`/`new.`), matching PG's
  `get_rule_expr(varprefix=true)` over the `old`/`new` range-table aliases.

(Reference data dir: `/tmp/du359_ref`.)

## Change

Dump-fidelity only — goopg implements no query-rewrite system; a conditional
rule has no runtime effect beyond the existing COPY-DML rule-kind bookkeeping.

1. **Parser** (`ast.go`/`ddl.go`): new `CreateRuleStmt.Qual Expr`.
   `parseCreateRuleTail`'s `WHERE` branch now parses the qualification with the
   standard `p.parseExpr()` (instead of letting the flat token scan discard it);
   `parseExpr` consumes the whole balanced expression — including any outer
   parens — and leaves the scanner positioned at `DO`. The unquoted `OLD`/`NEW`
   qualifier is already lower-cased onto each `*ColumnRef` by the lexer (the same
   mechanism the trigger `WHEN` slice 329 relies on). The first-class
   `CreateRuleStmt` return is widened from `isNothing && !hasWhere && !hasAction`
   to also admit `hasWhere` **when a qual was captured** (`!hasWhere || qual !=
   nil`); every other shape (action command, `ON SELECT`, a WHERE with an action
   command) still falls back to `CompatNoopStmt`.

2. **Catalog** (`catalog.go`): new `RuleInfo.Qual string` — the **deparsed**
   WHERE text in canonical parenthesized form (`pg_rewrite` stores it as
   `ev_qual`; goopg keeps the rendered text since the rewriter is not executed).
   The `pg_rewrite.VirtualRows` projection leaves `ev_qual`/`ev_action` NULL even
   for a conditional rule — `getRules` never reads them; the rule text (with its
   WHERE) comes entirely from `pg_get_ruledef`.

3. **Executor** (`operators_ddl.go`): `execCreateRule` deparses `s.Qual` via
   `defaultExprToSQL` — the same renderer the trigger `WHEN` and index-predicate
   paths use, which fully parenthesizes a top-level `OpExpr` (`old.a <> new.a` →
   `(old.a <> new.a)`), matching `pg_get_ruledef`'s single-paren form — and
   stores the result on `RuleInfo.Qual`. **No extra paren layer** is added (the
   same convention `pg_get_indexdef`'s WHERE uses).

4. **Deparse** (`expr.go`): `buildRuleDefString` emits `\n   WHERE <qual>` after
   the `ON … TO …` line whenever `r.Qual != ""`, then the trailing ` DO [INSTEAD
   ]NOTHING;` as before.

`Qual` defaults empty/nil → every unconditional rule (and every existing
relation) is byte-identical to slice 324 → zero blast radius.

## Tests / gates

- `TestParseCreateRuleConditional` — parenthesized + no-paren WHERE both parse to
  `*CreateRuleStmt` with a `*BinaryOp` qual whose left operand keeps the
  lower-cased `old` qualifier; `TestParseCreateRuleNonNothingStaysNoop` updated
  (action command + `WHERE … DO INSTEAD UPDATE` + `ON SELECT` stay
  `CompatNoopStmt`).
- `TestDDLCreateRuleConditionalRoundTrip` — record → deparse byte-match against
  the PG-captured goldens (both source forms normalize to the single-paren WHERE
  form).
- `TestPort_PgDumpConnectionSetup` **DU-002 slice 359** — `rcond` with a
  parenthesized (UPDATE) and a no-paren (DELETE) source qual; real pg_dump 18.3
  re-emits both byte-identically.
- `internal/parser` + `internal/catalog` + full `internal/executor` suites PASS;
  `go build ./...` clean; pgbench smoke = pre-commit hook.

## Still open under M0119-0004

Action-command / `DO ALSO <stmt>` rules (the full query reverse-compiler);
GRANT/ACL further slices; reserved-keyword-named-role quoting;
extended-protocol commit-time deferral.
