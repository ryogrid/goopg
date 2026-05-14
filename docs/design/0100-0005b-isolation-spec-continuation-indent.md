# 0100-0005b — Isolation Spec Parser: Preserve Continuation-Line Indentation

**Status:** accepted
**Milestone:** [M0100 — RC Isolation Test Suite Completion](../milestones/0100-rc-isolation-suite-pass.md) (M0100-0005)
**Slice cousin:** [0100-0005a — IsolationRunner BOOL wire-text reversal](0100-0005a-isolation-runner-bool-wire-text-reversal.md)
**Cross-links:**
[0096-0001 — RC isolation runner harness](0096-0001-rc-isolation-runner.md).

## Context

Upstream PostgreSQL `isolationtester` echoes multi-line step SQL into
each permutation's expected output **verbatim**, with one cosmetic
adjustment: the first line of the SQL is concatenated to the
`step <name>: ` header.  Every continuation line is printed exactly
as it appears in the spec file, leading whitespace included.  This
shape is what every `postgres/src/test/isolation/expected/*.out`
file commits to disk.

Example, from
`postgres/src/test/isolation/expected/insert-conflict-do-update-4.out`:

```
step insert1: INSERT INTO upsert VALUES (1, 11, 111)
                  ON CONFLICT (i) DO UPDATE SET k = EXCLUDED.k; <waiting ...>
```

Note the 18 leading spaces on the continuation line — they come
straight from the corresponding spec source line.

## Bug

`readBlock` in `internal/testport/framework/isolation.go` walks a
multi-line `{ ... }` step body line by line.  Interior lines (those
strictly between the opening-`{` line and the closing-`}` line)
correctly preserved leading whitespace via the `lines = append(lines,
next)` branch.  The line that contains the closing `}`, however,
went through `strings.TrimSpace(next[:idx])`, which erased the
leading whitespace from any SQL content that happened to live on the
same line as the closing brace.

For specs that close the brace on its own line (`}` alone) the
TrimSpace was a no-op, so the bug was invisible.  Specs that use
the compact inline form — common in the
`insert-conflict-do-update-*` family — silently lost their
continuation indentation:

```text
spec body:
  INSERT INTO upsert VALUES (1, 11, 111)
                    ON CONFLICT (i) DO UPDATE SET k = EXCLUDED.k; }

parsed (before fix):
  INSERT INTO upsert VALUES (1, 11, 111)
  ON CONFLICT (i) DO UPDATE SET k = EXCLUDED.k;       ← lost 18 spaces
```

The harness then emitted the stripped form into actual output and
the per-line diff against the upstream `.out` file always failed.

## Fix

`readBlock` now distinguishes the two roles a `}`-bearing line can
play.

* If the line is **content + `}`** (e.g. `... EXCLUDED.k; }`):
  preserve everything before `}`, trim only trailing whitespace, and
  append the result as a normal content line.  Leading whitespace is
  kept.
* If the line is **`}` alone** (or whitespace + `}`): drop it — there
  is no content to append, and isolationtester does not emit a
  whitespace-only continuation line in this position.

Implementation site:
`internal/testport/framework/isolation.go::readBlock`.

The first line (the same line as the opening `{`) keeps its existing
`TrimSpace` treatment — the leading space after `{` is just a
separator before the SQL starts, not significant indentation.
This matches the upstream output where the first line is
concatenated to `step <name>: ` without any extra padding.

## Regression Pins

`internal/testport/framework/isolation_test.go`:

* `TestParseIsolationSpecPreservesContinuationIndent` — feeds the
  exact `insert-conflict-do-update-4`-style inline-brace shape into
  `ParseIsolationSpec` and asserts the 18-space continuation
  indentation survives.  This is the test that would have caught
  the bug had it existed when M0096-0001 first landed.
* `TestParseIsolationSpecClosingBraceOnOwnLine` — pins the
  brace-on-own-line shape (the common upstream style) so a future
  refactor cannot regress it: every content line keeps its
  indentation, the trailing `}` line is dropped, no whitespace-only
  stragglers appear in the parsed SQL.

Both tests live next to the existing `TestParseAndRunIsolationPermutation`
and run with `go test ./internal/testport/framework/`.

## Non-goals

This slice fixes the *parser* — `IsolationStep.SQL` now carries the
correct bytes.  Three further harness gaps remain before the
inline-brace specs reach `pass`:

1. **First-line inlining in the step header.**
   `formatStepOutput`'s `flattenSQL` currently emits multi-line SQL
   as `step <name>: \n<full body>\n`.  Upstream's shape is
   `step <name>: <first-line>\n<continuation lines>`.  Fixing this
   is a single-site change in `isolation_runner.go` but interacts
   with the existing `<waiting ...>` placement logic, so it lands as
   a separate focused slice.
2. **`<waiting ...>` placement on multi-line statements.**
   When a multi-line statement blocks, upstream appends
   `<waiting ...>` to the **last** line of the SQL, not the header.
   `isolation_runner.go` already has the single-line variant; the
   multi-line branch needs to thread the suffix onto the final
   continuation line.
3. **Column width parity.**
   `pqprintFormat` derives column widths from the longer of the
   header or the longest data value.  Upstream's `PQprint`
   pads data values to a width that includes a single trailing
   space when the column is the widest in the row; this differs
   from `pqprintFormat`'s current "no trailing pad" behaviour.

These are tracked under M0100-0005's overall E2E pass goal in
`.ralph/fix_plan.md`.
