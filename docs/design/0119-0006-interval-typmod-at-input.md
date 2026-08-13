# M0119-0006 (63rd slice) — an `interval(N)` / `interval <field> [TO <lo>] [(p)]` column rounds at INPUT via `AdjustIntervalForTypmod`

Closes the `AdjustIntervalForTypmod` column-typmod deferral row filed by the
55th slice. This is the interval half of the `Adjust*ForTypmod` cluster the
62nd slice closed for `time`/`timetz`; the two differ in one way that shaped
everything here — the interval typmod is a **packed range+precision** value
(`INTERVAL_TYPMOD`), not a bare precision, so it also had to become reachable
(parsed, and stored on disk) before it could be applied, not merely applied
once reachable.

## The defect

Upstream `interval_in` (`postgres/src/backend/utils/adt/timestamp.c`) and its
binary twin `interval_recv` (`timestamp.c:1013`) both finish by calling
`AdjustIntervalForTypmod` (`timestamp.c:1355`) **before** the value is stored.
For a column declared `interval(N)` / `interval year to month` / `interval
hour to second(2)`, that function:

1. **zeroes** every field to the right of the range's low field (toward zero —
   `'1 year 2 mons'::interval year` is `1 year`, `'3 days 04:00:00'::interval
   day` is `3 days`), and
2. **rounds** the sub-second field half-away-from-zero to the `SECOND(p)`
   precision (`'1.23456789'::interval second(2)` is `00:00:01.23`,
   `'1.999999'::interval second(2)` is `00:00:02`).

goopg applied neither at input. The literal/cast half was already correct
(`applyIntervalCastTypmod` in `expr.go`), but a **column**'s typmod never
reached the value: `parseColumnType` did not even parse `interval year to
month` (it read `interval` and left `year to month` for the column-def parser
to reject), the `interval(N)` spelling it *did* parse was dropped to
`atttypmod = -1` on disk (`pgAttTypmod` has no interval case), and the
insert/COPY paths stored the received value whole. Measured on PG 18.3:
`INSERT INTO t (c interval year to month) VALUES ('1 year 2 mons')` stores
`1 year`; goopg stores `1 year 2 mons` — a stored-value difference, not a
display one.

## The fix

Mirror the 62nd slice, but with two extra steps the time typmod did not need:

1. **Port `AdjustIntervalForTypmod`** to `internal/pgdatetime` (the leaf the
   two cross-package readers share, alongside `FormatInterval`):
   `AdjustIntervalForTypmod(months, days int32, micros int64, typmod int32)
   (int32, int32, int64)`. A literal port of `timestamp.c:1355`: the
   `INTERVAL_MASK(field)` range switch, the `IntervalScales`/`IntervalOffsets`
   precision rounding, and the `±infinity` no-op. `typmod` is the packed
   `INTERVAL_TYPMOD` — `((range & 0x7FFF) << 16) | (precision & 0xFFFF)` —
   which goopg's parser already produces (see below); `-1` is a no-op.
2. **Make the column typmod reachable** — parse it in the column position
   (`parseColumnType`), carrying the **full** range mask (not the low-field
   collapse the cast path uses), and store it PG-faithfully on disk so a
   restart and a hosted PG both see it.
3. **Apply at the four input sites** (as the time slice did) and delete the
   single stale "truncates at display" comment.

### New / changed symbols

- `internal/pgdatetime/interval_typmod.go` — `AdjustIntervalForTypmod` plus the
  field-mask constants (`INTERVAL_MASK(YEAR)=1<<2`, `MONTH=1<<1`, `DAY=1<<3`,
  `HOUR=1<<10`, `MINUTE=1<<11`, `SECOND=1<<12`), the scale/offset tables, and a
  unit test.
