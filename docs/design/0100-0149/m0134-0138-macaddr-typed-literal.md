# M0134-0138 — `macaddr.sql` macaddr_in-faithful parsing (PARKED)

**Date:** 2026-08-24
**Status:** contained fix landed, PARKED — different closure shape than the
box/circle/line/lseg precedent (M0134-0094/-0098/-0136/-0137): this file's
remaining diff is dominated by the already-ledgered btree-opclass-generality
gap, not the LINE-position-echo gap alone.

## Result

`scripts/pg-regress-runner.sh macaddr` against PG 18.3: diff went from
**179 lines (0% parity)** to **33 lines**, with **zero residual
`^ERROR`/`^-ERROR` mismatches**. Every remaining diff line is one of two
already-tracked gaps (see "Not fixed this loop").

## Root cause

`macaddr` had **no executor support at all** — not even a raw-varlena
pass-through wrapper the way box/circle/line/lseg had before their M0134
fixes. The only trace of the type in `internal/executor` was its OID/type-name
mapping. Consequently:

- Any string, well-formed or not, was accepted and stored verbatim: distinct
  spellings of the same address (`'08-00-2b-01-02-03'` vs
  `'08:00:2b:01:02:03'`) compared *unequal*, and garbage like `'not even
  close'` inserted without error.
- `~b` / `b & mac` / `b | mac` fell through to the integer-only bitwise
  operator path and raised `operator ~/&/| requires integer operand(s)`.
- `trunc(macaddr_col)` fell through to the numeric `trunc()` path, failed
  `ParseFloat`, and silently returned `NULL` for every row.
- `pg_input_is_valid`/`pg_input_error_info('...', 'macaddr')` always reported
  valid (fell to the `default` "unregistered type" arm).

## What landed

1. **`parseMacaddrLiteral`** (`internal/executor/expr.go`) — a faithful port
   of `macaddr_in` (`postgres/src/backend/utils/adt/mac.c:55-114`). Upstream
   tries 7 `sscanf` format strings in order until one matches all 6 octets
   with no trailing garbage:

   | # | format | field width | separators |
   |---|--------|-------------|------------|
   | 1 | `%x:%x:%x:%x:%x:%x` | unbounded | `:` ×5 |
   | 2 | `%x-%x-%x-%x-%x-%x` | unbounded | `-` ×5 |
   | 3 | `%2x%2x%2x:%2x%2x%2x` | 2 | `-`/`:` at position 3 only |
   | 4 | `%2x%2x%2x-%2x%2x%2x` | 2 | `-` at position 3 only |
   | 5 | `%2x%2x.%2x%2x.%2x%2x` | 2 | `.` at positions 2,4 |
   | 6 | `%2x%2x-%2x%2x-%2x%2x` | 2 | `-` at positions 2,4 |
   | 7 | `%2x%2x%2x%2x%2x%2x` | 2 | none |

   goopg reduces this to a `macScanCandidate{width int, seps [5]byte}` table
   (`macScanCandidates`) and one shared scanner (`macScanOne`) that mirrors C
   `sscanf`'s per-`%x`-conversion leading-whitespace skip and the trailing
   `%1s` junk check (any non-whitespace character left over after the 6th
   field means the candidate's overall match count would have been 7, not 6,
   so upstream rejects it too). Only the two **unbounded** colon/dash forms
   can produce an out-of-range octet (`%2x` is capped at `0xff` by
   construction) — `parseMacaddrLiteral` range-checks all 6 octets
   post-match and raises PG's distinct `22003`
   (`ERRCODE_NUMERIC_VALUE_OUT_OF_RANGE`) "invalid octet value" message,
   separate from the generic `22P02` "invalid input syntax" all-candidates-
   failed case.

2. **`macaddrCanonicalText`** — `macaddr_out`'s fixed
   `"%02x:%02x:%02x:%02x:%02x:%02x"` format.

