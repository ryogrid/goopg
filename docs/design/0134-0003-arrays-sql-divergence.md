# M0134-0003 `arrays.sql` divergence map + S1 `LIKE`-family `ANY`/`ALL`

Status: **accepted** (S1 landed 2026-08-18; the umbrella case stays open and is
**PARKED** as a whole — see "Verdict on the umbrella case")

Milestone: M0134 (regress-sql `failed`/`not-tried` digestion), case
`postgres/src/test/regress/sql/arrays.sql`.

## Measurement

All numbers re-measured 2026-08-18 at branch `dir-compat`. **Every
`arrays`/regress baseline recorded before 2026-08-18 predates the C19 harness
fix** (`scripts/pg-regress-runner.sh` now passes
`-v HIDE_TABLEAM=on -v HIDE_TOAST_COMPRESSION=on`, matching
`postgres/src/test/regress/pg_regress_main.c:74-79`) — never compare against an
older recorded number for this case.

| point | diff lines | hunks | result |
|---|---|---|---|
| pre-S1 baseline | 3311 | 24 | FAIL |
| post-S1 | **3251** (−60) | 25 | FAIL (umbrella) |

The hunk count rising 24 → 25 is a boundary shift, not a new divergence: closing
the `LIKE`-family block split one hunk's context run in two. Sentinel case `char`
unchanged at 172 lines.

## Divergence classes