- `internal/parser/select.go` — `parseIntervalColumnQualifier`,
  `intervalRangeMask`, `packIntervalColumnTypmod`. The cast path's
  `packIntervalCastTypmod` already emits exactly PG's `INTERVAL_TYPMOD` (its
  `intervalFieldTypmodBit` map *is* `datetime.h`'s `MONTH=1, YEAR=2, DAY=3,
  HOUR=10, MINUTE=11, SECOND=12`); the column path reuses that packing but
  preserves the full `YEAR|MONTH`-style range mask instead of collapsing to the
  low field.
- `internal/parser/ddl.go` — `parseColumnType` parses `interval <field> [TO
  <lo>] [(p)]` and `interval(p)` into `Args = [packed typmod]`.
- `internal/executor/codec.go` — `intervalColumnTypmod(t)` (reads
  `t.Args[0]`, `-1` when absent) and `roundIntervalDatumToTypmod(d, typmod)`
  (parse via the shared `pgIntervalFieldsFromDatum`, adjust, re-wrap in
  `NewIntervalDatumFull`).
- `internal/executor/operators_storage.go` — `coerceRowForConstraintChecks`
  gains an `interval` case (INSERT/UPDATE).
- `internal/executor/copy_text.go` — `copyTextToDatum` gains an `interval` arm.
- `internal/executor/copy_binary.go` — the `interval` decode arm applies
  `roundIntervalDatumToTypmod` after the 16-byte length check.
- `internal/executor/pg18_user_catalog_rows.go` — `pgAttTypmod` `case 1186`
  returns the packed typmod verbatim (interval stores it raw, no `VARHDRSZ`
  offset, unlike numeric/char).
- `internal/initdb/catalog_heap_reload.go` — `pgTypeArgsFromTypmod` `case 1186`
  returns `[typmod]` so a reloaded catalog rebuilds `Args`.
- `internal/executor/expr.go` — `formatIntervalTypmod` (the
  `intervaltypmodout` port, `timestamp.c:1065`) and `formatTypeOID`'s `case
  1186` now append it, so `format_type(interval, typmod)` renders the declared
  spelling — `format_type` is what pg_dump reads, and without this the typmod
  I put on disk would be dumped back as a bare `interval`.

### Input sites (round, then the value is stored pre-rounded)

| site | change |
|---|---|
| cast (`::interval <qualifier>`) | already correct via `applyIntervalCastTypmod` |
| `coerceRowForConstraintChecks` | new `interval` case → `roundIntervalDatumToTypmod` |
| `copyTextToDatum` | new `interval` arm → `roundIntervalDatumToTypmod(NewStringDatum(raw), …)` |
| `copyBinaryToDatum` | existing arm → round after the length check |
| `encodeValuePG` | storage-choke safety net for the DEFAULT/generated path `!insertMissing` skips |

## Sibling audit (Hard-won Rule #2)

- The text/binary/heap readers of one interval column all parse through the
  **same** tokenizer — `parser.ParseIntervalBody` via `pgIntervalFieldsFromDatum`
  — so a table loaded by `COPY` cannot disagree with one loaded by `INSERT`
  under the same typmod. (`evalCast`'s `interval` arm is deliberately **not**
  used: it is the limited `<n> <unit>` cast grammar, which would reject a
  multi-component body `'1 year 2 mons'` the other paths accept.)
- `±infinity` is a no-op exactly as upstream (`INTERVAL_NOT_FINITE`): the
  sentinel is `all-fields-at-extreme`, which `AdjustIntervalForTypmod` checks
  first, and `pgIntervalFieldsFromDatum` already returns it for the
  `'infinity'` string.
- `interval` arrays (`_interval`, 1187) are unaffected — the element typmod is
  not carried (matching the existing `_interval` handling), and `IsArray`
  already skips them at every site.

## On-disk fidelity

The column typmod now round-trips `pg_attribute.atttypmod`: `pgAttTypmod(1186,
[typmod]) = typmod` and `pgTypeArgsFromTypmod(1186, typmod) = [typmod]`. The
full range mask is preserved, so `interval year to month` stores `6
(YEAR|MONTH)` and `format_type` renders `interval year to month`, not the
low-field-collapsed `interval month` the cast path's packing would produce —
the two paths must differ here, which is the one deliberate divergence from the
cast packing.

## Gates

`go build ./...` + `go vet` clean; `internal/pgdatetime` + `internal/executor`
+ `internal/initdb` + `internal/parser` targeted tests PASS; UNITS pre-commit
PASS; `scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=35); pgbench smoke via the
commit hook.

New guards (verified red on the pre-fix source):
- `internal/pgdatetime/interval_typmod_test.go` — oracle cells for
  `AdjustIntervalForTypmod`: each range mask's zeroing, half-away-from-zero
  rounding, `±infinity` no-op, `typmod = -1` no-op.
- `internal/executor/interval_column_typmod_test.go` — E2E through the four
  input sites against PG 18.3 cells: `INSERT`, `COPY TEXT`, `COPY BINARY` and
  the `interval year to month` column all store the range/round-adjusted value,
  and a restart (catalog reload) preserves the typmod.

## Deferred

- The cast path's low-field collapse (`packIntervalCastTypmod`) remains — it is
  internal-only (never on disk) and the truncation result is identical
  (`INTERVAL YEAR TO MONTH` ≡ `INTERVAL MONTH`, per PG's own comment), so no
  behavior depends on it. Not changed here to avoid touching the working cast
  tests.
- `ColumnTypmod` (pgnodes) still returns `-1` for interval, so a **DEFAULT
  expression** carrying an interval typmod is not re-coerced at the pg_attrdef
  layer — correct, because PG has no `interval(interval,int4)` length-coercion
  function; the typmod is applied by `interval_in` at input, which is this
  slice's encodeValuePG/coerce path.
