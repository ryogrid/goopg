# 0100-0005v: Multi-line `permutation` keyword in isolation spec parser

Status: accepted (2026-05-15)

## Problem

`internal/testport/framework/isolation.go::ParseIsolationSpec` required every
`permutation <tokens...>` line to fit on a single source line: the regex
`^permutation\s+(.+)$` mandated at least one whitespace plus one content
character after the keyword.  Upstream's
`postgres/src/test/isolation/specs/insert-conflict-specconflict.spec` uses the
bare-keyword form with indented continuation tokens and embedded `#` comment
lines:

```
permutation
   # acquire a number of locks ...
   controller_locks
   controller_show
   s1_upsert s2_upsert
   ...
```

The bare `permutation` line failed the regex, so the parser fell through and
treated the keyword line and every continuation as anonymous input.  Result:
no permutation was registered for the spec, and every declared step surfaced
as `unused step name: <step>` in the runner output — masking every real
diagnostic for the speculative-insert lock dance the spec exists to validate.

A secondary defect compounded the problem.  The continuation reader broke out
of the read loop as soon as it encountered an indented line whose trimmed
content was empty after comment stripping (`if !isIndented || stripped == ""
{ break }`).  Comment-only continuation lines therefore truncated the
permutation block at the first annotation, even if the keyword line had been
recognised.

## Fix

Two edits in `internal/testport/framework/isolation.go`:

1. `rePermutation` relaxed from `^permutation\s+(.+)$` to
   `^permutation(?:\s+(.+))?$`.  Bare `permutation` matches with `m[1] == ""`,
   single-line `permutation a b c` matches with `m[1] == "a b c"`.  The word
   boundary is preserved because the leading literal `permutation` must be
   followed either by whitespace or end-of-line — `permutationxyz` still
   fails.

2. The continuation reader splits the previously-combined break condition
   into two arms: non-indented lines push back and terminate the block
   (existing behaviour); indented-but-empty-after-comment-strip lines are
   skipped with `continue`.  Blank (zero-length) lines have `isIndented ==
   false` and still terminate the block, which is the correct upstream
   semantics — isolationtester's specparse uses a blank-line terminator.

`parsePermutationTokens("")` already returns an empty slice (it walks
`strings.Fields(raw)`), so the empty-content first line composes cleanly with
continuation token accumulation.

## Regression test

`TestParseIsolationSpecMultiLinePermutation` in
`internal/testport/framework/isolation_test.go` covers the layout from the
specconflict spec end-to-end:

- bare `permutation` keyword line,
- indented `#`-only comment line at the head of the block,
- mid-block `#`-only comment line between tokens,
- blank-line terminator,
- mixed coexistence with single-line `permutation "a" "c"` form on a
  follow-up block.

The test asserts both permutations are recognised and that comment lines do
not leak step-name tokens.

## Verification

```
go test -count=1 -run "^TestParseIsolationSpec" ./internal/testport/framework/
go test -count=1 -race ./internal/testport/framework/
```

`TestPort_IsolationInsertConflictSpecconflict` now advances past the parser
phase and surfaces the next real engine gap (CREATE FUNCTION attribute
ordering for `IMMUTABLE` without the `AS $$…$$` body — separate scope).
Previously the spec showed every step as `unused step name`; now it reports
a real run-error from the global setup, which is the correct upstream-format
diagnostic.  Adjacent isolation tests that already passed
(`InsertConflictDoNothing`, `InsertConflictDoUpdate`,
`LockCommittedUpdate`, `PartitionKeyUpdate1`, `PartitionKeyUpdate2`) remain
green.

## Out of scope

- CREATE FUNCTION attribute-after-body grammar (next gap for specconflict).
- The `# multi-line permutation comment` syntax used inside `permutation`
  blocks does not propagate into the runner's per-permutation echo; this
  matches upstream isolationtester which strips comments during parse and
  echoes only the recognised step names back to stdout.
