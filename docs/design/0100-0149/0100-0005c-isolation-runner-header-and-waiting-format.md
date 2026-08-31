# 0100-0005c — IsolationRunner step header & `<waiting ...>` format parity (M0100-0005)

- Status: accepted
- Date: 2026-05-15
- Supersedes: —

## Goal

Make `framework.IsolationRunner`'s rendering of step headers byte-identical to
upstream PostgreSQL `src/test/isolation/isolationtester.c` for both
inline-brace and brace-at-EOL spec layouts, so the residual diffs on the
`insert-conflict-do-update-{3,4}` family collapse on the output side.

Upstream emits a single literal echo of the step SQL:

```
step <name>: <raw SQL>\n
step <name>: <raw SQL> <waiting ...>\n    (when the step blocks)
```

That is, the SQL between the spec's `{` and `}` is appended verbatim after
`step <name>: `, and the `<waiting ...>` marker is suffixed onto the SQL's
final physical line — not placed on a new continuation line.

## Concrete shape required

`insert-conflict-do-update-4.out` (inline-brace spec — `step "insert1" {
INSERT INTO upsert VALUES (1, 11, 111)\n                  ON CONFLICT ... }`):

```
step insert1: INSERT INTO upsert VALUES (1, 11, 111)
                  ON CONFLICT (i) DO UPDATE SET k = EXCLUDED.k; <waiting ...>
```

`insert-conflict-do-update-3.out` (brace-at-EOL spec — `step "insert1" {\n
    WITH t AS (\n ... )\n    SELECT * FROM colors ORDER BY key; }`):

```
step insert1: 
    WITH t AS (
        INSERT INTO colors(key, color, is_active)
        VALUES(1, 'Brown', true), (2, 'Gray', true)
        ON CONFLICT (key) DO UPDATE
        SET color = EXCLUDED.color
        WHERE colors.is_active)
    SELECT * FROM colors ORDER BY key; <waiting ...>
```

In both cases there is exactly one `\n` at end of the rendered line; the
`<waiting ...>` marker sits on the SQL's final physical line; and no
continuation line is introduced by the runner.

## Prior shape (incorrect)

Before this slice:

- `flattenSQL` (in `internal/testport/framework/isolation_runner.go`) treated
  multi-line SQL as `"\n" + raw`. That introduced a forced leading newline on
  every multi-line step regardless of how the spec was indented.
- The blocked-step branch in `runPermutation` printed
  `"step %s: %s\n <waiting ...>\n"` for multi-line SQL, placing
  `<waiting ...>` on its own continuation line indented by one space — not
  the upstream layout.
- The brace-at-EOL parser branch of `readBlock`
  (`internal/testport/framework/isolation.go`) returned content without a
  leading newline, so brace-at-EOL specs and inline-brace specs were
  indistinguishable to the runner — the runner had to guess.

## Design

Push the layout decision into the parser, so the runner has a single
format string. Specifically:

1. **Parser-side marker for brace-at-EOL layout.**
   `readBlock` already preserves continuation-line indentation
   ([0100-0005b](0100-0005b-isolation-spec-continuation-indent.md)).  Extend
   it: when the opening `{` sits at end-of-line (the `rest` argument is
   empty or whitespace-only), prepend a leading `\n` to the joined block
   body. The runner's verbatim `"step %s: %s"` format then renders as
   `step <name>: \n<body>` for brace-at-EOL specs and
   `step <name>: <first line>\n<continuation>` for inline-brace specs —
   identical to upstream.

2. **Runner-side: single verbatim format.**
   - `formatStepOutput(name, sqlText, …, false)` writes
     `"step %s: %s\n"` with `sqlText = IsolationStep.SQL` raw. No
     `flattenSQL` indirection; no isMultiLine branching.
   - `formatWaitingStepHeader(name, sql)` (new helper) writes
     `"step %s: %s <waiting ...>\n"` with the same raw SQL. The blocked-
     step branch in `runPermutation` calls it.
   - `flattenSQL` is removed.

3. **Trailing-pad parity.**
   PQprint pads the rightmost result-set column with trailing spaces so
   the underline row aligns under the column header. `normalizeIsoOutput`
   already TrimRights every line on both the actual and expected side
   (`internal/testport/framework/isolation_runner.go::normalizeIsoOutput`),
   so the goopg side does not need to strip those pads — the diff already
   tolerates them.

## Why this layering

Putting the brace-at-EOL marker in the parser keeps the runner stateless:
the runner does not need to know which spec layout produced the SQL; it
just echoes the captured block.  This mirrors upstream isolationtester.c,
where the lexer captures the raw bytes between `{` and `}` (including any
opening newline) and the formatter emits them unchanged.

Storing the leading `\n` inside `IsolationStep.SQL` is safe for SQL
execution — PostgreSQL accepts arbitrary leading whitespace — and existing
oracle-test executors (`internal/testport/isolation_port_test.go`) feed
the SQL to lib/pq which forwards it unchanged.

## Regression pins

- `internal/testport/framework/isolation_test.go::TestFormatStepOutputMultiLineInlinesFirstLine`
  (3 cases: inline-brace multi-line, brace-at-EOL multi-line, single-line)
  asserts the rendered header line shape for all three SQL layouts.
- `internal/testport/framework/isolation_test.go::TestFormatWaitingStepHeader`
  (3 cases: same shapes with the `<waiting ...>` suffix) asserts the
  blocked-step format string and the suffix-on-final-line invariant.
- `internal/testport/framework/isolation_test.go::TestParseIsolationSpecClosingBraceOnOwnLine`
  updated to assert the new parser-side leading-`\n` semantics for
  brace-at-EOL specs.
- `TestParseIsolationSpecPreservesContinuationIndent` (unchanged) continues
  to pin the inline-brace continuation-indent preservation from
  [0100-0005b](0100-0005b-isolation-spec-continuation-indent.md).

## Verification

```
go test -race -count=1 ./internal/testport/framework/  # PASS
go build ./...                                         # clean
go vet ./internal/testport/...                         # clean
```

The full `TestPort_IsolationSuite` 21-spec sweep still depends on the
remaining runtime items listed in `.ralph/fix_plan.md` §M0100-0005
(range-partition `FOR VALUES FROM ... TO ...`, partition-key-update
trigger/FK syntax, advisory-lock snapshot refresh after wait, …) — this
slice closes only the output-side header/waiting-format diffs.
