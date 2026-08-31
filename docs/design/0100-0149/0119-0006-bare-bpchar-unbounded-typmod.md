# M0119-0006 (58th slice) — a bare `bpchar` is unbounded, and its blanks are data

Successor to `0119-0006-bpchar-declared-width.md` (57th slice), which closed the
four *render* boundaries and filed this as its second deferral. Where that slice
was about a width goopg failed to RESTORE, this one is about a width goopg
invented: a `bpchar` column with no length modifier was being held to
`character(1)`.

## The defect

`internal/executor/codec.go`'s `coerceTextLikeDatum` is the single place the
declared width of a `char`/`character`/`bpchar`/`varchar` value is enforced on
the way into storage. Its `char` arm opened with

```go
n := 1
if len(t.Args) > 0 {
    n = int(t.Args[0])
}
```

so an `Args`-less type meant `character(1)`, and `INSERT INTO t(c bpchar)
VALUES ('abc')` raised `22001 value too long for type character(1)`.

The implicit length of 1 is real, but it belongs to the **grammar, not the
type**. Upstream's `CHARACTER opt_charset` production (`gram.y`) reduces bare
`char`/`character` to bpchar *with typmod 1*, which
`internal/parser/ddl.go`'s `parseColumnType` already mirrors (it synthesises
`Args = [1]` for an unquoted, typmod-less `char`). The internal type name
`bpchar`, spelled directly, takes no such reduction: it arrives with typmod −1,
and `bpchar_input`'s `atttypmod < VARHDRSZ` arm
(`postgres/src/backend/utils/adt/varchar.c`) then sets `maxlen` to the value's
own length — no truncation, no error.

## Measurement (PG 18.3, port 65432)

```
CREATE TABLE t(a bpchar, b char, c character, d char(6));
```

| column | `atttypmod` | `format_type` | accepts `'abc'` |
|---|---|---|---|
| `a bpchar` | −1 | `bpchar` | yes |
| `b char` | 5 | `character(1)` | no |
| `c character` | 5 | `character(1)` | no |
| `d char(6)` | 10 | `character(6)` | yes |

So exactly one of the three typmod-less spellings is unbounded, and the length-1
default the other two rest on is what the fix has to carve `bpchar` out of —
not remove.

### The half the deferral row did not predict

The row's resume point was "gate the `n := 1` default on `tname != "bpchar"`",
which is correct as far as it goes. But the same arm also **trims** trailing
blanks, and that is wrong for the unbounded case:

```
INSERT INTO t(a, d) VALUES ('ab  ', 'ab');
SELECT octet_length(a), octet_length(d);   -- PG: 4, 6
```

A width-carrying `bpchar` may be stored trimmed because every render boundary
re-pads it from `Args[0]` via `catalog.PadBpchar` — that is the 57th slice's
whole mechanism, and the trimmed convention is load-bearing (M0103-0007 rung 24:
`compareDatum`'s padding-insensitive equality and the compact heap image both
rest on it). An **unbounded** `bpchar` has no width to re-pad from —
`PadBpchar` returns an `Args`-less value unchanged — so trimming it does not
defer the blanks, it destroys them. Unbounded values are therefore stored
verbatim, and the trimmed convention is untouched for everything else.

## The precondition the row demanded, and its answer

The row required a check before the gate could be trusted: *does any DDL path
canonicalise a `character(N)` column's `Type.Name` to `"bpchar"` with the `Args`
intact?* If one did, the gate would unbound those columns too. Answer: the
canonicalisation happens, and it is safe, because the width survives it.

- **CREATE TABLE** copies the parsed args verbatim —
  `catalog.Type{Name: typeName, Args: append(nil, c.Type.Args...)}`
  (`internal/executor/operators_ddl.go:1864`), so `character(3)` is stored as
  Name `character`, `Args` `[3]`.
- **Heap reload** reconstructs the column from `pg_attribute` as
  `catalog.OIDToTypeName(1042)` = `"bpchar"` — the rename does occur — but with
  `Args: pgTypeArgsFromTypmod(1042, atttypmod)`, which returns `[typmod − 4]`
  for any `typmod >= 4` (`internal/initdb/catalog_heap_reload.go:1240`,
  `internal/initdb/open.go:3093`). A restored `char(3)` therefore arrives as
  Name `bpchar` with `Args` `[3]`.

Empty `Args` on a `bpchar` can consequently only mean typmod −1, which is
exactly the case being unbounded. This reasoning is recorded at the call site,
because it is the thing that makes the gate sound.

## Sibling audit (Hard-won Rule #2)

| path | verdict |
|---|---|
| `catalog.PadBpchar` (render, all four boundaries) | already correct — returns an `Args`-less value unchanged, i.e. no padding for typmod −1 |
| `expr.go` explicit-cast typmod truncation | already correct — guarded by `x.Typmod > 0`, so `'abc'::bpchar` is untouched; verified against PG (`'abc'`, `octet_length` 3) |
| `parser`'s `synthesizeBareCharTypmod` / `parseColumnType` | already correct — both synthesise `[1]` for `char` only, never for `bpchar` |
| `validateTypedLen` (`pg_input_error_info`) | **was wrong, fixed here** — see below |

`validateTypedLen` is `pg_input_error_info`'s private copy of the same
declared-length rule. The 57th slice converted `coerceTextLikeDatum` from a byte
count to a rune count and left this sibling on bytes, so the two answered the
same question differently:
`pg_input_error_info('あいうえお','varchar(5)')` reported 22001 where PG 18.3
returns no error (5 characters, 15 bytes). Both of its arms now count runes.
Its bare-`bpchar` behaviour needed no change — it only matches spellings that
carry an explicit `(N)`.

## Gates

`go build ./...` clean; targeted `internal/executor` tests PASS;
`TestPort_RegressSuite` PASS (1045 s, `-timeout 40m` — the default 600 s kills
it with a goroutine dump that reads like a hang); `RALPH_PRECOMMIT_SCOPE=units`
PASS; `scripts/tpch-spotcheck.sh` RESULT=PASS (Q12=2, Q13=35 canonical — TPC-H
`lineitem` carries `char(1)`/`char(10)` columns, so this gate is load-bearing
for a `bpchar` coercion change); TPC-DS SF0.5 sweep; pgbench smoke via the
commit hook.

Both new guards were verified red on the pre-fix source before being accepted
(`TestCoerceTextLikeDatumUnboundedBpchar`: 4 failing assertions;
`TestValidateTypedLenMeasuresCharactersNotBytes`: 3).

E2E on a throwaway goopg cluster (port 5533) against PG 18.3 (port 65432): the
`INSERT` is accepted, `octet_length` is 3 for `'abc'` in a bare `bpchar`, and
`COPY … TO STDOUT` is byte-identical across both engines for a row holding
`'ab  '` in a `bpchar` and `'ab'` in a `char(6)` (`ab  \tab    `).

## Deferred

- `pg_input_error_info(v, t)` returns **zero** rows for valid input where PG
  returns **one** row with all four columns NULL. Measured this loop; it affects
  every type the SRF validates, not just the width family, so it needs its own
  audit of the `pg_regress` expected files that consume it (M0097-0003) rather
  than riding this commit.
- `validateTypedLen` matches its type by TEXT PREFIX (`varchar(`, `char(`, …)
  rather than by resolved type, so `pg_catalog.varchar(5)`, a domain over
  `varchar(5)`, or whitespace before the `(` silently validate nothing.
- Carried forward unchanged from the 57th slice: `octet_length()`/`bit_length()`
  still answer from the trimmed image for a width-carrying `bpchar`, and the
  heap image itself stays trimmed by design.
