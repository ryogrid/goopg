# 0119-0006 — reg* array element fidelity (array-of-reg* stores 4-byte OIDs, not text)

- **Status:** draft (accepted on merge with the slice commit)
- **Source:** deferral-ledger row 1306 (M0119-0006); successor to the 66th–68th
  slice scalar `reg*` family (`0119-0006ar`/`as`/`at`).
- **Oracle:** PG 18.3 `postgres/src/backend/utils/adt/arrayfuncs.c`
  (`array_in`, `ReadArrayStr`, `array_recv`) + `postgres/src/backend/utils/adt/regproc.c`
  (`regclassin`/`regtypein`/`regprocin`/`regprocedurein`/`regrolein`/`regcollationin`).

## The defect

The scalar `reg*` family (66th–68th slices) made `regclass`/`regtype`/`regprocedure`/
`regrole`/`regcollation`/`regproc` store as 4-byte LE OIDs end to end (heap, binary
COPY, `pgoutput`, name↔OID via `regIdentifierInput`/`RegOut`). The **array** form of
each was left behind: a `regclass[]` column still stores its elements as varlena
**text** (`elemtype=25` in the array blob header), where PG stores a 4-byte OID per
element resolved through the element type's input function. This is the same
descriptor-vs-blob disagreement the interval/uuid/numeric/date-time element slice
fixed for those types — the pg_attribute descriptor says `_regclass` (fixed 4-byte
elements) but the stored blob header says `text` (25).

Concrete divergences (measured, non-indexed path):

- `INSERT INTO t VALUES ('{mytable}')` into a `regclass[]` column **silently stores
  the text** `mytable` rather than resolving to `mytable`'s OID — no error, no
  resolution. PG resolves to the OID.
- `SELECT` / COPY TO then render the stored text verbatim, so a numeric-OID element
  (`{1259}`) renders `{1259}` where PG's `regclassout` renders `{pg_class}`.
- The scalar path's name→OID SQLSTATEs (42P01/42704/42883/42602) are never reached;
  the array path bypasses `coerceRowForConstraintChecks` entirely (see below).

## Root cause (two seams)

1. **`coerceRowForConstraintChecks` skips array columns.**
   `internal/executor/operators_storage.go:2305`:
   `if … || col.Type.IsArray { continue }`. The 68th-slice reg* include filter
   (`isRegIdentifierTypeName`) *passes* a `regclass[]` column (it tests `Type.Name`,
   which is the element name), but this IsArray guard drops it before
   `regIdentifierInput` runs.
2. **`pgarray.ElemTypeInfo` has no reg* arms.** `encodeArrayValuePG`
   (`codec_array.go:51`) calls `arrayElemTypeInfo(t.Name)` → `pgarray.ElemTypeInfo`,
   which returns `ok=false` for `regclass` → text-element fallback (`elemtype=25`,
   varlena). `encodeArrayElem` (`codec_array.go:117`) then has no reg* case and
   stores each element as text via `array4ByteVarlena`.

The single choke point for heap storage is `encodeValuePG` (`codec.go:252`, IsArray
arm) → `encodeArrayValuePG` → `encodeArrayElem`; every writer (INSERT `Next`, COPY
FROM `insertSourceRow`, binary/CSV) converges here.

## The fix (sibling triplet — all three must land together)

Hard-won Rule #2: encode↔decode, and every array sibling, must move in one slice or
the heap/image disagreement recurs (cf. the interval/uuid/numeric element slice).

1. **`pgarray.ElemTypeInfo`** (`internal/pgarray/pgarray.go:138-208`) — add arms for
   the six `isRegIdentifierTypeName` members: `regproc`(24), `regprocedure`(2202),
   `regclass`(2205), `regtype`(2206), `regrole`(4096), `regcollation`(4191). Each is
   **fixed 4-byte OID, align 4 (`'i'`), varlena=false** — identical to the scalar
   family's typalign. (`cid`(29) and numeric-only `oid` stay out of scope: no name
   form, and the encode/align arms deliberately exclude them today.)
