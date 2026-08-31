# 0118-0044 — Dollar-quote-aware step splitter in the isolation harness (M0118-0008 enabler)

Status: accepted
Date: 2026-06-23
Milestone: M0118-0008 (DDL / VACUUM / maintenance concurrency)

## Summary

This is a **test-harness correctness fix, not a spec promotion.** It removes a
false blocker that mis-diagnosed every isolation spec whose *step body* is a
dollar-quoted PL/pgSQL block embedding its own statement-terminating
semicolons (e.g. `plpgsql-toast`'s `do $$ ... commit; ... $$;`).

## Problem

`internal/testport/framework/isolation_runner.go`'s `splitSQLStatements`
(used by `execStep` to break a multi-statement step into individual
statements, matching isolationtester's `PQgetResult` loop) handled
single-quoted strings and `--` comments but **not dollar-quoted strings**. A
step such as

```
do $$
  declare x text;
  begin
    select test1.b into x from test1;
    delete from test1;
    commit;
    ...
  end;
$$;
```

was split at the **first `;` inside the `$$` body**, so goopg received the
truncated fragment `do $$\n declare x text` and replied

```
ERROR:  lex error at byte 3: unterminated dollar-quoted string (looking for "$$")
```

The lex error is an artifact of the harness, *not* a goopg engine gap — goopg
lexes `$$…$$` correctly (`internal/parser/lexer.go`). The bug made
`plpgsql-toast`'s recorded first divergence point at the wrong place and could
do the same for any future dollar-quoted multi-statement step.

Setup blocks (which contain dollar-quoted `CREATE FUNCTION` bodies) were
unaffected because they run through `execGlobalSetup` / `execConnSetupCapture`,
which submit each block whole and never call `splitSQLStatements`. Only step
bodies go through the splitter, and no currently-passing spec had a
multi-statement dollar-quoted *step* (the one already-strict spec with a
dollar-quoted step, `merge-update`, wraps an `EXPLAIN … MERGE … RETURNING`
with **no top-level `;`** inside the `$$`, so both the old and new splitter keep
it as a single statement — byte-identical).

## Fix

Made `splitSQLStatements` dollar-quote aware:

- A new `dollarOpener(sql, i)` helper recognises a `$tag$` opener at a `$`
  byte, where the tag is empty (`$$`) or a SQL identifier and cannot itself
  contain `$` (PG manual §4.1.2.4). It returns the matching closer string and
  the opener length, or `("", 0)` for a positional parameter (`$1`) / stray
  `$`.
- The splitter tracks an active `dollarCloser`; while inside a dollar-quoted
  literal it copies bytes verbatim (ignoring `;`, `'`, `--`) until the exact
  closer is seen. Single-quote handling is unchanged and only consulted
  outside a dollar quote.

## Result

With correct splitting, `plpgsql-toast`'s first divergence moves from the
harness-induced lex error to the **real** engine blocker:

```
ERROR:  invalid PL/pgSQL DO body: plpgsql: syntax error at byte 96:
        unsupported PL/pgSQL statement
```

i.e. transaction control (`COMMIT`) inside a PL/pgSQL `DO` block — substantial
PL/pgSQL procedure-control work (plus `pg_advisory_lock` and TOAST
detoast-across-commit semantics the spec also needs). The spec stays
**deferred**; this loop only corrects the harness so the deferral is recorded
against the true blocker.

## Tests / gates

- New unit `TestSplitSQLStatementsDollarQuote`
  (`internal/testport/framework/isolation_test.go`): DO block with embedded
  semicolons kept whole; tagged `$body$`; plain statements still split;
  `$1`/`$2` positional params not treated as dollar quotes; single-quoted `;`
  still respected.
- `internal/testport/framework` package tests PASS.
- `TestPort_IsolationMergeUpdate` (the only already-strict spec with a
  dollar-quoted step) PASS — no regression.
- `go vet ./internal/testport/framework/` clean. pgbench smoke = pre-commit
  hook (no production-code change).

## Scope / blast radius

Test-harness only (`internal/testport/framework/`); no goopg engine change.
Cannot regress any spec — it only stops truncating dollar-quoted step bodies
that previously errored.

## Deferred (unchanged)

`plpgsql-toast` itself: PL/pgSQL `COMMIT`/`ROLLBACK` transaction control in a
`DO`/procedure, `pg_advisory_lock`/`pg_advisory_unlock[_all]`, and detoasting
of values held across a commit. The rest of the M0118-0008 tail
(`alter-table-{1,2,4}`, `detach-partition-concurrently-*`, partition
ATTACH/DETACH, `vacuum-no-cleanup-lock`, `reindex-concurrently-toast`) remains
deferred per the ledger — most blocked on transactional DDL visibility or
missing parser support (`NOT VALID`/`VALIDATE CONSTRAINT`, `DETACH … CONCURRENTLY`).
```
