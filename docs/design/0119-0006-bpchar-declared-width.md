# M0119-0006 (57th slice) — a `bpchar` value loses its declared width at every render boundary

status: accepted
date: 2026-08-13
milestone: M0119-0006 (pg_amcheck server tier — the binary-`COPY` type chain)
supersedes-resume-point: the 56th slice's `bpchar` deferral row
(`.ralph/deferral_ledger.md`, 2026-08-13)

## What the resume point said, and why it was wrong twice

The 56th slice closed `jsonb` and named `bpchar` as the last type in the
binary-`COPY` chain, with this resume point:

> Upstream `bpcharsend` IS `textsend` (`postgres/src/backend/utils/adt/
> varchar.c`), so the bytes are accidentally right — but `bpchar_recv` applies
> `bpchar_input`'s BLANK PADDING to the declared typmod, which goopg's decode
> does not […] That padding needs the column typmod, i.e. the SAME
> `copyBinaryToDatum` signature widening the 49th/51st/55th slices'
> `AdjustTimeForTypmod`/`AdjustIntervalForTypmod` rows are blocked on.

Both halves were refuted before any code was written, by ten minutes of
measurement against a throwaway PG 18.3 (`initdb` out of
`postgres/local_install`, `pg_ctl -o "-p 5539 -k $D -h ''"`).

**The bytes are not accidentally right.** `bpcharsend` being `textsend` is
true and is exactly the problem: what `textsend` ships is the *stored image*,
and upstream stores a `bpchar` blank-padded to its declared width where goopg
stores it trimmed. On a `char(10)` column holding `'ab'`, PG writes a
length-**10** binary field; goopg wrote **2**. The defect was on the *encode*
side, which the resume point had cleared.

**The signature widening was never needed.** `copyBinaryToDatum` already takes
a `catalog.Type`, and `ParseCopyBinaryRows` already passes `cols[i].Type` — a
`char(10)` column's `Type` carries `Args: []int64{10}`, which *is* the typmod.
Every consumer that wanted "the column's declared length" had it all along.
(That does not unblock the `Adjust*ForTypmod` rows, which are blocked on the
unported *functions*, not on plumbing; the rows are corrected in place.)

**And the defect was not COPY-local at all.** Measuring the same `char(10)`
column across every boundary found the same missing padding four times.

## Measurement

`CREATE TABLE zz_bp(id int, c char(10), d char(3))` with `('ab','x')`,
`('','')`, `('abcdefghij','xyz')`, read on both engines:

| boundary | PG 18.3 | goopg (before) |
|---|---|---|
| `SELECT c` DataRow (`psql -A -t \| cat -A`) | `ab        $` | `ab$` |
| `COPY … TO STDOUT` (text) | `ab        ` | `ab` |
| `COPY … TO STDOUT (FORMAT csv)` | `ab        ` | `ab` |
| `COPY … TO STDOUT (FORMAT binary)` | 10-byte field | 2-byte field |
| `octet_length(c)` | 10 | 2 |
| `length(c)` | 2 | 2 (already agreed) |
| `'[' \|\| c \|\| ']'` | `[ab]` | `[ab]` (already agreed) |

The last two rows are why this survived so long: `length()` uses `bcTruelen`
and the `||` operand goes through the `bpchar`→`text` cast, which rtrims — so
the two most natural ways to eyeball a `bpchar` in `psql` both *hide* the
padding. A pre-existing comment in `internal/server/dispatch.go` had drawn the
wrong conclusion from exactly that:

> bpcharout (PG) uses bcTruelen which trims trailing spaces before sending over
> the wire.

`bpcharout` is a bare `TextDatumGetCString` and trims nothing. `bcTruelen`
belongs to the comparison and cast-to-text paths.

A second, independent divergence fell out of the multibyte probe:
`INSERT INTO t(char(5)) VALUES ('あい')` was a flat `22001 value too long for
type character(5)` on goopg, because `coerceTextLikeDatum` measured the
declared length in **bytes** (6) rather than characters (2). PG accepts it
(`octet_length` 9 = 2 three-byte runes + 3 pad spaces), and
`'あいうえお'::varchar(5)` is likewise accepted at 15 bytes. Upstream measures
with `pg_mbstrlen_with_len` in both `varchar_input` and `bpchar_input`.

## The design constraint: goopg stores `bpchar` trimmed, on purpose

`internal/executor/codec.go`'s `coerceTextLikeDatum` strips trailing spaces
before storage. That is not an oversight — the compact image and
`compareDatum`'s padding-insensitive `bpchar` equality (and
`btree.PGCompareBpcharC`, which ignores trailing spaces) all rest on it, and
the M0103-0007 pgoutput rung documented it as the convention.

So the fix is *not* to store padded. It is to restore the declared width at
every boundary that renders the value — which is what upstream does implicitly
by storing it padded, and what goopg must do explicitly. Four boundaries, one
rule, therefore one function (Hard-won Rule #2 — these are render siblings and
must not be allowed to drift):

`catalog.PadBpchar(t Type, s string) string` (`internal/catalog/bpchar.go`).
It lives on the package that owns `Type` because `internal/executor` and
`internal/wal` both need it and neither may import the other.

