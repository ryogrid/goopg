# M0119-0006 (67th slice) — `regrole`/`regcollation` store as 4-byte OIDs

Closes the 66th slice's carry-forward (ledger row 1302): `regrole` (4096) and
`regcollation` (4191) remain the last two members of the object-identifier
family stored as **varlena TEXT** in the heap. Every other member — `oid`,
`regproc`, `regprocedure`, `regclass`, `regtype`, `cid` — is a 4-byte `typbyval`
identifier since the 66th slice (ledger row 1300, design
`0119-0006-reg-identifier-family-storage.md`). The 66th slice deliberately
excluded `regrole`/`regcollation` because their `*_in` functions resolve against
`pg_roles`/`pg_collation` (not a static type table), and there was no name→OID
seam at coerce time. That seam now exists:

- **`regrole`**: `InMemory.RoleOID(name)` (catalog.go:15864) — a live lookup
  over the user-role map, `predefinedRoles`, and the `postgres` bootstrap
  special case, exactly the domain `get_role_oid` searches.
- **`regcollation`**: the executor already reaches user collations via
  `UserCollationOIDByName`/`FindCollation`, and the 7 BKI-pinned builtin
  collations are resolvable through the unexported
  `builtinCollationOIDByName` (`default` 100, `C` 950, `POSIX` 951,
  `pg_c_utf8` 811, `ucs_basic` 962, `unicode` 963, `pg_unicode_fast` 6411).
  This slice exports a combined `InMemory.CollationOIDByName` that calls the
  catalog's existing `builtinCollationOIDByName` (case-sensitive, PG-identifier
  semantics) then `UserCollationOIDByName` (case-insensitive) — the same
  builtin-then-user order `resolveRangeCollation` already uses, WITHOUT a third
  copy of the builtin table (the executor's own `collationNameToOID`,
  internal/executor/pg18_user_catalog_rows.go, stays for its catalog-less call
  sites).

## The defect

Two gaps, mirroring the 66th slice's structure but on the last two family
members:

1. **Storage width.** `regrolesend`/`regcollationsend` are both `pq_sendint32`
   over the 4-byte OID upstream (`postgres/src/backend/utils/adt/regproc.c`),
   and `pg_type` 4096/4191 are `typlen 4, typbyval t, typalign 'i'`. goopg's
   heap stores them as varlena text, so a hosted PG 18.3 (or `pg_amcheck`'s
   heap tier, or the pgoutput decoder) reads a 4-byte identifier where the heap
   holds text — the same
   `feedback_pg_faithful_binary_over_text` inversion the 66th slice closed for
   the other four.

2. **Name→OID input.** A bare quoted name literal — `INSERT INTO t(r regrole)
   VALUES ('postgres')` — reaches `encodeValuePG` as a `KindString`, and the
   numeric oid arm's `coerceStringToInt64` would try to parse the *name* as a
   number. `regrolein`/`regcollationin` upstream are catalog lookups and raise
   the type's own undefined-object error (both 42704) on a miss.

## What PG actually does

- **`regrolein`** (regproc.c:1541): `parseDashOrOid` first (`'-'` → OID 0; a
  pure-digit string → numeric OID via `oidin`), then
  `stringToQualifiedNameList`. If the name has more than one component →
  42602 `invalid name syntax` (role names are single identifiers). Otherwise
  `get_role_oid(name, true)`; miss → 42704 `role "%s" does not exist`.
- **`regcollationin`** (regproc.c:1026): `parseDashOrOid` first, then
  `get_collation_oid(names, true)` (search-path aware, may be qualified); miss
  → 42704 `collation "%s" for encoding "%s" does not exist`.
- **`regroleout`** (regproc.c): OID 0 → `"-"`; else `GetUserNameFromId` →
  `quote_identifier(role_name)`; a dangling OID renders numerically.
- **`regcollationout`** (regproc.c): OID 0 → `"-"`; else the `pg_collation`
  name, schema-qualified only when not visible.
- Both types send as `oidsend` / receive as `oidrecv` (4-byte BE on the wire),
  and the heap image is the same 4 bytes LE — identical to the family arms the
  66th slice wired.

## The fix

### 1. `internal/catalog/catalog.go` — the exported collation seam

New `func (c *InMemory) CollationOIDByName(name string) uint32`, sited next to
`UserCollationOIDByName` (catalog.go:14572): checks `builtinCollationOIDByName`
first, then the user registry, returning 0 when neither matches — the same
order and shape as the collation half of `resolveRangeCollation`
(catalog.go:22158-22163), lifted to an exported method the executor can call.

