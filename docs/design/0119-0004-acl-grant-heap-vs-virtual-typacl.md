# 0119-0004 — Why the remaining GRANT/ACL surface (typacl / attacl / datacl) is heap-entangled, not a slice

**Status:** accepted (architectural finding + forward plan; no code change this loop)
**Milestone:** M0119-0004 (pg_dump 002–010 TAP / DU-002)
**Date:** 2026-06-30 (loop #83)
**Supersedes the per-slice "Still open under M0119-0004: column-level (`attacl`) / database
(`datacl`) / TYPE-DOMAIN (`typacl`)" footnote that 30+ DU-002 ACL slices have been carrying.**

## TL;DR

The DU-002 ACL **GRANT round-trip** thread (slices 330–356: table/sequence `relacl`,
schema `nspacl`, function/procedure `proacl`, owner/REVOKE/grant-option/grant-order
variants) is **complete for every object class that goopg serves *virtually***. The three
object classes still diverging from real `pg_dump` — `pg_type.typacl` (TYPE/DOMAIN),
`pg_attribute.attacl` (column-level), `pg_database.datacl` (DATABASE) — all share **one
architectural blocker**, and that blocker is *not* a missing parser branch or a missing
ACL renderer. It is a **catalog-storage asymmetry**:

> goopg records a GRANT **server-side** (`internal/server/query.go`), where the only thing
> in scope is the in-memory catalog ACL store — there is **no executor `*Context`**, hence
> no ability to write a heap page. That is sufficient for `relacl`/`nspacl`/`proacl`
> because `pg_class`/`pg_namespace`/`pg_proc` are **virtual** catalogs that re-project the
> ACL store live on every read. It is **insufficient** for `pg_type`, which — alone among
> the user-facing catalogs — stores **real heap rows** (for PG-standby basebackup compat,
> M0097-0022). The same is true of `pg_attribute` (`attacl`) and `pg_database` (`datacl`).

Closing these three is therefore a **new capability** ("ACL heap re-sync driven from the
GRANT path"), milestone-sized and high-blast-radius (heap mutation + MVCC visibility +
PG-standby read path + the dump read path). It is tracked as the M0119-0004 follow-up
**M0119-0004-ACLHEAP** below, not attempted as another single slice.

## How GRANT round-trips today (the virtual path)

A single-statement autocommit `GRANT`/`REVOKE` is intercepted **before the executor** in
`internal/server/query.go:69-87`:

```go
if strings.HasPrefix(upper, "GRANT ") && (connTx == nil || !connTx.InExplicit()) {
    s.tryRecordTableGrant(matchable)        // only s.cfg.Catalog in scope — NO *executor.Context
    ...WriteCommandComplete("GRANT")...
}
```

`tryRecordTableGrant` / `tryRecordTableRevoke` (`internal/server/grant_ddl.go`) resolve the
object OID and mutate the **OID-keyed in-memory ACL store**
(`GrantTablePrivilegeWithGrantOption`, `RevokeTablePrivilege`, `MaterializeOwnerACL`). They
dispatch by object keyword:

| object keyword | recorder | OID resolved via | renderer that pg_dump reads |
|----------------|----------|------------------|------------------------------|
| `TABLE` (default) | `tryRecordTableGrant` | `LookupTable` | `pg_class` virtual builder → `relaclTextLocked` (`catalog.go:4016`) |
| `SEQUENCE` | `tryRecordTableGrant` | `LookupTable` | `pg_class` virtual builder → `relaclTextLockedSeq` |
| `SCHEMA` | `recordSchemaGrant` | schema OID | `pg_namespace` virtual builder → `NamespaceACLText` (`catalog.go:4386`) |
| `FUNCTION`/`PROCEDURE`/`ROUTINE` | `recordFunctionGrant` | `lookupFunctionOID` | `pg_proc` **virtual view** → `ProcACLText` (`pg_proc_view.go:388`) |

The common thread: every renderer runs **at read time, live, from the ACL store**. The
GRANT recorder only has to touch the store; the next `SELECT ... FROM pg_class|pg_proc|...`
re-materializes the `aclitem[]` and `pg_dump`'s `buildACLCommands` diffs it against
`acldefault` client-side to re-emit exactly the GRANT/REVOKE. No heap write is involved, so
a Context-free server-side recorder is enough.

Crucially, `pg_proc` proves a heap row is **not** needed for the virtual path:
`execCreateFunction` (`operators_ddl.go:9057`) registers a user function **only** in the
`Routines()` registry — it writes **no** `pg_proc` heap row. `registerPgProcView`
(`internal/initdb/pg_proc_view.go`) rebuilds every `pg_proc` row from that registry on each
read and fills `proacl` from `ProcACLText`. `pg_proc` is virtual end-to-end for user
functions.

## Why TYPE/DOMAIN (`typacl`) is different — the heap

`pg_type` is the **one** user-facing catalog where goopg writes **real heap rows** for
user-defined objects. `CREATE TYPE`/`CREATE DOMAIN` call
`writeHeapRowCanonical(ctx, typeRel, pgTypeColumnsPG18(), buildUserPGTypeRowFor{Enum,Composite,Domain}(...))`
(`operators_ddl.go:10337/10371/10438`), and those builders
(`pg18_user_catalog_rows.go:1226/1273/1317/...`) bake **`typacl = NullDatum`**. There is
**no** `registerPgTypeView` overlay — a `SELECT ... typacl FROM pg_type` reads the heap
directly. So `pg_dump`'s `getTypes` (which selects `typacl`, `pg_dump.c:6078`) sees the
baked NULL and emits no GRANT, even after `GRANT USAGE ON TYPE ... TO role`. Today that
GRANT is a silent no-op: `grant_ddl.go:137` bails on every object in `nonTableGrantObjects`
(which includes `type`).

This heap-backing is **deliberate** and load-bearing: M0097-0022 makes the `pg_type` rows
PG18-canonical so a real PG18 standby attaching to a goopg basebackup can read user types.
A virtual `pg_type` overlay (mirroring `pg_proc`) would create a second source of truth and
risk diverging from the heap rows the standby reads — **not acceptable** without unifying
the two, so the virtual path is **not** the recommended fix here.

The same storage shape applies to the other two open cases:

- **`pg_attribute.attacl`** (column-level GRANT): `pg_attribute` is heap-backed; ALTER
  already re-syncs it via `delete-old-rows + syncTableToCatalogHeap` (see
  `pg_attribute_alter_needs_heap_resync` memory). Column GRANT would need the same re-sync
  driven from the GRANT path.
- **`pg_database.datacl`** (DATABASE GRANT): heap-backed **and** only emitted by `pg_dump`
  under `--create`; the DU-002 connsetup harness runs `--no-create`, so it is also
  **untestable** through the current round-trip harness. Doubly deferred.

## Forward plan — M0119-0004-ACLHEAP (the next real task, not a slice)

To round-trip `typacl` (and, by the same machinery, `attacl`), GRANT-on-heap-catalog must
acquire heap-write capability:

1. **Route GRANT/REVOKE on a heap-backed object through the executor** instead of the
   server short-circuit. `GRANT ... ON TYPE`/`DOMAIN` (and column-level) should fall through
   to `dispatchSimpleQueryViaExecutor`, where an `*executor.Context` (Pool, TxnMgr) is in
   scope. Keep the existing server-side fast path for the virtual classes
   (table/sequence/schema/function) — it is correct and lower-overhead.
2. **Add an executor GRANT operator** that (a) updates the OID-keyed ACL store exactly as
   the server recorder does, then (b) **re-syncs the heap row**: mirror
   `deleteTypeFromCatalogHeap` (`operators_ddl.go:10455`, already stamps xmax on a `pg_type`
   row by OID) to delete the stale row and re-insert via `writeHeapRowCanonical` with the
   `typacl` column filled from a new `TypeACLText(typeOID)` renderer (a trivial wrapper of
   `relaclTextLockedFor` with `typeACLPrivOrder = {USAGE}` / `ownerTypeACLString = "U"` —
   `acldefault('T', owner)` = `{=U/owner,owner=U/owner}`, structurally identical to the
   function `EXECUTE` default that `recordFunctionGrant` already handles).
3. **Mirror to the postgres DB** (`mirrorCatalogRelToPostgresDB(ctx, TypeRelationId)`,
   already called by the CREATE TYPE paths) so the standby/basebackup read path stays
   consistent.
4. **Gates (mandatory — this is heap + MVCC + dump + standby):** a new DU-002 connsetup
   slice (`CREATE TYPE` then `GRANT USAGE ON TYPE ... TO role` → assert `GRANT USAGE ON TYPE`
   in `pg_dump` stdout vs real PG 18.3); `TestE2E_PhysicalReplication` / recovery testport
   (standby still reads `pg_type`); full `internal/executor` + `internal/catalog` +
   `internal/parser` suites; **TPC-H Q12/Q13 spot-check** and **pgbench smoke**
   (executor-path change). Re-init the data dir if the `pg_type` row layout is touched.

### Progress — step 2 renderer landed (loop #84, 2026-06-30)

The renderer half of step 2 is **in tree** as a self-contained, behaviour-neutral
building block (no GRANT path calls it yet, so blast radius is nil):

- `catalog.InMemory.TypeACLText(typeOID)` (`internal/catalog/catalog.go`) — the
  pg_type analogue of `ProcACLText`, delegating to the object-agnostic
  `relaclTextLockedFor` with the new `typeACLPrivOrder = {USAGE/'U'}` and
  `ownerTypeACLString = "U"`. A type's `acldefault('T', owner)` =
  `{=U/owner,owner=U/owner}` (owner + PUBLIC both hold USAGE) is structurally
  identical to the function `EXECUTE` default, so the projection reuses the proven
  proacl machinery verbatim. Added to the `Catalog` interface.
- Unit tests (`internal/catalog/relacl_test.go`): `TestTypeACLText`,
  `TestTypeACLGrantWithGrantOption`, `TestTypeACLRevokeFromPublic`,
  `TestTypeACLRevokeFromOwner` — mirror the ProcACL goldens with USAGE, pinning
  NULL→materialize, grant-option `*`, REVOKE-FROM-PUBLIC, and owner-side REVOKE
  (leaves `{=U/postgres}` / empties to `{}`).

### Progress — step 1 parser capture landed (loop #85, 2026-06-30)

The AST half of step 1 is **in tree** as a self-contained, behaviour-neutral
building block (no consumer reads it yet, so blast radius is nil):

- `parser.CompatNoopStmt.TypeACL *TypeACLChange` (`internal/parser/ast.go`) — a new
  optional field set **only** for `GRANT`/`REVOKE … ON TYPE|DOMAIN …`. The new
  `TypeACLChange` carries `{Revoke, IsDomain, Privileges, TypeNames, Grantees,
  WithGrantOption}`. Other object classes leave it nil, so the existing
  virtual-path recorders (table/schema/function) are untouched.
- The GRANT/REVOKE scan in `internal/parser/parser.go` gained explicit `ON TYPE` /
  `ON DOMAIN` cases (placed before the `grantNonTableClass` catch-all, which also
  matches type/domain) that capture the full token run and parse it via
  `buildTypeACLChange` + token-split helpers (`tokIndexOf`, `splitTokRuns`,
  `splitTokPrivileges`, `splitTokObjectNames`, `splitTokRoles`,
  `objectNameFromTokens`). `DatabaseACL`/`TableACL` capture is unchanged (ON
  TYPE/DOMAIN was, and still is, never a pg_class ACL change → `TableACL == ""`).
- Unit tests (`internal/parser/op_grant_typeacl_test.go`): `TestParseGrantTypeACL`
  (USAGE, ALL, ALL PRIVILEGES, DOMAIN, REVOKE-FROM-PUBLIC, WITH GRANT OPTION,
  multi-name, multi-grantee, CASCADE/GRANTED-BY stripping) and
  `TestParseGrantNonTypeLeavesTypeACLNil` (table/schema/sequence/function/database
  leave `TypeACL` nil). Full `internal/parser` suite green; `go build ./...` clean.

This unblocks the executor wiring: the GRANT details now reach a parsed AST node
that `execCompatNoop` runs with a full `*executor.Context` in scope.

### Progress — step 2 aclitem[] binary codec landed (loop #86, 2026-06-30)

Wiring loop #85's parser capture toward the heap revealed a **previously-unknown
foundational blocker** that step 2 silently assumed was already solved: the heap
codec could **not** encode a *non-empty* `aclitem[]`. `pgTypeColumnsPG18`'s
`typacl` column is `aclitem[]`, and `codec.go`'s `case "aclitem[]"` only handles
(a) a pre-built `KindBytes` blob (passthrough) or (b) `emptyArrayTypeBytes(1033)`
— a `NewStringDatum("{=U/postgres,…}")` falls to the empty path and **silently
drops the ACL**. The generic `encodeArrayValuePG` (`codec_array.go`) has no
`aclitem` element-type entry either, so it would mis-encode as a `text` array
(`elemtype 25`, 4-byte-varlena elements) that a PG18 standby / `pg_dump`'s
`getTypes` cannot parse (it expects `elemtype 1033` + 16-byte fixed `AclItem`
structs). Because `pg_type` is heap-backed precisely for PG18-standby basebackup
parity, an internally-round-tripping-but-non-PG-native blob would fail
`TestE2E_PhysicalReplication` — so the codec must be byte-faithful.

Landed as a self-contained, behaviour-neutral building block (no caller yet →
nil blast radius):

- `internal/executor/codec_aclitem.go` — the dedicated PG-native `_aclitem`
  (OID 1034) array codec. `encodeAclItemArrayText(aclText, resolveOID)` parses
  the canonical aclitemout array text the catalog renderer produces
  (`{grantee=privs/grantor,…}`) into the on-disk ArrayType varlena: the same
  24-byte 1-D no-NULL header as `codec_array.go` but `elemtype 1033` and one
  16-byte `AclItem` per entry (`ai_grantee` Oid + `ai_grantor` Oid + `ai_privs`
  AclMode `uint64`, low 32 bits = privilege bits / high 32 bits = grant-option
  bits, per `acl.h` `ACL_GRANT_OPTION_FOR(privs) = privs << 32`). The empty
  grantee resolves to `ACL_ID_PUBLIC` (0). `decodeAclItemArrayText(blob,
  resolveName)` inverts it. The codec is **pure** — role name↔OID resolution is
  injected by the caller (the heap re-sync path will pass the per-role OID
  registry), so it has no catalog dependency and is unit-testable in isolation.
  Privilege letters follow `ACL_ALL_RIGHTS_STR = "arwdDxtXUCTcsAm"` (letter at
  index `i` ⇒ bit `1<<i`), and role-name quoting mirrors PG's `putid`.
- Tests `internal/executor/codec_aclitem_test.go`: `TestAclModeFromPrivLetters`
  (priv-letter↔AclMode incl. grant option), a **byte-exact golden** for
  `{=U/postgres}` (the type-default PUBLIC USAGE entry — 40-byte blob, header +
  one AclItem), `TestAclItemArrayRoundTrip` (PUBLIC, owner-only, owner+PUBLIC+
  grantee, grant-option, relacl-style multi-priv, non-owner grantor),
  `TestAclItemArrayEmpty` (owner-revoke-all → PG-valid empty `_aclitem`), and
  `TestAclItemArrayQuotedRole` (a quoted role name with an embedded comma is not
  torn by the top-level splitter). Full `internal/executor` suite green;
  `go build ./...` clean.

This is the encode/decode primitive every later heap-ACL re-sync (typacl now,
attacl next) builds on; the GRANT path can now produce a PG-faithful `typacl`
heap value instead of silently storing an empty/mis-typed array.

### Progress — the heap-write + read half LANDED (loop #87, 2026-06-30) — `typacl` round-trips

The high-blast-radius half is **in tree** and the `typacl` GRANT round-trip is **complete**.
`GRANT USAGE ON TYPE public.gtype TO typg_grantee` → real `pg_dump` emits
`GRANT ALL ON TYPE public.gtype TO typg_grantee;`, byte-identical to PG 18.3
(`TestPort_PgDumpConnectionSetup` slice 357, strict).

**Step 1 — dispatch reroute** (`internal/server/query.go`): the autocommit GRANT/REVOKE
server fast path now skips any statement whose upper-cased text contains `" ON TYPE "` or
`" ON DOMAIN "` (`isHeapACLObject`), so it falls through the simple-query switch to
`dispatchSimpleQueryViaExecutor`. The virtual classes (table/sequence/schema/function)
keep the lower-overhead server recorder. Explicit-txn GRANT already routed to the executor,
so the command-tag / txn-state handling was already in place.

**Step 2b — executor ACL store update + heap re-sync** (`internal/executor/operators_ddl.go`):
`execCompatNoop` gained a `s.TypeACL != nil` branch → `execTypeACLChange`, which resolves
each `TypeName` to its OID + builder kind (`resolveUserTypeOID` over
`LookupEnum`/`LookupDomain`/`LookupCompositeType`) and updates the OID-keyed ACL store
exactly as `recordFunctionGrant`/`recordFunctionRevoke` do for `proacl`, but with the type's
sole privilege **USAGE** (`typeACLAllPrivs = {USAGE}`; GRANT seeds the implicit PUBLIC USAGE,
REVOKE seeds owner+PUBLIC while `typacl` is still NULL). It then calls
`resyncTypeACLHeapRow`, which rebuilds the base `pg_type` row via the matching
`buildUserPGTypeRowFor{Enum,Domain,Composite}`, fills the final `typacl` column with
`NewBytesDatum(encodeAclItemArrayText(TypeACLText(oid), RoleOID-resolver))` (or `NullDatum`
when the ACL is empty), and applies the proven delete-old + insert-new + mirror template:
`deleteTypeFromCatalogHeap(DefaultDBOid, oid, Tx.XID)` (xmax-stamps the stale row) →
`writeHeapRowCanonical(pg_type, …)` → `mirrorCatalogRelToPostgresDB(TypeRelationId)`.
Gated on `catalogHeapSyncAvailable` (a no-op in the in-memory VM fixture).

**Step 2c — read path** (`internal/executor/codec.go` + `operators_storage.go`): a new
`case "aclitem[]", "_aclitem":` in `decodePhysicalPGValueMctx` returns the full `_aclitem`
varlena (incl. its 4-byte header) as **KindBytes** instead of the shared `default` branch's
meaningless string. The `seqScanOp` then renders it: armed only when scanning `pg_type`
(`typeACLColIdx`/`typeACLOidIdx`/`typeACLCat` resolved once in `Open`), a per-row hook
(modelled on the existing enum injector) converts the KindBytes `typacl` to canonical
aclitemout text via `decodeAclItemArrayText` with the new
`catalog.InMemory.RoleNameForOID` reverse resolver (0→PUBLIC, 10→postgres, registered role
→ case-preserved name, else numeric). So a goopg-served `SELECT typacl FROM pg_type`
(pg_dump's `getTypes`) reads the same role-NAME text `pg_class.relacl` projects virtually —
**the heap is the single read source of truth**, no second overlay. Dormant for every
non-pg_type scan and for an unpopulated (NULL) `typacl`; `pg_class.relacl` is virtual so it
never hits the decode case.

**Step 3 — mirror**: `resyncTypeACLHeapRow` calls `mirrorCatalogRelToPostgresDB` so the
session (which reads from DBOid=5) and an attaching PG18 standby see the re-synced row.

**Step 4 — gates (all run, this loop)**: `TestPort_PgDumpConnectionSetup` (the DU-002
TYPE-grant round-trip, PASS); `TestE2E_PhysicalReplication` (standby still reads `pg_type`,
PASS); `-race ./internal/executor ./internal/catalog ./internal/storage ./internal/mvcc`
(PASS); full `internal/executor` / `internal/catalog` / `internal/parser` / `internal/server`
suites (PASS); TPC-H Q12/Q13 spot-check + pgbench smoke (pre-commit). Unit:
`TestRoleNameForOID`, the `aclitem[]` decode case, and the existing `codec_aclitem` round-trip
goldens.

**Still open under M0119-0004-ACLHEAP** (follow-ups, NOT regressions): `attacl` (column-level
GRANT) reuses this exact template against `pg_attribute` (heap-backed; the
`pg_attribute_alter_needs_heap_resync` precedent); `datacl` stays deferred (`pg_database` is
heap-backed AND only emitted by `pg_dump --create`, which the `--no-create` connsetup harness
cannot exercise). Type-ACL durability across a goopg restart is in-memory-only, matching every
other goopg ACL (relacl/nspacl/proacl) — the heap `typacl` blob is for standby/basebackup
parity; goopg's own read derives from the same store the heap was synced from.

Catalog accessors used: `LookupEnum`/`LookupEnumByOID`, `LookupCompositeType`/`...ByOID`,
`LookupDomain`/`...ByOID`, and the new `RoleNameForOID`.

### Progress — `attacl` step 2 renderer + column-ACL store landed (loop #88, 2026-06-30)

Low-blast-radius building block, the column analogue of loop #84's `TypeACLText`: nothing on
the GRANT path calls it yet, so live behaviour is unchanged. Lands the catalog half of the
`attacl` follow-up.

**Key divergence from typacl/proacl that drove the design:** a column's
`acldefault('c', owner)` is **empty** — the owner and PUBLIC hold no implicit column
privilege. So `attacl` is NULL until the first column GRANT, has **no leading owner aclitem
and no implicit PUBLIC entry**, and returns to NULL once the last column privilege is revoked
(a table empties to `{}`, a column to NULL). This means the `attacl` renderer **cannot** reuse
`relaclTextLockedFor` (which unconditionally prepends the owner entry) — it needs a dedicated
renderer, and the `relACLEmptied`/`relACLOwnerRevoked` machinery does not apply.

Landed (`internal/catalog/catalog.go`):
- `attrACLKey{relOID uint32; attNum int16}` — a struct key, not a packed `relOID<<16|attnum`
  uint32, because real table OIDs routinely exceed 2^16 and would overflow.
- New composite-keyed stores `attrACLs map[attrACLKey]map[string]map[string]bool` (grantee →
  priv → grant-option) + `attrACLOrder map[attrACLKey][]string` (grant order), initialized in
  `NewInMemory`.
- `attrACLPrivOrder = {INSERT/a, SELECT/r, UPDATE/w, REFERENCES/x}` — the column-grantable
  subset in canonical aclitemout bit order (no DELETE/TRUNCATE/TRIGGER/MAINTAIN).
- `GrantColumnPrivilege` / `GrantColumnPrivilegeWithGrantOption` / `RevokeColumnPrivilege`
  (mirror the table-ACL recorders; share `roleACLDisplay`) + `AttrACLText(relOID, attNum)`
  renderer (grantees only, grant order, PUBLIC→empty grantee, `aclQuoteName` for unsafe
  names). All four added to the `Catalog` interface.
- Tests `TestAttrACLText` / `TestAttrACLGrantWithGrantOption` / `TestAttrACLRevoke` /
  `TestAttrACLGranteeNameRendering` (case-preserved-unquoted + unsafe-char-quoted).

**Remaining `attacl` steps** (the high-blast-radius half — a dedicated loop, per the
WAL/MVCC practice card): (1) parser — capture `GRANT priv (col,…) ON TABLE t TO role` into a
new `CompatNoopStmt.AttrACL *AttrACLChange` (the column-list is the ON-detection signal,
mirroring `TypeACLChange`); (2) executor — `execAttrACLChange` + `resyncAttrACLHeapRow`
(delete-old `pg_attribute` rows + `syncTableToCatalogHeap` per the
`pg_attribute_alter_needs_heap_resync` precedent; set `attacl` via
`encodeAclItemArrayText(AttrACLText(relOID,attnum), …)`); (3) read — a `pg_attribute` seqscan
`attacl` decode hook, the sibling of the `pg_type` `typacl` hook; (4) the DU-002 connsetup
slice `GRANT SELECT (col) ON TABLE t TO role` → assert pg_dump emits it.

## Why not just do it now

Single-loop autonomous discipline + the hard-won "silent row-count / visibility regression
is this project's most expensive failure mode" rule. Step 1 changes the simple-query
dispatch path and step 2 mutates a heap catalog that a PG standby reads — exactly the
surface the WAL/MVCC practice card flags as highest-blast-radius. It deserves a dedicated
loop with the full gate set, not a rushed slice tacked onto the end of a 26-slice ACL run.
The tractable virtual-path ACL surface **is done**; this doc converts 83 loops of implicit
"still open" footnotes into one explicit, costed, ready-to-execute plan.

## Files referenced

- `internal/server/query.go:69-87` — server-side GRANT/REVOKE interception (no Context).
- `internal/server/grant_ddl.go` — `tryRecordTableGrant/Revoke`, `recordSchemaGrant`,
  `recordFunctionGrant`, `nonTableGrantObjects` (TYPE bail at line 137).
- `internal/catalog/catalog.go:8757` `relaclTextLockedFor` (object-agnostic ACL renderer);
  `:4016` `relaclTextLocked` (pg_class), `:8731` `NamespaceACLText`, `:8748` `ProcACLText`.
- `internal/initdb/pg_proc_view.go:388` — `pg_proc` virtual view projecting `proacl`.
- `internal/executor/operators_ddl.go:10337/10371/10438` — CREATE TYPE heap writes;
  `:10455` `deleteTypeFromCatalogHeap` (the re-sync template); `:9057` `execCreateFunction`
  (registry-only, proves the virtual path needs no heap row).
- `internal/executor/pg18_user_catalog_rows.go:1226/1273/1317` — `typacl = NullDatum` bake.
- `postgres/src/bin/pg_dump/pg_dump.c:6078/6152` — `getTypes` reads `typacl`.

## Related

- `pg_attribute_alter_needs_heap_resync` memory — the `attacl` re-sync precedent.
- `goopg_pg_class_virtual_pg_attribute_heap` memory — the virtual/heap split.
- `pgdump_catalog_queries_run_serverside` memory — pg_dump resolver SQL runs on goopg.
- Design `0119-0004-grant-relacl-pgdump.md` … `0119-0004-partial-revoke-keeps-slot-relacl.md`
  — the completed virtual-path ACL slices (330–356).
