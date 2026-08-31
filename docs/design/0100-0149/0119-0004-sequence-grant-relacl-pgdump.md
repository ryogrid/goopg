# DU-002 slice 333 — `GRANT … ON SEQUENCE` (sequence `relacl`) round-trip in pg_dump

Status: accepted
Milestone: M0119-0004 (pg_dump fidelity)

## Problem

A sequence-level privilege grant must round-trip through `pg_dump`:

```sql
CREATE SEQUENCE public.grant_seq;
CREATE ROLE seq_role;
GRANT USAGE ON SEQUENCE public.grant_seq TO seq_role;
```

`pg_dump` must re-emit `GRANT USAGE ON SEQUENCE public.grant_seq TO seq_role;`.

`pg_dump` treats sequences as relations: its `getTables` query (`pg_dump.c`)
selects `c.relacl` for every dumped relkind including `'S'`, and computes the
ACL baseline with

```sql
acldefault(CASE WHEN c.relkind = 'S' THEN 's'::"char" ELSE 'r'::"char" END,
           c.relowner) AS acldefault
```

so a sequence's privileges are diffed against `acldefault('s', owner)`
(`{postgres=rwU/postgres}` — USAGE/SELECT/UPDATE). `dumpTableSchema` then passes
the object type `"SEQUENCE"` (not `"TABLE"`) to `dumpACL`, and `buildACLCommands`
(`dumputils.c`) parses the `aclitem[]` text **client-side** and emits the
`GRANT … ON SEQUENCE …` diff. No server-side `aclexplode`/`aclitemout` is
involved.

Before this slice goopg lost the privilege two ways:

1. `internal/server/grant_ddl.go`'s `tryRecordTableGrant` listed `sequence` in
   `nonTableGrantObjects` and **bailed** — `GRANT … ON SEQUENCE` was a pure
   no-op, nothing recorded.
2. Even had it recorded, `relaclTextLocked` was hard-wired to the **table**
   privilege order (`arwdDxtm`) and table owner-default string, so a sequence's
   `relacl` would have rendered with the wrong owner baseline (`arwdDxtm`
   instead of `rwU`) and dropped the `USAGE` letter entirely (USAGE is not in
   the table privilege set).

## Fix (dump-fidelity only)

Sequences already flow through the same `pg_class` virtual builder as tables
(they are virtual `catalog.Table`s with `IsSequence=true`, emitted with
`relkind='S'` since DU-002 slice 116), and `acldefault('s', owner)` already
works (`evalAclDefault`, slice 2). Only the recorder and the `relacl` renderer
needed object-type awareness.

### `internal/catalog/catalog.go`

- Extract the privilege-letter pair into a named type `aclPrivLetter`.
- Add `sequenceACLPrivOrder` = `SELECT('r')`, `UPDATE('w')`, `USAGE('U')` (the
  canonical `aclitemout` bit order for sequence privileges) and
  `ownerSequenceACLString = "rwU"` (matches `acldefault('s', owner)`).
- Refactor `relaclTextLocked` into an object-type-agnostic core
  `relaclTextLockedFor(relOID, privOrder, ownerString)`; keep
  `relaclTextLocked` (tables) delegating to it, and add `relaclTextLockedSeq`
  (sequences). The grant-option `*` suffix logic (slice 332) is shared.
- The `pg_class` virtual builder calls `relaclTextLockedSeq(t.OID)` when
  `t.IsSequence`, else `relaclTextLocked(t.OID)`.

The catalog ACL store (`tableACLs`, keyed by relation OID) is shared across
object classes — only the rendering differs — so a sequence grant and a table
grant never collide, and `HasTablePrivilege` / the `truncate-conflict`
enforcement path are untouched.

### `internal/server/grant_ddl.go`

- Remove `sequence` from `nonTableGrantObjects`.
- `tryRecordTableGrant` strips an optional leading `SEQUENCE` keyword (as it
  already does for `TABLE`), sets `isSequence`, and expands `ALL [PRIVILEGES]`
  to `allSequencePrivileges` (`USAGE/SELECT/UPDATE`) instead of the table set.
- `parseGrantPrivileges` takes the applicable `allPrivs` set as a parameter.

The recorded privileges go into the same store via the existing
`GrantTablePrivilegeWithGrantOption` by OID — a sequence GRANT … WITH GRANT
OPTION therefore round-trips too (the `*` rendering is shared).

## Blast radius

A relation with no recorded sequence grant projects exactly as before (NULL
`relacl`), and the table GRANT path is byte-for-byte unchanged (same order list,
same owner string). Zero impact on the physical-heap `pg_class` path
(`pg18_user_catalog_rows.go`) — GRANT is a runtime-only mutation and goopg does
no in-place update of on-disk shared catalogs.

## Gates

- `TestRelaclTextSequence` (catalog): `GRANT USAGE` → `{postgres=rwU/postgres,
  seq_role=U/postgres}`; multi-priv canonical order `rU` (and a table-only
  privilege like `INSERT` is dropped from a sequence rendering); grant-option
  `UPDATE` → `rw*U`.
- `TestPort_PgDumpConnectionSetup` **DU-002 slice 333**: `CREATE SEQUENCE
  public.grant_seq` + `CREATE ROLE seq_role` + `GRANT USAGE ON SEQUENCE … TO
  seq_role` → `GRANT USAGE ON SEQUENCE public.grant_seq TO seq_role;`
  byte-identical vs real `pg_dump` 18.3.
- `catalog` + `server` packages, `truncate-conflict` isolation suite PASS;
  `go build ./...` clean; pgbench smoke = pre-commit hook.

## Still open under M0119-0004

Column-level GRANT (`pg_attribute.attacl`, a heap re-sync), schema/database
GRANT projection, REVOKE-of-default modelling, reserved-keyword-named-role
quoting; extended-protocol commit-time deferral.

## Oracle

`postgres/src/bin/pg_dump/pg_dump.c` (`getTables` `acldefault` CASE,
`dumpTableSchema` objtype `"SEQUENCE"` → `dumpACL`),
`postgres/src/bin/pg_dump/dumputils.c` (`buildACLCommands`),
`postgres/src/backend/utils/adt/acl.c` (`acldefault`, `aclitemout`).