Bucketed hunk-by-hunk (research:
`tmp/ralph-handoffs/m0134-0003-s01-measure/report.md`, incl. its "Follow-up:
bucket D precision pass"). 100% of `+ERROR` lines classified.

| class | ~lines | root cause | verdict |
|---|---|---|---|
| **A** — slice subscripting `a[lo:hi]` entirely unsupported, read *and* write; the lexer never tokenizes `:` inside `[...]` and `ArraySubscriptExpr` (`internal/parser/select.go:1883`) carries a single `Index` with no bound pair | ~900 | no slice-subscript representation anywhere in parser or executor | MILESTONE-SCALE |
| **B** — assignment-*target* indirection unsupported: `UPDATE … SET col[i] = …` and `SET (col[1:5], …) = …`; `parseAssign` (`internal/parser/dml.go:462`) accepts only a bare identifier left of `=`, so even plain (non-slice) index targets fail | ~250 | coupled to A — a shared target-indirection representation | MILESTONE-SCALE |
| **C** — 13 array builtins registered in the catalog but with no executor dispatch case (`array_sort`, `string_to_array`, `string_to_table`, `array_position`, `array_positions`, `array_replace`, `array_reverse`, `array_sample`, `array_shuffle`, `trim_array`, `cardinality`, `width_bucket`); e.g. the switch at `internal/executor/expr.go:8902`, `operators_call.go:734` | ~600 | each function independently missing | MILESTONE-SCALE as a set; each function is a self-contained FRAGMENT |
| **D** — `op ANY/SOME/ALL (array)` not wired for the `LIKE` family | 60 | **S1, LANDED — see below** | BOUNDED-SLICE ✔ |
| **D′** — `op ANY/ALL` with a *non-boolean* operator (`select 33 * any ('{1,2,3}')`, `select 33 * any (44)`) must raise PG's `op ANY/ALL (array) requires operator to yield boolean` / `… requires array on right side`; goopg's bool-guard in `evalInExpr` silently no-ops | ~10 | no semantic-analysis pass performs this check anywhere | FRAGMENT, but **no in-tree precedent** — split out, ledgered |
| **E** — misc singles (COPY rejects a NULL array, TOAST size reporting, cascaded fixture/relation errors) | ~40 | mostly downstream of A/B | FRAGMENT / not independent |

### A correction to carry forward

The first-pass classification described class D as "ANY/ALL only wired for
comparison operators; PG allows **any** binary operator". The general statement
is true of PG, but it is **not** what this case exercises. A precision pass over
`arrays.sql` found the only ANY/ALL operators that actually fail here are the
`LIKE` family (lines 463-470) and `*` (lines 395-396). `=`, `>`, `>=` already
parse at HEAD; **no** `@>`, `<@`, `&&`, `~`, or `IS [NOT] DISTINCT FROM` appears
combined with ANY/ALL anywhere in the file. Scoping S1 to the `LIKE` family was
therefore not a narrowing of class D — it *is* class D, minus D′. Do not re-open
this as "generalize ANY/ALL to arbitrary operators" on the strength of this case;
that would be a speculative change with no witness in the corpus.

## S1 — `[NOT] LIKE|ILIKE ANY|SOME|ALL (…)` (LANDED)

### The gap was a sibling-path omission, not a missing feature

`parseExprPrec` (`internal/parser/select.go`) handles quantified operators in
three adjacent operator groups, and only two of them had the wiring:

- the **ordering comparisons** (`< > <= >= != <>`, ~2174-2213) check
  `isAnyOrSomeTok`/`isAllTok` and desugar to `InExpr{AnyOp, AllOp}` (M0122-0004);
- the **POSIX regex** operators (`~ ~* !~ !~*`, ~1984-2016) do the same
  (M0097-0068);
- the **`LIKE` family** (`LIKE`, `NOT LIKE`, `ILIKE`, `NOT ILIKE`, ~1936-1980)
  did **not** — each block unconditionally called
  `p.parseExprPrec(precCompare + 1)` and built a plain `BinaryOp`.

So `'foo' like any (array['%a','%o'])` parsed `any` as a function call and failed
with `ERROR: function any does not exist`. This is the "sibling paths must change
together" failure mode from `CLAUDE.md` in its purest form: three groups that
must agree, one silently left behind when quantifier support was added.

### The executor needed no change — verified, not assumed

`evalInExpr` (`internal/executor/expr.go:6839-6885`) dispatches `x.AnyOp`
through the **general** binary evaluator, `evalBinary(x.AnyOp, operand, v, 0,
ctx)` (`:6859`, `:6877`) — not a comparison-only compare-and-test-sign helper.
`evalBinary` already implements the whole family at
`internal/executor/expr.go:1684-1699` and returns `KindBool`. The four opcodes
(`OpLike`, `OpNotLike`, `OpILike`, `OpNotILike`, `internal/parser/op.go:47-50`)
already existed. Hence: **parser-only**, and the brief instructed the implementer
to escalate rather than edit the executor if that proved false. It did not.

This generality is also exactly why D′ is a separate problem: the same dispatch
happily accepts a non-boolean opcode like `OpMul` and the bool-guard falls
through silently instead of erroring.

### Change

Four blocks in `parseExprPrec`, each mirroring the regex block's structure: after
consuming the operator (and `NOT` first, for the two negated forms, keeping `pos`
at the `NOT` token as those blocks already did), check
`isAnyOrSomeTok(p.cur()) || isAllTok(p.cur())`; record `allQuant`, advance past
the quantifier, call `p.parseAnyTail(left, pos)`, and set `ie.AnyOp` to the
group's opcode and `ie.AllOp = allQuant`. Otherwise fall through unchanged to the
plain `BinaryOp` path, so non-quantified `LIKE`/`ILIKE` is untouched.

`SOME` is accepted wherever `ANY` is — `isAnyOrSomeTok` already covers both, per
PG's treatment of them as synonyms.

Note the negated forms compose correctly by construction: `x NOT LIKE ALL (arr)`
becomes `AnyOp=OpNotLike, AllOp=true`, which `evalInExpr` ANDs element-wise —
"for every element, x is NOT LIKE it" — matching PG.

### PG oracle

PG has no special case here at all, which is the point. `LIKE` is spelled `~~`
and `op ANY (array)` is a fully general `ScalarArrayOpExpr`:
`postgres/src/backend/parser/parse_expr.c` `transformAExprOpAny` /
`transformAExprOpAll` → `make_scalar_array_op`
(`postgres/src/backend/parser/parse_oper.c`), which resolves the operator and
requires only that it yield boolean. goopg reaches the same result through a
per-operator-group desugaring, which is why the groups must be kept in sync by
hand — a structural hazard worth remembering when a fifth group is added.

### Tests

- `internal/parser/any_all_test.go` — `TestParseLikeFamilyAnyAll`, 12
  table-driven cases (4 operators × {ANY, SOME, ALL}), asserting the parsed node
  is an `*InExpr` with the expected `AnyOp` opcode and `AllOp` flag. Extends the
  file that already pins this desugaring for the other two operator groups, so
  all three now sit under one guard and cannot drift apart again.
- `internal/executor/any_all_test.go` — `TestLikeFamilyAnyAllEvaluation`, the 8
  statements of `arrays.sql:463-470` verbatim with PG's expected results, plus
  the `SOME` synonym and `NOT ILIKE ANY/ALL`. Placed beside
  `TestAnySomeAllOrderingOperators`, the existing executor-level ANY/ALL
  coverage.

### Gates

`go test ./internal/parser/` PASS · `go test ./internal/executor/` PASS ·
`RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` PASS ·
`scripts/pg-regress-runner.sh arrays` 3311→3251 lines · sentinel
`scripts/pg-regress-runner.sh char` PASS, 172 lines unchanged. No TPC-H/TPC-DS
gate — parser-only, no planner/executor/codec change.

## Verdict on the umbrella case: PARKED

After S1, classes A + B alone are ~1150 coupled lines (slice subscripting read
and write share one representation and cannot land separately), and class C is a
13-function set. Nothing bounded remains. `arrays.sql` therefore joins
M0134-0001 and M0134-0002 as a **parked** umbrella case: the M0134-0003 fix_plan
item stays unchecked and not-selectable, and the CSV row stays `failed`.

**Re-arm trigger:** array slice subscripting (classes A+B) landing as its own
milestone, or class C's builtin set being filled in — then re-measure from
scratch.

The cheapest genuine follow-ons, if small wins are wanted from this file without
re-arming the umbrella, are individual class-C functions that are thin wrappers
over existing array-decode helpers (`cardinality`, `array_reverse`,
`trim_array`), each a standalone one-function ticket.

## Deferrals

Recorded in `.ralph/deferral_ledger.md` (2026-08-18): class D′ (non-boolean
operator under `ANY`/`ALL` must raise `op ANY/ALL (array) requires operator to
yield boolean` / `requires array on right side`; goopg's `evalInExpr` bool-guard
silently no-ops), and the parked classes A/B/C/E.