2. **`encodeArrayElem`** (`internal/executor/codec_array.go:117-242`) — add a reg*
   case that resolves each element string name→OID via the **same `regIdentifierInput`**
   the scalar path uses (`reg_identifier.go:216`), so the miss SQLSTATEs (42P01
   regclass, 42704 regtype/regrole/regcollation, 42883 regproc/regprocedure, 42602
   invalid-name-syntax) and the `parseDashOrOid` `-`/numeric-OID handling match the
   scalar path byte-for-byte. Store the resolved OID as 4-byte LE.
3. **Output render** (`internal/pgarray/pgarray.go` `DecodeElemStyled` :288 /
   `RenderTextStyled` :229) — render each reg* element OID → name via
   `executor.RegOut` (the 68th-slice shared SELECT+COPY renderer, `reg_identifier.go`),
   OID 0 → `-`, dangling → numeric. **`internal/pgarray` is a leaf and cannot import
   `internal/executor`** — thread the name renderer (or a minimal `catalog.Catalog`
   value) in as a **value parameter** from the executor caller, exactly as `dateStyle`/
   `dateOrder`/`timeZone` already flow into the array render path. No new imports.

### Threading (the mechanical work)

- **Input:** `regIdentifierInput(input, typeName, ctx, pos)` needs `ctx` (catalog for
  name resolution + the connection's dbOid for the 75th-slice scoping) and `pos`
  (error positions). `encodeValuePG`/`encodeArrayValuePG`/`encodeArrayElem` currently
  take no ctx. Widen them (ctx + pos) and propagate from `EncodeRowPG`/its callers.
- **Output:** the executor array→text formatter (SELECT `appendTypedCellText` and COPY
  TO `datumToCopyText`, both of which already carry `cat catalog.Catalog`) passes the
  catalog/`RegOut` value down into the pgarray render entry point so reg* element OIDs
  render as names.

## Explicitly deferred (new ledger rows, not in this slice)

1. **B-tree array-key name resolution** — an indexed `regclass[]` column's insert goes
   through `encodeArrayBTreeKey` (`btree_array_key.go:117`) → scalar element encoder
   (`operators_ddl.go:10915`); it may still numeric-parse a name element. Verify and,
   if still divergent, defer with its own row.
2. **WAL `pgoutput` array decode for reg\*** — the `pgoDecodePhysicalValue` array twin
   must agree; verify, fix if trivial, else defer.
3. **Binary-COPY `_regclass` recv/send** — `array_recv`/`array_send` per-element
   `typreceive`/`typsend`; separate from the TEXT/CSV + heap path.
4. **NULL array elements** — `encodeArrayValuePG` hard-errors 0A000
   (`codec_array.go:72-76`) where PG's `array_in` accepts `ATOK_ELEM_NULL`
   (`arrayfuncs.c:710,735`). Pre-existing, unrelated to reg*.

## Acceptance criteria

1. `ElemTypeInfo` reports the six reg* members as fixed 4-byte OID elements.
2. `INSERT`/COPY FROM of `'{mytable}'` into a `regclass[]` column stores the resolved
   4-byte OID (blob header `elemtype` = the `_regclass` type OID, not 25); a miss
   raises 42P01; `'{1259}'` stores OID 1259.
3. `SELECT`/COPY TO of a `regclass[]` column renders names (`{pg_class}`), OID 0 → `-`,
   dangling → numeric, byte-identical to scalar `RegOut` — so `TestRegCopyAndSelectSibling`
   -style sibling agreement holds for the array case too.
4. Named tests FAIL-pre / PASS-post; mutation-checked.

## Gates (foreground, in order)

- `go test ./internal/pgarray/ ./internal/executor/` (package suites)
- `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh`
- `scripts/tpch-spotcheck.sh` (Q12=2, Q13=35)
- `go test -v -run 'TestPort_RegressSuite' ./internal/testport/` (regress parity)
