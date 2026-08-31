# 0097-0024d — COPY query-form syntax errors (FROM / column-list)

Status: accepted
Milestone: M0097-0024 (Port COPY / sequence / identity regress tests)
Date: 2026-05-25

## Problem

Two `copyselect` "this should fail" cases produced the wrong diagnostics:

```sql
copy (select * from test1) from stdin;
-- PG:    ERROR:  syntax error at or near "from"
-- goopg: ERROR:  syntax error at or near "COPY (query) is only valid with TO" (byte 0)

copy (select * from test1) (t,id) to stdout;
-- PG:    ERROR:  syntax error at or near "("
-- goopg: ERROR:  syntax error at or near "expected FROM or TO in COPY (got () (byte 27)"
```

In PostgreSQL's grammar (`gram.y`), the parenthesised-query form of COPY
(`COPY '(' PreparableStmt ')' TO ...`) is **TO-only** and takes **no column
list** — there is simply no production for a trailing `FROM` or a `(col …)`
after the query. So both cases are plain syntax errors anchored at the
offending token (`from` / the opening paren), reported with the standard
`syntax error at or near "TOKEN"` shape plus a `LINE n:`/caret.

goopg instead:
1. accepted the `FROM`, set `Direction = CopyFrom`, then raised a bespoke
   `COPY (query) is only valid with TO` message anchored at byte 0 (the `COPY`
   keyword) — wrong text *and* wrong caret position; and
2. for the column list, fell through to the generic `expected FROM or TO in
   COPY` message — wrong text.

A second, independent defect: even once the parser produced the right
`*parser.SyntaxError`, the COPY wire path rendered it with `err.Error()`
directly, leaking the internal ` (byte N)` suffix and dropping the `Position`
field, so psql showed no `LINE`/caret.

## Fix

### Parser (`internal/parser/copy.go`, `parseCopy`)

Split the direction handling on source form. For the query form
(`stmt.Query != nil || stmt.QueryDML != nil`) only `TO` is legal; anything
else is a syntax error at the current token:

```go
if stmt.Query != nil || stmt.QueryDML != nil {
    if !p.acceptKeyword(KwTo) {
        return nil, p.errSyntaxAtCur()
    }
    stmt.Direction = CopyTo
} else {
    switch { // table form: FROM or TO
    case p.acceptKeyword(KwFrom): stmt.Direction = CopyFrom
    case p.acceptKeyword(KwTo):   stmt.Direction = CopyTo
    default: return nil, p.errAtCur("expected FROM or TO in COPY")
    }
}
```

This subsumes the old post-hoc `COPY (query) is only valid with TO` check
(removed). For `… from stdin` the current token is the direction `from`; for
`… (t,id) …` it is the opening paren — each becomes the offending token.

New helper `errSyntaxAtCur` (`internal/parser/parser.go`) emits a bare
PostgreSQL-style `syntax error at or near "TOKEN"` with no explanatory suffix,
for the cases where upstream's grammar simply has no production for what
follows. (`errAtCur` appends ` (got X)`, which is appropriate for
"expected X" diagnostics but not for these "no such production" ones.)
`SyntaxError.Message` is rendered as `syntax error at or near %q`, so passing
the raw token text yields the exact upstream wording.

### Wire (`internal/server/copy.go`, `dispatchCopyViaExecutor`)

The COPY parse-error arm now threads the error through `syntaxErrorMsg`
(the same helper the main simple-query path in `dispatch.go` uses), which
strips the internal ` (byte N)` suffix and attaches a `Position` ErrorField so
psql renders `LINE n:` with a caret:

```go
msg, extra := syntaxErrorMsg(err)
return ..., s.writeQueryError(w, sqlstate.SyntaxError, msg, extra...)
```

This is the same sibling-path class as the earlier COPY-from-view HINT fix —
the COPY dispatch had drifted from the canonical query-error rendering. See
[[pattern_sibling_paths_must_agree]].

## Verification

- Unit: `TestParseCopyQueryFromRejected`, `TestParseCopyQueryColumnListRejected`
  (`internal/parser/copy_test.go`) assert the `*SyntaxError` `Message` (`"from"`,
  `"("`) and `Pos` (the trailing token, not the SELECT's own `FROM`).
- Packages `internal/parser`, `internal/planner`, `internal/server` pass.
- Live (port 5599): both statements now emit the exact PG ERROR + `LINE 1:` +
  caret; plain `copy (select …) to stdout` still streams. The `copyselect`
  regress diff no longer contains the four gap-#1 lines.

## Remaining copyselect gap

Only the psql multi-command `\;` / `\.` STDIN handling remains (chained
`copy … from stdin` statements, the `expected exactly one COPY statement` and
`planner.Copy has no executor path yet` leaks). That is a distinct
wire/meta-command feature and is the next COPY win.
