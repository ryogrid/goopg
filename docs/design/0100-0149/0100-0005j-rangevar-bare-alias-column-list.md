# M0100-0005j — RangeVar bare-alias column list

## Status
Implemented (2026-05-15, loop 26).

## Problem
The MERGE JOIN isolation spec
(`postgres/src/test/isolation/specs/merge-join.spec`) failed every
permutation in global setup with:

```
pq: syntax error at or near "expected ';' or end of input (got ()"
    at position 4:58 (42601)
```

The failing line of the spec setup is

```sql
INSERT INTO src SELECT x, x*10 FROM generate_series(1,3) g(x);
```

`generate_series(1,3) g(x)` is a table-function reference with a
*bare* alias (no `AS` keyword) followed by a column-alias list `(x)`.
The same shape is widely used in upstream regression and isolation
tests, e.g.

```sql
SELECT * FROM mytable t (a, b);
SELECT * FROM (VALUES (1)) v (col);          -- already worked (AS)
SELECT * FROM generate_series(1,3) g (x);    -- failed (bare alias)
```

## Root cause
`parser.parseRangeVar` (`internal/parser/select.go`) handled the column
alias list correctly inside the **AS-branch** but the **bare-alias
fall-through branch** simply consumed the alias identifier and
returned, never inspecting the next token.  When the next token was
`(`, control returned to the FROM-list driver which expected `,`,
`WHERE`, or end-of-statement.

The AS-branch logic that handles `AS alias (c1, c2, ...)`:

```go
if p.acceptKeyword(KwAs) {
    t, err := p.parseIdent()
    ...
    rv.Alias = identText(t)
    if p.cur().Kind == TokenSymbol && p.cur().Value == "(" {
        // parse column-alias list, store in rv.Columns
    }
    return rv, nil
}
```

The bare-alias branch:

```go
if isAliasStart(p.cur()) {
    t := p.advance()
    rv.Alias = identText(t)
}
return rv, nil
```

…with no list handling.  PostgreSQL's grammar treats AS as optional
in `alias_clause`
(`postgres/src/backend/parser/gram.y::alias_clause`), so both shapes
must accept the parenthesised column list.

## Fix
Mirror the AS-branch list parser inside the bare-alias branch, so the
optional `(c1, c2, …)` is consumed and stored in `rv.Columns`
identically to the AS path.  No AST or downstream-consumer changes
are required because `RangeVar.Columns` is the single shared sink
already used by the AS branch and the derived-subquery branch.

## Verification
- `internal/parser/select_test.go::TestParseRangeVarBareAliasWithColumnList`
  pins the SRF + bare-alias + 1-column shape (the exact `generate_series
  (1,3) g(x)` shape from the merge-join spec).
- `internal/parser/select_test.go::TestParseRangeVarBareAliasMultiColumnList`
  pins the multi-column variant `mytable t (a, b)`.
- `merge-join` global setup now parses; the spec advances past the
  setup block on every permutation (was: hard-fail at `g(x)`).
- `go test ./internal/parser/ ./internal/planner/ ./internal/analyzer/
  ./internal/executor/` PASS.

## Out of scope
The remainder of the merge-join spec still defers — m1 / m2 / mj / ex
output divergence reflects real MERGE / EXPLAIN / EPQ-recheck gaps
that are tracked separately under M0100-0005's 21-spec pass goal.
This sub-milestone closes only the parser-level blocker.
