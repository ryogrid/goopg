# 0119-0006 — `boolean` input from an unknown literal (`VALUES ('true')`)

Status: **landed 2026-08-10**
Milestone: M0119-0006 (deferral-ledger backlog consumption)
Source: deferral-ledger row 2026-08-10 — discovered while gating the
`int2`/`oid`/`bool`/`bytea`/`time` index-key slice (13th), whose E2E test had to
insert booleans **unquoted** to get past the codec.

## The defect

`INSERT INTO t(b) VALUES ('true')` into a `boolean` column raised

```
XX000: expected bool, got kind 3
```

`kind 3` is `KindString`. `encodeValuePG`'s bool arm (`internal/executor/codec.go`)
demanded `KindBool` strictly and had no `KindString` route, so every quoted
boolean was refused at storage-encode time.

This is not an exotic spelling problem. Upstream types a bare literal as
`unknown` and coerces it to the target column type through the type's input
function — here `boolin` (`postgres/src/backend/utils/adt/bool.c`). Quoting
booleans is what `pg_dump` archives, `COPY`-style loader scripts and most
hand-written fixtures do, so the whole class of them loaded on PG and failed on
goopg.

The array path inherited the same failure for free: `encodeArrayValuePG`
recurses into `encodeValuePG` once per element, and an element always arrives as
element **text**, i.e. `KindString`. A `boolean[]` column was therefore
unwritable by any spelling.

## Why bool alone

Auditing every `expected …, got kind %d` site in `encodeValuePG` (15 of them)
shows bool was the lone holdout: `int2`/`int4`/`int8`/`oid`/`pg_lsn` each have a
`KindString` arm routing through `coerceStringToInt64` / `parsePgLSN`,
`timestamp`/`date`/`time` parse the string before the `KindTime` check, `bytea`
routes through `byteaIn`, `uuid` requires a string. So the fix is one arm, not a
sweep — and the remaining strict arms (`oidvector`, `int2vector`, `xid`,
`char`) take kinds no bare literal produces.

## What upstream actually accepts

`parse_bool_with_len` accepts the unambiguous prefixes of `true`, `false`,
`yes`, `no`, plus `on`/`off`, plus `1`/`0`:

| result | spellings |
|---|---|
| true | `t` `tr` `tru` `true` `y` `ye` `yes` `on` `1` |
| false | `f` `fa` `fal` `fals` `false` `n` `no` `of` `off` `0` |

Two details are load-bearing and are asserted by the gate:

- **`o` alone is rejected.** It prefixes both `on` and `off`, which is exactly
  why upstream compares with `len > 2 ? len : 2` in the `'o'` case. `of` *is*
  accepted (it can only be `off`).
- **`1`/`0` only at length one** — `10` and `01` are errors, not truthy.

`boolin` wraps that with leading/trailing whitespace trimming and, on failure,
raises `ERRCODE_INVALID_TEXT_REPRESENTATION` (**22P02**) quoting the
**original, untrimmed** input.

## The change

`pgBoolIn(s string) (bool, bool)` in `internal/executor/codec.go` reproduces
`boolin` (trim + `parse_bool_with_len`) and is now the single source of truth
for the spelling table. Four sites carried their own copy of it before —
Hard-won Rule #2, sibling paths must change together; a fifth copy in the codec
would have been the fourth place to keep in sync:

| site | file | role |
|---|---|---|
| `evalTypedStringLit` | `expr.go` | `BOOLEAN 'true'` typed literal |
| `evalCast` | `expr.go` | `'true'::bool`, `CAST(… AS bool)` |
| `isValidBoolInput` | `expr.go` | validity probe |
| `encodeValuePG` bool arm | `codec.go` | **new** — storage encode |

The codec arm keeps `KindBool` on its existing fast path, adds `KindString` via
`pgBoolIn`, and keeps the strict-Kind error for everything else. Notably it does
**not** add `KindInt`: `INSERT INTO t(b) VALUES (1)` is an error upstream too
(`column "b" is of type boolean but expression is of type integer`), so
accepting it would be a new divergence rather than a fix.

## Gates

- `TestPgBoolInMatchesParseBoolWithLen` — the whole spelling table, case and
  whitespace variants, and the rejections (`o`, `10`, `01`, `onn`, `truex`, …).
- `TestEncodeValuePGBoolAcceptsUnknownLiteral` — the encode arm directly: both
  polarities from `KindString`, the untouched `KindBool` path, and that invalid
  text raises `*ExecError` **22P02** quoting the original untrimmed input.
- `TestBoolColumnAcceptsQuotedLiteralEndToEnd` — the real INSERT path for a
  `boolean` **and** a `boolean[]` column, read back through a SELECT, plus a
  refusal case so the acceptance is not "anything goes".
- `TestScalarIndexBuildAndMaintainKeys/bool` — the 13th slice's E2E index test
  no longer needs its bool exception; `sqlLiteralForKeyType` now quotes every
  type, which is a second, independent user of the fixed path.

All confirmed non-vacuous by one source mutation (disabling the new
`KindString` arm), which reproduced the exact reported
`XX000: expected bool, got kind 3` in three of them.

## Deferred

`pgBoolIn` trims with Go's `strings.TrimSpace` (Unicode whitespace) where
`boolin` trims with C `isspace` (ASCII). A value like `" true"` is accepted
here and rejected upstream (ASCII space/tab/newline padding behaves identically
on both). Pre-existing in the two `expr.go` sites and not introduced by this
slice; recorded as
a ledger row rather than fixed here, since the same ASCII-vs-Unicode trim
question applies to several other input functions and deserves one answer.