### 2. `internal/executor/reg_identifier.go` — the input seam

`regIdentifierInput` gains the family-wide `parseDashOrOid` equivalent at the
top of the `KindString` arm (upstream runs it before ANY name resolution, for
every reg* type, including the four the 66th slice wired — this closes that
latent gap too):

- a string `"-"` → `NewIntDatum(0)` (InvalidOid), matching `parseDashOrOid`;
- a pure-digit string → `strconv.ParseUint` → `NewIntDatum` (the heap arm's
  `regIdentifierOIDFromDatum` range-checks it exactly as `oidin`'s 22003 does);
  a parse failure falls through to name resolution.

Then two new switch cases:

- **`regrole`**: split the name; if it contains a `.` (more than one component)
  → 42602 `invalid name syntax` (PG rejects qualified role names). Else
  `im.RoleOID(name)`; miss → 42704 `role "%s" does not exist`.
- **`regcollation`**: bare name → `im.CollationOIDByName(name)`; a qualified
  name (`pg_catalog."C"`, `public.mycoll`) resolves against `FindCollation` in
  that schema. Miss → 42704
  `collation "%s" for encoding "UTF8" does not exist` (goopg is UTF-8 only, so
  the encoding name is the constant `GetDatabaseEncodingName()` would print).

The file's header comment is updated: `regrole`/`regcollation` leave the
"deliberately NOT in this file" list (regoper/regoperator/regnamespace/
regconfig/regdictionary stay there — still no seam).

### 3. `internal/executor/operators_storage.go` — the coercion choke point

`coerceRowForConstraintChecks`'s reg* case (line 2355) gains `"regrole"` and
`"regcollation"`, so a bare quoted name in an INSERT/DEFAULT resolves to its
OID before the heap arm stores it, and a miss raises 42704 instead of a silent
text store.

### 4. `internal/executor/codec.go` — the heap arms

`encodeValuePG`'s `"oid", "regproc", …` arm (line 384) adds `regrole`/
`regcollation` through `regIdentifierOIDFromDatum`; `physicalPGTypeAlign`
(line 1193) returns 4; `pgPhysicalTypeIsVarlena` (line 1244) returns false;
`decodePhysicalPGValueMctx` (line 1351) decodes the 4 LE bytes to `KindInt`.

### 5. `internal/executor/copy_binary.go` — the wire twins

`datumToCopyBinary` (line 229) and `copyBinaryToDatum` (line 496) extend their
reg* arms to the two types (4-byte BE, `regrolesend`/`regcollationsend` ARE
`oidsend`), sharing `regIdentifierOIDFromDatum` with the heap encoder so the
two images cannot drift (Hard-won Rule #2).

### 6. `internal/wal/pgoutput.go` — the third physical decoder

`pgoDecodePhysicalValue`'s oid arm (line 430) adds the two types: today the
heap image is varlena text and the fall-through returns it correctly, but this
slice makes the image 4 fixed bytes, so WITHOUT the arm the fall-through would
read a varlena header off the OID and emit garbage (the sibling-pair gap the
66th slice pinned for the other four). The decoded text is the unsigned decimal
OID — `regrolein`/`regcollationin` accept a numeric OID, so a replicated value
round-trips.

### 7. `internal/server/dispatch.go` — the DataRow output

`appendTypedCellText` gains `regrole` and `regcollation` render cases next to
the existing `regclass`/`regproc`/`regprocedure`/`regtype` ones: OID 0 → `"-"`
(matching `regroleout`/`regcollationout`), else `RoleNameForOID` /
`ResolveIndexColumnCollationName` (the exported OID→name resolvers on
`InMemory`). Without these the `SELECT` of a `regrole` column would regress from
`postgres` (the text it stores today) to the raw OID `10`. A dangling regrole
OID falls through to `AppendValueText` (the numeric form) — `RoleNameForOID`
already renders unknown OIDs numerically. A dangling regcollation OID is
handled the same way: `ResolveIndexColumnCollationName` returns `""` for an
unknown nonzero OID, so the case falls through to `AppendValueText` and prints
the numeral, matching `regcollationout`. The schema-qualification
`regcollationout` applies to a collation name not visible in the session's
search_path is NOT ported (`ResolveIndexColumnCollationName` returns the bare
name; diverges only under a custom search_path — accepted).

### Deliberately NOT done

- **The `::regrole` / `::regcollation` cast in `expr.go`** is untouched: it is
  a separate evaluation path from the column-coercion seam, and it already
  renders the numeric value today (a pre-existing gap, not a regression this
  slice introduces). Ledgered, not absorbed.
- **`regoper`/`regoperator`/`regnamespace`/`regconfig`/`regdictionary`** stay
  varlena — still no name-resolution seam.
- **Index-key encoding** is untouched: the family is excluded from
  `isSupportedBTreeKeyType`.
- **`quote_identifier`** on `regroleout` output (PG quotes a role whose name
  needs it) is not ported; `RoleNameForOID` returns the bare name. Common
  names are unaffected.
- **TEXT/CSV COPY (both directions) is untouched.** A `reg*` column copies OUT
  as its numeric OID (the default `datumToCopyText` KindInt arm) and a name
  copied IN errors (the COPY FROM path writes through `writeHeapRowReturning` →
  `EncodeRowPG` directly, bypassing `coerceRowForConstraintChecks`, so a
  `KindString` name reaches `regIdentifierOIDFromDatum`'s numeric parse). This
  is the pre-existing family-wide gap the 66th slice already shipped for
  `regclass`/`regtype`/`regprocedure`/`cid` — regrole/regcollation simply join
  the family's established COPY behavior, and the numeric OID is lossless
  cross-engine (`regrolein`/`regcollationin` accept a numeric OID). Fixing it
  means threading a catalog handle through `RunCopyTo`/`EncodeCopyTextRow`/
  `EncodeCopyCsvRow`/`datumToCopyText`/`copyTextToDatum` — out of scope here;
  tracked in a new deferral row.

## New / changed symbols

- `internal/catalog/catalog.go`: `InMemory.CollationOIDByName` (new, exported).
- `internal/executor/reg_identifier.go`: `regIdentifierInput` gains
  `parseDashOrOid` handling + `regrole`/`regcollation` cases; header comment.
- `internal/executor/operators_storage.go`: `coerceRowForConstraintChecks` reg*
  case widened.
- `internal/executor/codec.go`: `encodeValuePG`, `physicalPGTypeAlign`,
  `pgPhysicalTypeIsVarlena`, `decodePhysicalPGValueMctx` arms widened.
- `internal/executor/copy_binary.go`: `datumToCopyBinary`/`copyBinaryToDatum`
  arms widened.
- `internal/wal/pgoutput.go`: `pgoDecodePhysicalValue` oid arm widened.
- `internal/server/dispatch.go`: `appendTypedCellText` gains `regrole`/
  `regcollation` render cases.

## Tests

- `internal/executor/reg_identifier_test.go`:
  - `TestRegIdentifierColumnStoresFourBytesNotText` — loop widened to
    `regrole`/`regcollation`.
  - `TestRegIdentifierInputResolvesRegroleName` — `'postgres'` → OID 10,
    42704 on a miss, 42602 `invalid name syntax` on a qualified name.
  - `TestRegIdentifierInputResolvesRegcollationName` — `'C'` → 950,
    `'default'` → 100, 42704 on a miss.
  - `TestRegIdentifierInputAcceptsDashAndNumericOid` — `'-'` → 0 and a
    pure-digit string → numeric OID across regclass/regrole/regcollation.
  - `TestCoerceRowForConstraintChecksResolvesRegRoleAndCollation` — the choke
    point resolves `'postgres'`/`'C'` for `regrole`/`regcollation` columns and
    the coerced OIDs encode to 4 bytes.
- `internal/wal/pgoutput_reg_identifier_test.go`:
  `TestPgoDecodeRegFamilyMatchesPGNativeLayout` and
  `TestPgoPhysicalAlignRegFamily` widened to `regrole`/`regcollation`.
- Oracle E2E on a capped throwaway server (5533) vs PG 18.3 (65432):
  `INSERT`/`SELECT`/binary `COPY` of `regrole` and `regcollation` columns
  byte-identical. TEXT/CSV COPY deliberately differs (numeric OIDs — see
  "Deliberately NOT done").

## Gates

- `go build ./internal/...` clean.
- `go test ./internal/executor/ ./internal/catalog/ ./internal/wal/` — PASS.
- `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` — PASS.
- `TestPort_RegressSuite` — PASS.
- `scripts/tpch-spotcheck.sh` — PASS (Q12=2, Q13=35).
- Commit-hook pgbench smoke runs on the commit.

1 ledger row resolved (1302). Design `0119-0006-regrole-regcollation-4byte-storage.md`
+ README row.
