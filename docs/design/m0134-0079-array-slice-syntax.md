# M0134-0079 — array slice syntax `expr[lower:upper]`

Status: accepted (case PARKED — remains `failed`)
Task: M0134-0079 (`tuplesort.sql`)

## Summary

Sized at HEAD (`scripts/pg-regress-runner.sh --verbose tuplesort`): **473 raw
diff lines / 11 hunks / 2 `^+ERROR` / 0 `^-ERROR`**, the gate runs to
completion (no hang/crash). Five independent root causes:

- **A** (`EXPLAIN` plan shape): a fresh single-column btree index that exactly
  matches an `ORDER BY ... LIMIT` is not chosen over a `Sort` + `Seq Scan` —
  goopg picks `Sort -> Seq Scan` where PG picks `Index Scan`. Cost-model /
  planner-preference gap, REFACTOR-tier (own milestone; PG's
  `create_index_paths`/`build_index_paths` cost comparison, `optimizer/path/
  indxpath.c`).
- **B** (`CLUSTER` physical order + aborted-tuple retention): after `CLUSTER
  ... USING <idx>`, `ORDER BY ctid` does not reproduce the clustered physical
  order goopg itself just wrote, and PG's expected output additionally
  retains dead/uncommitted tuples with NULL columns from the case's aborted
  sub-transactions (the "abbrev_abort_uuids" name refers to PG's abbreviated
  sort-key abort path, `tuplesort.c AssertCheckAbbrevKeys`/`abbrev_converter`
  fallback — goopg has no abbreviated-key optimization at all, so it never
  reaches PG's abort branch or leaves the aborted rows it inserted along the
  way). REFACTOR-tier — no contained slice found (~230 lines, the largest
  bucket).
- **C** (`expr[lower:upper]` array-slice syntax): **shipped this loop** — see
  below.
- **D** (cursor `FETCH LAST`/`FETCH BACKWARD` scroll semantics): a
  backward-scrollable cursor's `FETCH LAST` / `FETCH BACKWARD` / `FETCH NEXT`
  sequence returns a different row count/position than PG (~85 lines across
  the in-memory and on-disk sort variants). Distinct from bucket A/B;
  unsized, ledgered.
- **E** (`EXPLAIN` plan shape for `count(DISTINCT ...)` + mark/restore): a
  `GroupAggregate` over a `Merge Join` with `Incremental Sort` (PG) vs `Hash
  Join` with a plain `Sort` (goopg) — goopg has no Incremental Sort node
  (same gap already ledgered from M0134-0030) and picks Hash Join instead of
  Merge Join for this shape. REFACTOR-tier, overlaps the existing Incremental
  Sort re-arm trigger.

## Landed: bucket C — array slice syntax

`(array_agg(id ORDER BY id DESC NULLS FIRST))[0:5]` — and every other
`arr[lower:upper]` expression in the SQL surface — failed to parse at all:
`lex error at byte N: unexpected character ':'`. goopg's lexer treated any
bare `:` (outside `::` and `:=`) as a hard lex error; PG's grammar uses it as
the array-slice separator (`indirection_el: '[' a_expr ']' | '[' opt_slice_bound
':' opt_slice_bound ']'`, `gram.y`), with either bound optional
(`arr[:5]`, `arr[5:]`, `arr[:]`).

### Fix

- **Lexer** (`internal/parser/lexer.go`): emit a bare `:` as a plain
  `TokenSymbol` instead of erroring. This only changes behaviour where a
  parser production now consumes it (array subscripts); every other context
  still surfaces "unexpected token" from the normal parse-error path, so no
  previously-valid-vs-error SQL statement changes classification.
- **Parser** (`internal/parser/select.go`, the `expr[...]` postfix arm):
  after `[`, if the next token is `:` or `]`, the lower bound is omitted;
  after consuming `:`, if the next token is `]`, the upper bound is omitted.
  `ArraySubscriptExpr` (`internal/parser/expr.go`) gained `IsSlice bool` and
  `Upper Expr` fields; `Index` doubles as the slice's lower bound.
