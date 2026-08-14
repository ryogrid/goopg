# M0119-0006 (66th slice) — the reg* family and `cid` store as 4-byte OIDs, and the regtypein 42704 miss-path fires

Closes the reg*/cid half of the deferral the 54th slice filed (ledger row 1300).
Upstream stores every member of the object-identifier family as a 4-byte
unsigned integer (typbyval, typalign `'i'`); goopg's heap codec recognised only
`oid` and `regproc`, so `regclass`, `regtype`, `regprocedure` and `cid` columns
fell through `encodeValuePG`'s default and were stored as **varlena TEXT** — a
hosted PG 18.3 reads a 4-byte identifier where the heap holds text (the same
`feedback_pg_faithful_binary_over_text` inversion the serial-spelling and
unsigned-identifier slices fixed for their own families).

This is the fix the 65th slice's gate note names as its only failure: the
untracked `reg_identifier.go` WIP had a genuinely failing
`TestRegIdentifierInputResolvesRegtypeName`, because `catalog.TypeNameToOID`
**falls back to `OIDText` (25) for any name it does not know**, and the code
tested `oid != 0` — so `regIdentifierInput("no_such_type", "regtype")`
resolved to text's OID instead of raising the regtypein undefined-object error
(42704).

## The defect

Two separate gaps, both on the write path that ends in `encodeValuePG`:

1. **Storage width.** `regclass` (2205), `regtype` (2206), `regprocedure`
   (2202) and `cid` (29) send as 4-byte identifiers upstream — their typsend is
   `regclasssend`/`regtypesend`/`regproceduresend`/`cidsend`, which are all
   `oidsend` (`pq_sendint32`, `postgres/src/backend/utils/adt/regproc.c` /
   `oid.c`) — and `pgPhysicalTypeIsVarlena`/`physicalPGTypeAlign` did not know
   them, so a `regclass` column stored its value as text.

2. **Name→OID input.** A bare quoted name literal — `INSERT INTO t(r regclass)
   VALUES ('mytable')` — reached `encodeValuePG` as a `KindString`, and the
   numeric oid arm's `coerceStringToInt64` then tried to parse the *name* as a
   number. `regclassin`/`regtypein`/`regprocin` upstream do a catalog lookup,
   never a numeric parse, and raise the type's own undefined-object error
   (42P01 / 42704 / 42883) on a miss. goopg had a partial implementation in
   `expr.go` but nothing on the coercion path.

## What PG actually does

- **Storage**: all seven OID-family members (oid, regproc, regprocedure,
  regclass, regtype, regrole, regcollation) plus `cid` are `int4`-sized
  `typbyval` types; `regclasssend` and friends call `oidsend`, so the wire image
  is the 4-byte BE OID (`pq_sendint32`), and the heap image is the same 4 bytes
  LE. The output functions (`oidout`/`regclassout`/…, and `cidout`) all use the
  **unsigned** `%u` conversion, so OID 0xFFFFFFFF prints `4294967295`, not −1.
- **Input**: each reg* type's `*_in` resolves a NAME through a catalog lookup
  and raises its own undefined-object error on a miss. A numeric `KindInt` datum
  is already an OID and passes through unchanged.
- `regrole` (4096) and `regcollation` (4191) have the same storage requirement
  but their input functions resolve against `pg_roles`/`pg_collation`, for which
  goopg has no name→OID seam at coerce time — they carry forward (new ledger
  row), deliberately staying on the varlena default because storing a numeric
  parse of a name is more wrong than text.

## The fix

### 1. `internal/executor/reg_identifier.go` (new file)

- **`regIdentifierInput(v Datum, typeName string, ctx *Context, pos int)
  (Datum, error)`** — the input half of the reg* family's name↔OID contract.
  A `KindInt` passes through; a `KindString` is trimmed and resolved as a NAME:
  - `regclass` → `LookupTable` then `LookupIndex` (indexes are valid regclass
    targets), else 42P01 `relation %q does not exist`.
  - `regtype` → the static `TypeNameToOID` table, then `userTypeOIDForName`
    for user types, else 42704 `type %q does not exist`. **The miss test is the
    established `oid != catalog.OIDText || strings.EqualFold(name, "text")`
    idiom** (shared with `castKeyTypeName` and `pgTypeofOIDForName`): the
    `OIDText` fallback would otherwise make the 42704 arm dead code.
  - `regproc`/`regprocedure` → `Routines().LookupByName`, then
    `LookupBuiltinProc`, else 42883 `function %q does not exist`.
  - `oid`/`cid` are numeric-only and are NOT routed here.
- **`regIdentifierOIDFromDatum(d Datum, typeName string) (uint32, error)`** — the
  shared 4-byte range check for the family (mirrors `pgUnsignedIDFromDatum` but
  names the actual type in the 22003 message, so a `cid` overflow says "cid").

