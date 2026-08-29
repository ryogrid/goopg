# M0134-0172 — `RETURN QUERY` and `FOR … IN <query>` must substitute PL/pgSQL frame variables

Status: **landed** (2026-08-29)
Task: `M0134-0172` (regress case `stats_ext.sql`)
Code: `internal/executor/plpgsql_runtime.go`
Guard: `internal/executor/plpgsql_query_frame_vars_test.go`
 (`TestPlpgSQLQueryStmtsSubstituteFrameVars`)

## Summary

goopg's PL/pgSQL runtime plans some statements from a *captured SQL string*
rather than from an AST it owns. Every such statement must first rewrite the
string's PL/pgSQL variable references into SQL literals, because the planner
that receives it has no PL/pgSQL frame and will resolve any bare identifier as
a **column of the queried relation**.

Three statement kinds capture SQL this way. Two of them substituted; the third
and fourth did not:

| statement | handler | substituted before this change? |
|---|---|---|
| embedded DML / `PERFORM` | `execPLpgSQLEmbeddedSQL` | yes |
| `SELECT … INTO` | `case *plpgsql.SelectIntoStmt` | yes (added by M0118-0008) |
| `RETURN QUERY <query>` | `case *plpgsql.ReturnQueryStmt` | **no** |
| `FOR rec IN <query> LOOP` | `case *plpgsql.ForSelectStmt` | **no** (static form) |

The consequence is much larger than it sounds. It is not that *some* variables
failed to resolve — **no** PL/pgSQL variable was visible to those two
statements. Not a declared local, not a function parameter. This minimal
function failed outright:

```sql
CREATE FUNCTION f(n int) RETURNS TABLE (a int) LANGUAGE plpgsql AS $$
declare v int := 7;
begin
  return query select v + n;   -- ERROR: 42703 column "v" does not exist
end $$;
```

`RETURN QUERY` whose query references a variable is the *normal* way to write a
set-returning PL/pgSQL function, so this closed off a large slice of ordinary
PL/pgSQL.

## How it surfaced

`stats_ext.sql` opens with the helper every estimate assertion in the file runs
through:

```sql
create function check_estimated_rows(text) returns table (estimated int, actual int)
language plpgsql as $$
declare ln text; tmp text[]; first_row bool := true;
begin
    for ln in execute format('explain analyze %s', $1) loop
        if first_row then
            first_row := false;
            tmp := regexp_match(ln, 'rows=(\d*) .* rows=(\d*)');
            return query select tmp[1]::int, tmp[2]::int;
        end if;
    end loop;
end; $$;
```

Every call raised `42703: column "tmp" does not exist` — **381 times in one
run**, 88% of the case's 435 `^+ERROR` lines, all from this one missing call.

The failure was invisible from the outside because `tmp[1]` *does* work in a
`RAISE` (a different evaluation path), so the variable was plainly bound; only
the query-planning path could not see it.

## The fix

### 1. Call the existing substituter from both handlers

`substitutePlpgsqlFrameVarsInSQL` already existed and already did the right
thing. Both handlers now call it (`ReturnQueryStmt` also gains the
`substituteTriggerRefs` pass its siblings have, so `RETURN QUERY SELECT NEW.x`
works inside a trigger function).

The **`FOR … IN EXECUTE <expr>`** form is deliberately excluded. There the
parameters travel through `USING` and the string is opaque to PL/pgSQL, matching
PostgreSQL — substituting it would corrupt a dynamic query that happens to name
a column after a local. The handler had already collapsed both forms onto one
`sql` variable, so the substitution is placed *before* the EXECUTE branch
rewrites it, guarded on the statement not being the EXECUTE form.

### 2. A subscript of a NULL or out-of-range array must render `NULL`

Pass 1 of the substituter handles `varname[N]`. When the variable was NULL, or
the index fell outside the array, it *bailed out and emitted the identifier
text unchanged* — and pass 2 then skipped it too, because pass 2 suppresses any
identifier followed by `[`. The bare `tmp[1]` reached the planner and became a
column reference.

PostgreSQL yields NULL for both cases (`ExecEvalSubscriptingRef`,
`postgres/src/backend/executor/execExprInterp.c`). Both now emit the literal
`NULL`. This is not a corner case for `stats_ext.sql`: `regexp_match` returns
NULL on every EXPLAIN line that does not match, which is most of them.

### 3. Exclusion list — the collision this exposed

`RETURNS TABLE (a int)` stores its result columns as trailing OUT arguments,
and the frame builder registers every OUT argument as an ordinary **NULL frame
variable**. Once `RETURN QUERY` substitutes, a function whose result column is
named after a real table column silently self-destructs:

```sql
CREATE FUNCTION h(lo int) RETURNS TABLE (a int) LANGUAGE plpgsql AS $$
begin return query select a from q2 where a >= lo; end $$;
-- `a` substitutes to the always-NULL OUT variable:
--   select NULL from q2 where NULL >= 2   →  0 rows
```

This was caught by sub-test (b) of the guard, which failed on the first
implementation.

PostgreSQL settles the same collision with `plpgsql.variable_conflict`, whose
default is `error` — it raises `column reference "a" is ambiguous`. goopg's
text substitution has no ambiguity detector and cannot reproduce that, so the
fix takes the only non-destructive option: **the column wins**. A new
`plpgsqlFrame.outParamNames` records the routine's OUT/INOUT/TABLE parameter
names, and `substitutePlpgsqlFrameVarsInSQL` grew a variadic `except …string`
that both new call sites pass it through. `FOR … IN <query>` additionally
excludes the loop's own record variable, so a stale binding from the previous
iteration cannot be substituted into the query that produces the next one.

The exclusion is applied in **all three** substitution arms (pass 1's array
subscripts, pass 2's record-field arm, pass 2's bare-identifier arm). Missing
the third arm was the reason the first attempt still failed — see
[Known limitations](#known-limitations) for why that is the interesting part.

## Verification

- **Guard** `TestPlpgSQLQueryStmtsSubstituteFrameVars` — 7 sub-tests, all pass.
  **Revert-checked**: 6 of the 7 fail against the pre-change body. The 7th
  (`for_in_execute_still_uses_using`) is the over-reach guard and must pass both
  before *and* after — it pins that the EXECUTE form is still **not**
  substituted, using a local deliberately named after a table column.
- **`stats_ext.sql`**: 3754 → 3451 diff lines, `^+ERROR` **435 → 54**
  (the full 381-error bucket cleared).
- **14-case regress A/B** (`plpgsql triggers rangefuncs domain
  create_function_sql polymorphism transactions returning with copy inherit
  alter_table foreign_key privileges`): 13 byte-identical, `plpgsql` +10 lines.
- **`plpgsql.sql` investigated to statement level.** Its case diff is
  nondeterministic (memory: 4400/4402/4401), so this was A/A'd first — the noise
  band is ±1 per side, making +10 a real signal. Running the whole file against
  two **freshly-initdb'd** clusters (one per build) and diffing goopg's own
  output showed **exactly one changed statement**: `ret_query2(8)`, which went
  from `42703 column "lim" does not exist` to returning **all 9 rows with values
  byte-identical to PostgreSQL**. The diff grew because 1 error line became 9
  data rows that are still mis-*shaped* by an unrelated gap (below). Nothing
  regressed.
- `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh`, `scripts/tpch-spotcheck.sh`,
  pgbench pre-commit smoke — see the commit's `Gates run`.

## Known limitations

Recorded in `.ralph/deferral_ledger.md`; none is a regression.

1. **`RETURNS SETOF <composite>` is not column-expanded.** `ret_query2` above
   now returns the right 9 values but as a single composite column
   `(hash,-8,f)` instead of `x | y | z`. `frame.returnNextRows` packs a
   multi-column row into one composite text datum; PostgreSQL expands the
   rowtype into the function's result tuple descriptor.
2. **Variable/column ambiguity is resolved silently, not reported.** goopg has
   no `plpgsql.variable_conflict`; a collision picks the column (RETURN QUERY)
   or the variable (elsewhere) instead of raising `42702`.
3. **Text substitution remains the binding mechanism.** This change extends the
   existing convention to two more statements; it does not replace it with
   PostgreSQL's `PARAM_EXTERN` parser hooks. That is the standing item behind
   the `goopg plpgsql binds vars by pre-parse TEXT substitution` finding.

## The lesson worth carrying

A "lazy convention" — *the caller resolves it at each consumer* — has a failure
mode that code review does not catch: **the convention is invisible at the site
that forgets it.** `ReturnQueryStmt` looks completely correct in isolation. It
parses a string and plans it, which is what it is supposed to do. Nothing at
that site says a rewrite pass was owed first; the obligation lives entirely in
the *other* three handlers.

This is the mirror image of M0134-0171's lesson (a shared helper was wrong while
all five call sites were right). Together they bracket the same hazard: when
correctness is distributed across a set of siblings, neither auditing the helper
nor auditing the call sites is sufficient on its own — you have to enumerate the
set and check that it is closed. The table at the top of this document is that
enumeration, and it is the artifact most worth keeping.

The same shape then repeated *inside* the fix: `substitutePlpgsqlFrameVarsInSQL`
has three independent arms that consult the frame, and the first implementation
wired the new exclusion into two of them. The bug survived the unit test for one
run because only sub-test (b) exercised the third arm.
