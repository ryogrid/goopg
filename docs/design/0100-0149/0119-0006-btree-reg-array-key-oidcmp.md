# M0119-0006 — btree reg*[] (and scalar reg*) keys encode as 8-byte oidcmp

**Status:** accepted (2026-08-14, 81st slice). Resolves deferral-ledger row 1352.

## The bug

A B-tree index over a `reg*[]` array column (`regclass[]`/`regtype[]`/
`regprocedure[]`/`regrole[]`/`regcollation[]`/`regproc[]`) — and, it turned out, a
scalar `reg*` column too — could not be built or maintained. `CREATE INDEX … ON
t(regclass_col[])` raised `0A000 btree v0 cannot index column … of type regclass`,
and the INSERT maintain path *swallowed* the key-encode error, leaving the index
silently empty. Root cause: `encodeBTreeKeyForColumn` had no `reg*` arm, so a
`reg*` element fell through the scalar switch to the 0A000 fallback.

## Two corrections to the ledger row's premise

The row-1352 resume point said "the scalar `encodeBTreeKeyForColumn` already stores
reg* scalars 4-byte (66th slice) — align the array path with it." Both halves of that
were wrong, and the correction drives the design:

1. **No scalar reg* btree-key arm existed.** The 66th slice was heap-storage only
   (`codec.go`/`copy_binary.go`/`pgoutput.go` store a reg* datum as a 4-byte OID);
   it never touched btree keys. Only `regproc` rode the numeric-only `oid` arm
   (`isOidType` covered `oid`+`regproc`). `regclass`/`regtype`/`regprocedure`/
   `regrole`/`regcollation` scalar indexed columns also raised 0A000. So this slice
   **adds both** the scalar and the array reg* arm — the array path flows through the
   scalar arm automatically (`encodeArrayBTreeKey` delegates every element to
   `encodeBTreeKeyForColumn` with `elemCol.IsArray=false`).
2. **The KEY is 8 bytes, not 4.** The stored *element* is a 4-byte OID, but the key
   must be the 8-byte unsigned `oidcmp` form (`btree.EncodeInt8`), because every
   reg* type's default btree opclass is `oid_ops` and `array_cmp` compares reg*
   elements with unsigned `oidcmp` (postgres `arrayfuncs.c:3991`). Encoding 4 bytes
   would have produced a key that round-trips but sorts differently from PG.

## Design decisions

1. **Key width = 8-byte unsigned oidcmp** (`btree.EncodeInt8(int64(oid))`), byte-
   identical to the oid arm's form. `decodeScalarBTreeKey`'s oid case widened to
   `isOidType || isRegType` — one 8-byte inversion serves all seven; the datum is
   the raw OID and only the *render* side does OID→name.
2. **`regproc` joins the reg* family** (`isRegType`, six members), removed from the
   numeric-only `isOidType` arm. The 80th slice already put regproc in the reg*
   heap family (`encodeArrayElem`/`ElemTypeInfo`); this makes the btree key agree.
   A regproc *name* element now resolves via `regIdentifierInput` instead of 22P02.
3. **Name→OID resolution is `regIdentifierInput`** (reg_identifier.go:216), the same
   resolver the 80th-slice heap element path uses. `parseRegDashOrOid` runs first
   (`-`→OID 0, pure-digit→numeric OID via `oidin`, never name resolution), then the
   per-type catalog lookup yielding each family member's own miss SQLSTATE (42P01
   regclass / 42704 regtype,regrole,regcollation / 42883 regproc,regprocedure /
   42602 syntax / 22003 range). `keyExecError` adapts the plain-error returns to
   `*ExecError`, preserving an already-`*ExecError` miss.
4. **ctx threading with a nil-ctx numeric-passthrough contract.** `encodeBTreeKeyForColumn`
   and `encodeArrayBTreeKey` gain `ctx *Context`; the build/probe/maintain/arbiter
   callers pass their in-scope ctx. `indexColumnFingerprint`/`indexKeyFingerprint`
   pass nil: that path sees a heap-decoded numeric-OID string, which
   `regIdentifierInput`'s numeric passthrough resolves; a bare name reaching a
   nil-ctx caller errors rather than silently storing — the encodeArrayElem nil-ctx
   contract (codec_array.go:263-265).
5. **Decode/render twin lands in the same slice** (Hard-won Rule #2).
   `arrayKeyElemRenderer` gains a reg* arm rendering OID→name via `st.RegOut`
   (OID 0→`-`, dangling→numeric, else name), mirroring `DecodeElemStyled`
   pgarray.go:427-444. With the renderer arm present, `indexKeyColumnIsDecodable`
   admits reg*[] *automatically* (it requires `arrayKeyElemRenderer != nil`), so no
   `btree_key_decodable.go` edit was needed and reg*[] index-only scans activate.
6. **`isSupportedBTreeKeyType` admits `isRegType`** — the 0A000 that blocked `CREATE
   INDEX` actually fired at this CREATE-side gate, not inside the encoder.

## Oracle

- `regclassin` postgres/src/backend/utils/adt/regproc.c:882 (`parseDashOrOid` :890,
  `RangeVarGetRelid`, miss 42P01 :910-914); `regclassout` :943 (InvalidOid→`-`,
  visible→`quote_qualified_identifier`, dangling→numeric :986-991);
  `regclassrecv`=oidrecv :1000 (binary form is a 4-byte OID).
- `array_cmp` postgres/src/backend/utils/adt/arrayfuncs.c:3991 — element-wise via
  the element type's `cmp_proc` (oidcmp for reg*, unsigned 32-bit), NULL rules
  :4065-4081. `btarraycmp`=array_cmp :3979.

## Gates

- Targeted btree-key probe, `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh`
  (all packages), `scripts/tpch-spotcheck.sh` (Q12=2, Q13=35) — all PASS.

## Follow-on (deferral ledger row 1353)

WAL pgoutput still renders a reg*[] blob numerically (`pgoDecodePhysicalValue` has
nil `OutputStyle.RegOut`) — pre-existing, tracked separately as row 1353.
