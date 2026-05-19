# M0100-0005d — Isolation spec parser: preserve trailing `\n` when `}` is on its own line

## Problem

`merge-match-recheck.spec` (and similar multi-line specs) declare steps with
the closing brace on its own line:

```
step "merge_status"
{
  MERGE INTO target t
  ...
	UPDATE SET status = 's4', val = t.val || ' when3';
}
```

When such a step is blocked, upstream `isolationtester` emits

```
step merge_status: 
  MERGE INTO target t
  ...
  UPDATE SET status = 's4', val = t.val || ' when3';
 <waiting ...>
```

— note the `<waiting ...>` on a fresh line, with a single leading space.
goopg's runner emitted

```
step merge_status: 
  ...
  UPDATE SET status = 's4', val = t.val || ' when3'; <waiting ...>
```

— `<waiting ...>` appended to the last SQL line — which diverged from the
expected output of every `}`-on-own-line spec and held
`TestPort_IsolationMergeMatchRecheck` (and others) in `defer`.

## Root cause

Upstream `specscanner.l` captures the body of `{ ... }` verbatim, including
any embedded newlines. Its `{space}` character class is `[ \t\r\f]` — it
does **not** include `\n`. So:

- `"{"{space}*` opens the block, eating only horizontal whitespace after `{`.
- `<sql>{space}*"}"` closes the block, eating only horizontal whitespace
  before `}`.
- Any `\n` between the last SQL byte and `}` is kept in the captured buffer.

For a block whose `}` sits on its own line, the captured `step->sql` ends
with `\n`. The runner then prints `step %s: %s <waiting ...>\n` — which,
because the SQL already terminates with `\n`, renders `<waiting ...>` on a
new line, with a single leading space from the format string.

goopg's `readBlock` (`internal/testport/framework/isolation.go`) dropped the
closing-brace line entirely when it contained only whitespace, and the
step-assignment site did `strings.TrimRight(body, " \t\n\r")`, so the
trailing `\n` was always stripped.

## Fix

Two changes in `internal/testport/framework/isolation.go`:

1. **`readBlock`** — when the loop breaks on the `}` line and that line had
   only whitespace before the `}` (i.e. the closing brace is on its own
   line), append a single `\n` to the joined body. Inline-closing-brace
   layouts (`step name { ...sql...; }` and
   `step name { sql-line-1\n  sql-line-2; }`) still produce a body without a
   trailing `\n`, preserving the previously-correct
   `insert-conflict-do-update-4` shape.

2. **Step assignment in `ParseIsolationSpec`** — drop the
   `strings.TrimRight(body, " \t\n\r")` call. The body returned by
   `readBlock` is now exactly what should land in `IsolationStep.SQL`:
   leading `\n` for brace-at-EOL, trailing `\n` for `}`-on-own-line.

## Why this is harness-only

Server-side wire behavior is unchanged. This is purely the harness's
spec-to-output transform aligning with upstream `isolationtester` byte for
byte.

## Regression coverage

`internal/testport/framework/isolation_test.go`:

- `TestParseIsolationSpecClosingBraceOnOwnLine` — updated. SQL string now
  ends with `\n` (was: no trailing whitespace). Documents the layout-to-output
  mapping.
- `TestParseIsolationSpecPreservesContinuationIndent` — unchanged. Inline
  closing-brace layout still yields no trailing `\n`. Pins
  `insert-conflict-do-update-4` parity.
- `TestFormatWaitingStepHeader` — gained a `brace_at_eol_close_own_line`
  case that feeds `"\n  MERGE INTO target t\n\tUPDATE SET status = 's4';\n"`
  through `formatWaitingStepHeader` and asserts the output puts
  `<waiting ...>` on a fresh line with a single leading space, matching
  `merge-match-recheck.out`.

## Scope note

Fixing `<waiting ...>` placement does **not** flip
`TestPort_IsolationMergeMatchRecheck` to pass — that test still fails on
real-feature gaps (MERGE matched-AND-recheck semantics; partition routing
on UPDATE; trigger NOTICE interleaving). But every `}`-on-own-line spec
in the 21-test target now produces the upstream-shaped output for waiting
steps, removing this format diff from the list of differences the runner
has to overcome before those tests can pass.