Only a type carrying an explicit length modifier pads:

- a bare `char` with no `Args` is `pg_type` OID 18, a 1-byte internal type that
  is not `bpchar` at all;
- a bare `bpchar` is upstream typmod −1, and `bpchar_input`'s
  `atttypmod < VARHDRSZ` arm sets `maxlen` to the actual string length.

The width counts **characters**, matching the `coerceTextLikeDatum` fix and the
rune-counting truncation the explicit-cast path in `expr.go` already did.

## The four wirings

| boundary | site | note |
|---|---|---|
| DataRow (both protocols) | `internal/server/dispatch.go` `appendTypedCellText`, `case "char", "bpchar"` | replaces the `bcTruelen` comment quoted above; shared by the simple-query streaming path and the extended-query materialising path, so one edit covers both |
| `COPY … TO` text + CSV | `internal/executor/copy_text.go` `datumToCopyText`, default arm's `KindString` case | CSV shares `datumToCopyText`, so both formats move together |
| `COPY … TO … (FORMAT binary)` | `internal/executor/copy_binary.go` `datumToCopyBinary`, default arm's `KindString` case | `bpcharsend` IS `textsend`, so no dedicated `case` is needed — only the padding |
| pgoutput change message | `internal/wal/pgoutput.go` `pgoDecodePhysicalValue`, varlena fall-through | a goopg publisher was sending a narrower value than a PG publisher would |

## Why the decode half deliberately gets no arm

`bpchar_recv` runs `bpchar_input`, which pads to the typmod. Reproducing that
in `copyBinaryToDatum` would put a *padded* value into a column that an
`INSERT` stores *trimmed* — the same column would be two different widths
depending on how it was loaded. goopg's single point of truth for that
convention is `coerceTextLikeDatum`, which every write path already reaches and
which already carries `bpchar_input`'s own 22001 wording (`value too long for
type character(%d)`). A PG-authored padded field therefore round-trips
correctly through the existing default arm, and an over-long foreign field
still raises 22001. `TestCopyBinaryBpcharRoundTripsToTrimmedStorage` pins that
reasoning so a later loop does not "complete the symmetry" and break it.

## Gates

- `TestPadBpchar` (`internal/catalog`, 15 oracle-pinned rows).
- `TestCopyTextBpcharCarriesDeclaredWidth` +
  `TestCopyBinaryBpcharCarriesDeclaredWidth` (`internal/executor`), driven from
  ONE shared table so the two COPY formats cannot answer the same column
  differently.
- `TestCopyBinaryBpcharRoundTripsToTrimmedStorage` (`internal/executor`).
- `TestCoerceTextLikeDatumMeasuresCharactersNotBytes` (`internal/executor`).
- `TestPgoDecodeBpcharCarriesDeclaredWidth` (`internal/wal`).
- `TestAppendTypedCellTextBpcharCarriesDeclaredWidth` (`internal/server`).

Mutation-checked three ways: pad disabled → 30 failing sub-tests across the
four packages; pad counting bytes instead of runes → 10; `coerceTextLikeDatum`
reverted to a byte count → 1.

One pre-existing test changed, because it had encoded the bug:
`TestPGHeapEncodingPreservesTextLikeInsertCoercions` expected an
"empty-string-ish" row for `INSERT INTO t(v char) VALUES (3, '')`. PG returns a
single space there (`octet_length` 1, DataRow `" "`), measured on the same
table; the assertion now demands it.

E2E vs PG 18.3, byte-identical on all eight comparisons (`cmp`, not eyeball):
`SELECT *`, `COPY … TO` in text/CSV/binary, over both the ASCII `char(10)`
table and a multibyte `(char(5), varchar(5))` table.

Full gates: `go build ./...`; `internal/catalog`, `internal/executor`,
`internal/wal`, `internal/server`, `internal/pgnodes` PASS;
`TestPort_RegressSuite` PASS (271 s — Hard-won Rule #5, this is a codec change);
`RALPH_PRECOMMIT_SCOPE=units` PASS; `scripts/tpch-spotcheck.sh` PASS
(Q12=2, Q13=35 canonical — TPC-H `lineitem` carries `char(1)`/`char(10)`
columns, so this gate is load-bearing here); TPC-DS SF0.5 sweep PASS=95
MISMATCH=0 CKMISMATCH=0 ERROR=0, plan shapes identical 99/99.

## Deferred

- `octet_length()`/`bit_length()` on a `bpchar` column still answer from the
  trimmed image (2 where PG says 10). Unlike the four render boundaries these
  are expression evaluations, and the declared type is not threaded to the
  function's argument — the same plumbing gap `length()` escapes only because
  `bcTruelen` makes the trimmed answer the right one.
- A bare `bpchar` column (no length modifier) is treated as `char(1)` by
  `coerceTextLikeDatum`'s `n := 1` default, so `INSERT`ing `'abc'` raises a
  spurious 22001; upstream's typmod −1 means unlimited. Only the *bare*
  `bpchar` spelling is affected — bare `char`/`character` really are
  `character(1)`.
- goopg's heap image stays trimmed by design (above). A hosted real PG reading
  a goopg `char(N)` column off disk therefore sees a short varlena where it
  expects N characters.