- **Planner** (`internal/optimizer/planner.go` `resolveExpr`): a slice lowers
  to `FuncCall{Name:"array_slice", Args:[base,lower,upper]}`, mirroring the
  existing `array_subscript` lowering. An omitted bound resolves to
  `&NullConst{}` so the shared evaluator can distinguish "omitted → default
  to that dimension's actual bound" from an explicit `NULL` bound (PG:
  `NULL` in a slice bound clamps as if omitted too — goopg does the same
  since both paths hit the same "default" branch).
- **Column naming** (`targetMeta`): PG's `FigureColnameInternal`
  (`parse_target.c`, `T_A_Indirection`) skips pure subscripts/slices (no
  field-name `String` in the indirection list) and recurses into the *base*
  expression's name — `(array_agg(x))[1:2]` labels as `array_agg`, not a
  name derived from the slice itself. Added an `array_slice` arm that
  recurses `targetMeta` into `fc.Args[0]`, matching the existing
  `array_subscript` element-access precedent's structure (though that arm
  derives a type-based label for a different, pre-existing reason — left
  untouched, out of scope here).
- **Evaluator** (`internal/executor/expr.go`, `array_slice` case): splits the
  base array's TEXT form with the existing `splitArrayElements` (which
  preserves each element's original raw substring, so no re-quoting/escaping
  is needed to reassemble a slice), clamps `lower`/`upper` to `[1,
  len(elems)]`, and returns `{}` when `lower > upper` — matching PG's
  `array_ref` (`arrayfuncs.c`): out-of-range and reversed bounds are normal,
  not `NULL` and not an error.
- **Sibling traversal sites** (analyzer type-checking, CHECK-constraint
  column collection, partition-DEFAULT-expr validation, `ON CONFLICT`
  column collection, PL/pgSQL expression lowering) all previously called
  `analyzeExpr(x.Index, ...)` / `lowerPLpgSQLExpr(x.Index, ...)`
  unconditionally — safe for plain subscripts (`Index` always set) but a nil
  dereference (parser) or nil-interface panic (analyzer, confirmed live: a
  `runtime error: invalid memory address or nil pointer dereference` inside
  `analyzeExpr`, `analyzer.go:1634`) once a bound can be omitted. Each site
  now special-cases `IsSlice` and skips a nil bound; the PL/pgSQL lowering
  path additionally lowers slice reads to `array_slice` instead of forcing
  them through the scalar `array_subscript` path.

### Regression test

`TestArraySlice` (`internal/executor/array_slice_test.go`): both bounds,
either bound omitted, both omitted, reversed bounds, out-of-range bounds
(negative and beyond-length), single-element slice, nesting
(`arr[2:4][1:1]`), slicing a parenthesized array expression, and a NULL
array input.

### Measured effect on `tuplesort.sql`

The array-slice statements now parse and evaluate; the case still fails
because the two statements that use slicing (`(array_agg(...))[0:5]` +
`percentile_disc(...) WITHIN GROUP (...)` + `rank(...) WITHIN GROUP (...)`
in the same target list) hit a **different, deeper** bug once past the parse
error: `ERROR: WITHIN GROUP types uuid and text cannot be matched` — an
ordered-set-aggregate argument-type-coercion gap, unrelated to slicing and
out of scope for this slice (ledgered separately below). Raw diff line count
is materially unchanged (473 → 477 — the `lex error` line was replaced by a
different, still-wrong `ERROR` line at the same two spots) but the class of
failure moved from "cannot parse the statement at all" to "parses and
executes past the array slice, fails on an unrelated ordered-set-aggregate
gap" — a precondition for buckets A/B/D/E ever becoming visible in this
case's diff, since none of them are reachable while every statement using
slice syntax aborts at parse time.

## Remaining buckets

Bucket A (index-vs-sort plan-shape preference for `ORDER BY ... LIMIT`),
bucket B (CLUSTER physical order + aborted-tuple retention, largest, no
contained slice), bucket D (backward cursor scroll semantics), and bucket E
(count-DISTINCT + mark/restore plan shape, overlaps Incremental Sort) are
REFACTOR-tier or unsized; see the deferral ledger. **Next slice:** none
identified as CONTAINED — the case stays PARKED per the standing M0134
pattern (see `.ralph/fix_plan.md`'s M0134-0079 entry and re-arm trigger).