### 2. `internal/executor/codec.go` — the heap arms

`encodeValuePG`'s `"oid", "regproc"` arm now covers `regprocedure`, `regclass`,
`regtype` and `cid` through `regIdentifierOIDFromDatum`;
`decodePhysicalPGValueMctx` mirrors them (4-byte LE → `KindInt`);
`physicalPGTypeAlign` and `pgPhysicalTypeIsVarlena` return 4 / false.

### 3. `internal/executor/operators_storage.go` — the coercion choke point

`coerceRowForConstraintChecks` is the single point every new row passes through.
It gains a case routing `regproc`/`regprocedure`/`regclass`/`regtype` through
`regIdentifierInput`, so a bare quoted name in an INSERT/DEFAULT is resolved to
its OID before the heap arm stores it, and an unresolvable name raises the
reg*in error instead of a silent text store.

### 4. `internal/executor/copy_binary.go` — the wire twins

Binary COPY's `datumToCopyBinary`/`copyBinaryToDatum` arms extend to the same
four types + `cid` (4-byte BE on the wire, `oidsend`/`oidrecv`), sharing
`regIdentifierOIDFromDatum` with the heap encoder so the two images cannot
drift (Hard-won Rule #2).

### 5. `internal/wal/pgoutput.go` — the third physical decoder

`pgoDecodePhysicalValue` is the SECOND decoder of goopg's heap layout (the
executor's `decodePhysicalPGValueMctx` is the first) and the two must agree.
Before this slice an unrouted `regclass`/`regtype`/`regprocedure`/`cid` here
would have read the 4-byte image through the varlena fall-through — the first
raw byte taken as a length header, so a replicated identifier reached the
subscriber as garbage of an arbitrary length rather than failing. Its `oid`
arm now covers the family and returns the **unsigned** decimal OID (what
`regclasssend`/… and `cidout` print).

### Deliberately NOT done

Index-key encoding is untouched: the family is excluded from
`isSupportedBTreeKeyType`, so no key path is reachable. The wire formatter in
`dispatch.go` already renders `regclass`/`regproc`/`regprocedure`/`regtype`
OID→name (the bidirectional `::reg*` cast half in `expr.go`).

## New / changed symbols

- `internal/executor/reg_identifier.go` (new): `regIdentifierInput`,
  `regIdentifierOIDFromDatum`.
- `internal/executor/codec.go`: `encodeValuePG` reg*/cid arm,
  `decodePhysicalPGValueMctx` arm, `physicalPGTypeAlign`, `pgPhysicalTypeIsVarlena`.
- `internal/executor/operators_storage.go`: `coerceRowForConstraintChecks` reg* case.
- `internal/executor/copy_binary.go`: `datumToCopyBinary`/`copyBinaryToDatum`
  reg*/cid arms.
- `internal/wal/pgoutput.go`: `pgoDecodePhysicalValue` oid-arm extension.

## Tests

- `internal/executor/reg_identifier_test.go`:
  - `TestRegIdentifierColumnStoresFourBytesNotText` — heap width 4, LE bytes,
    `pgPhysicalTypeIsVarlena` false, align 4, decode round-trip → `KindInt`.
  - `TestRegIdentifierInputResolvesRegclassName` — name→OID, 42P01 on a miss,
    numeric pass-through.
  - `TestRegIdentifierInputResolvesRegtypeName` — builtin table resolve, **42704
    on a miss** (the pre-existing blocker this slice fixes).
  - `TestCoerceRowForConstraintChecksResolvesRegIdentifier` — the coercion
    choke point resolves a bare name, the coerced OID encodes to 4 bytes, and a
    miss raises 42P01 through the choke point.
- `internal/wal/pgoutput_reg_identifier_test.go`:
  - `TestPgoDecodeRegFamilyMatchesPGNativeLayout` — 4-byte decode → unsigned
    decimal text for regclass/regtype/regprocedure/regproc/cid/oid, incl. OID 0
    and 0xFFFFFFFF.
  - `TestPgoPhysicalAlignRegFamily` — typalign `'i'` across odd offsets.

## Gates

- `go build ./internal/...` clean.
- `go test ./internal/executor/ ./internal/wal/` — all PASS, including the
  previously-failing `TestRegIdentifierInputResolvesRegtypeName`.
- `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` — full unit/
  component suite PASS (the regtype blocker is gone).
- `TestPort_RegressSuite` — PASS (346 s).
- `scripts/tpch-spotcheck.sh` — PASS (Q12=2, Q13=35).
- Commit-hook pgbench smoke runs on the commit.

1 ledger row resolved (1300's reg*/cid scope); 1 new row filed (regrole/
regcollation carry-forward). Design `0119-0006-reg-identifier-family-storage.md`
+ README row.
