# M0143-0004 — `PhysicalTypeIsVarlena` had no `IsArray` arm

Status: fixed 2026-09-18.

## 1. The bug

A user array column carries `catalog.Type{Name:<element>, IsArray:true}` —
`Name` holds the ELEMENT type, `IsArray` marks the array-ness separately
(DU-002 slice 62; `catalog.go:219-229`). `PhysicalTypeAlign` already had an
`IsArray` short-circuit at its top (`if t.IsArray { return 4 }`,
`physical_align.go:21-23`, landed M0118-0002) because every array is a
varlena `ArrayType` blob on disk regardless of its element type's own
alignment.

`PhysicalTypeIsVarlena` — the sibling function answering "is this column's
on-disk representation varlena" — had no such arm. It switches purely on
`strings.ToLower(t.Name)`, so `Type{Name:"int4", IsArray:true}` fell into the
same `case "int4", ...: return false` arm as a plain scalar `int4` column,
reporting an `int4[]`/`bool[]`/`date[]`/`uuid[]`/… column as fixed-width.
`sys_pg_constraint.go:141-163` had already hand-documented this exact gap
while explaining why `pg_constraint.conkey`/`confkey` are deliberately typed
`{Name:"int2[]"}` (a catalog-form array Name, never `IsArray:true`) instead
of the "more natural" `{Name:"int2", IsArray:true}` spelling — to dodge
exactly this bug.

## 2. Consequence, confirmed live

`PhysicalTypeIsVarlena` is the single source of truth shared by the heap
codec, pgoutput's walker, and the catalog statistic paths
(`physical_align.go:72-77`). Its most consequential caller is
`pgRowHasVarWidth` (`codec.go:1573`), which drives the `HEAP_HASVARWIDTH`
infomask bit stamped on every heap tuple written
(`operators_storage.go:4272-4273`, `:9362-9363`, `:9799-9800`). For a row
whose only varlena column was a fixed-width-element array, `pgRowHasVarWidth`
returned `false` — `HEAP_HASVARWIDTH` went unset on a tuple that genuinely
contains a varlena `ArrayType` blob.

`codec.go:1550-1557` names the exact hazard: PG18's `nocachegetattr`
fast-path `attcacheoff` walker (`heaptuple.c:642`, `Assert(j > attnum)`)
trusts `HEAP_HASVARWIDTH` being unset to mean every column up to the cached
offset is fixed-width and can be skipped by arithmetic rather than walked
byte-by-byte. A real PG18 backend (or any PG-faithful decoder honoring the
infomask) reading such a tuple would use the fixed-prefix shortcut, land
inside or past the array's bytes, and misread it plus every following
column.

**Reproduced against real heap bytes**, not just by code reading: built a
pre-fix and a post-fix `goopg` binary, started throwaway clusters, ran

```sql
CREATE TABLE onlyarr (tags int4[]);
INSERT INTO onlyarr VALUES (ARRAY[1,2,3]);
```

then parsed the raw page bytes (`PageHeaderData` → `ItemIdData` →
`HeapTupleHeaderData.t_infomask` at byte offset 20). Pre-fix:
`t_infomask=0x0800` (`HEAP_HASVARWIDTH` unset). Post-fix: `t_infomask=0x0802`
(set, correctly).

## 3. What did NOT need a change (checked, not assumed)

`encodeArrayValuePGCtx` (`codec_array.go:104-113`) always emits the
**4-byte / long-form** varlena header (`total << 2` — low 2 bits always `0`,
so `buf[0]` is always even). `isShortVarlenaHeader` (`codec.go:163-165`,
D-09's own short-header test, `buf[0]&1==1 && buf[0]!=0x01`) is therefore
**always false** for arrays, independent of this bug — `packableShortColumn`
(`codec.go:170-178`, the D-09 `att_align_datum` gate described in
`../executor-d09-alignment/DESIGN.md` §3) never took the packable branch for
an array column either before or after this fix.

Consequently `AttAlignPointer`'s decode-side peek (post-fix) and its old
unconditional-align branch (pre-fix) land on the **identical** offset for
every array column, by construction of D-09's own encode-side invariant:
whatever the writer decided ("pack, no padding" vs "align, zero-pad"), the
byte the peek inspects is either real (non-zero-ish) data or a genuine zero
pad byte the encoder wrote — never an accidental match with the other case.
Confirmed live: byte-identical relation file sizes between the two binaries
for both an `int4[]`-as-last-column table (2000 rows) and a `bool` + `int4[]`
table (5000 rows). So this fix changes the `HEAP_HASVARWIDTH` **flag**, not
any encoded byte or offset in goopg's own read/write path — the flag was
simply wrong when nothing downstream depended on the alignment/pack decision
matching it, only on the flag being correct on its own.

## 4. Fix

`internal/catalog/physical_align.go`: add the same `IsArray` short-circuit
`PhysicalTypeAlign` already has, at the top of `PhysicalTypeIsVarlena`:

```go
if t.IsArray {
    return true
}
```

`sys_pg_constraint.go`'s `conkey`/`confkey` workaround comment stays — those
two columns are still, deliberately, not `IsArray:true` — it just stops being
the only mitigation for this class of bug in the tree.

## 5. Tests

- `TestPhysicalTypeIsVarlenaArray` (`internal/catalog/physical_align_test.go`)
  — direct unit pin: every fixed-width element name reports varlena in array
  form, non-varlena in scalar form.
- `TestPgRowHasVarWidthDetectsVarlenaCols` (extended,
  `internal/executor/pg18_user_catalog_rows_test.go`) — a new
  `{Name:"int4", IsArray:true}` case exercising the exact `HEAP_HASVARWIDTH`
  path the live infomask reproduction above hit; the pre-existing case there
  used `{Name:"text[]"}`, a catalog-form Name that was never in the
  fixed-width switch and so never exercised the missing arm.

## 6. Gates

`go build ./...` clean; `go test ./internal/catalog/... ./internal/executor/...
./internal/access/...` PASS; `RALPH_PRECOMMIT_SCOPE=units
scripts/ralph-precommit-test.sh` full green; `scripts/tpcds-sf025-regression.sh
sweep` `PASS=96 MISMATCH=0 ERROR=0 TIMEOUT=0`, plan-shapes 99/99 identical;
`tpch-spotcheck.sh` SKIP-BLOCKED (exit 3) by the `:65433` P0-E6 evidence hold
(`bench/tpch/runtime_goopg/data.HOLD`) — ledger: P0-E7 is the standing re-run
owner, same exception already used by M0143-0003b/c/d/e/f.