3. **Wiring**, mirroring the box/circle/line/lseg/inet chokepoint pattern:
   - `internal/executor/codec.go` `coerceTextLikeDatum` — a `macaddr` column's
     assignment coercion now validates + canonicalizes.
   - `pg_input_is_valid`/`pg_input_error_info` (`expr.go` +
     `operators_pg_input_error_info.go`) — new `"macaddr"` cases.
   - No `macaddr '...'` typed-literal arm added (`macaddr.sql` doesn't
     exercise that cast spelling, only plain-string coercion) and no
     `macaddr`/`macaddr8` parser-whitelist entry — out of scope this loop.
     `macaddr8` (the 8-octet EUI-64 variant) is untouched entirely; this file
     only exercises 6-octet `macaddr`.

4. **Bitwise operators** (`macaddr_not`/`macaddr_and`/`macaddr_or`, mac.c:
   287-334) — `~`, `&`, `|` on a macaddr value XOR/AND/OR each of the 6
   octets independently. goopg's `Datum` carries no static type tag beyond
   `Kind` (`KindString` covers every text-backed user type), so there is no
   way to know at the operator dispatch site that an operand is specifically
   `macaddr` rather than arbitrary text — the same limitation the existing
   `point << point` / `point >> point` detection already lives with. The fix
   follows that established pattern exactly: `evalUnary`'s `OpBitNot` arm and
   the shared `OpBitAnd`/`OpBitOr` binary-operator arm try
   `parseMacaddrLiteral` on a `KindString` operand *before* falling through to
   the integer-only error; a string that doesn't parse as a macaddr (the
   overwhelming majority of `KindString` values in practice) falls through
   unchanged. `macaddr.sql` never exercises `~`/`&`/`|` against non-macaddr
   text so no regression surface was found (confirmed by the box/circle/
   line/lseg/point/inet re-check below).

5. **`macaddr_trunc`** (mac.c:341-356, `trunc(macaddr)`) — zeroes the last 3
   octets ("mask to the vendor prefix"). Same text-shape-sniffing pattern:
   `trunc()`'s existing numeric-only implementation gets a `parseMacaddrLiteral`
   probe inserted before its `ParseFloat` fallback.

## Not fixed this loop

The residual 33-line diff has two independent, both already-ledgered,
components:

1. **psql LINE-position echo** (rows 8/9's `-- invalid` INSERTs) — the same
   box/circle/line/lseg-shared gap: `coerceTextLikeDatum` never attaches
   `ExecError.Pos`, so psql's client-side `"LINE N: ...\n  ^"` echo never
   fires. Resume point unchanged from the M0134-0094/-0098/-0136/-0137 rows.

2. **`CREATE INDEX ... USING btree/hash (b)` on a macaddr column** — both
   raise `0A000 btree v0 only supports int4 / numeric keys, got "macaddr"`.
   This is the pre-existing, independently-ledgered (M0134-0060 `rangetypes.sql`,
   M0134-0067 `domain.sql`) btree-opclass-generality gap: goopg's btree v0 key
   encoder (`internal/executor/btree_scalar_keys.go`,
   `isSupportedBTreeKeyType` at `internal/executor/operators_ddl.go:15810`)
   hard-codes a fixed set of scalar types (int4/numeric/date/timestamp(tz)/
   interval/…) with no generic per-type comparator dispatch. Generalizing it
   to cover `macaddr` needs a `macaddrCmp`-shaped key encoder (the hi/lo
   `unsigned long` comparison from `macaddr_cmp_internal`, mac.c:181-194) —
   same shape as the already-landed `interval` slice (M0119-0006). Not
   attempted this loop: it is a large, cross-case architectural item that
   recurs across multiple still-`failed` M0134 files, not specific to
   macaddr.

Neither residual line represents a value-level macaddr semantics gap — every
comparison, bitwise operator, and `trunc()` result in the diff now matches PG
byte-for-byte.

## Verification

- `go build ./...` — PASS.
- `scripts/pg-regress-runner.sh --verbose macaddr` — 179→33 diff lines, 0
  residual `^ERROR`/`^-ERROR`.
- `scripts/pg-regress-runner.sh --verbose box circle line lseg point inet` —
  all held steady at their known baselines (722/51/55/27/531/1298 diff
  lines respectively): no regression from the new `KindString`-based macaddr
  detection in the shared `~`/`&`/`|`/`trunc()` code paths.
