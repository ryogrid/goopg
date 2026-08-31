# 0119-0004 — ACL grantee grant-order preservation in relacl/proacl/nspacl (DU-002 slice 354)

Status: accepted
Milestone: M0119-0004 (pg_dump 002–010 TAP / DU-002 catalog-view parity battery)

## Problem

goopg materializes `pg_class.relacl` (and the sibling `pg_proc.proacl`,
`pg_namespace.nspacl`) from an in-memory ACL store so that pg_dump's client-side
`buildACLCommands` can re-emit the original `GRANT`/`REVOKE` statements. The
store, `InMemory.tableACLs`, is keyed `relOID → role → privilege → grant-option`
— a `map`, which has no ordering.

`relaclTextLockedFor` (the object-type-agnostic core that renders the aclitem
array) collected the grantee role names and **`sort.Strings`-sorted** them before
emitting one aclitem per grantee. PostgreSQL does **not** sort: `aclupdate`
(`src/backend/utils/adt/acl.c`) modifies an existing aclitem in place but
**appends a brand-new grantee's aclitem to the end of the array**, so
`pg_class.relacl` preserves **grant order**.

The two orders coincide only when grants happen to be issued in alphabetical
order. Every prior multi-grantee slice (352 differing-priv, 353 same-priv) did
exactly that, masking the divergence. A reverse-order grant exposes it:

```sql
GRANT SELECT ON TABLE t TO og_role_z;   -- granted first
GRANT SELECT ON TABLE t TO og_role_a;   -- granted second
```

Real PG 18.3 (verified against `./postgres/local_install`):

```
relacl = {postgres=arwdDxtm/postgres,og_role_z=r/postgres,og_role_a=r/postgres}
```

`z` before `a` — grant order. goopg's `sort.Strings` rendering produced
`og_role_a` before `og_role_z`, so pg_dump fanned the aclitem array out into two
`GRANT` lines in the **wrong order** vs real pg_dump — a byte-level round-trip
divergence.

## Fix

Track per-relation grant order alongside the unordered privilege store and render
from it.

- **New field** `InMemory.tableACLOrder map[uint32][]string` — for each relOID,
  the order in which non-owner grantee roles first appeared in a GRANT. The owner
  (`postgres`) is omitted; it is always rendered first, separately. A re-GRANT to
  an existing grantee does not move it (matches `aclupdate`'s in-place update).
- **`GrantTablePrivilegeWithGrantOption`**: when a role's privilege map is created
  for the first time (i.e. its first grant on this relation) and the role is not
  the owner, append it to `tableACLOrder[relOID]`.
- **`RevokeTablePrivilege`**: when a role's privilege set becomes empty and its
  `byRole` entry is dropped, remove it from the order list
  (`dropTableACLOrderRole`, which also deletes the slice when it empties).
- **Teardown sites**: every `delete(c.tableACLs, oid)` (whole-relation revoke,
  `DropTableACL`, the two table-drop paths) gains a matching
  `delete(c.tableACLOrder, oid)`.
- **`relaclTextLockedFor`**: replace the `sort.Strings(roles)` collection with an
  iteration over `tableACLOrder[relOID]` (skipping the owner and any stale entry
  whose role is no longer in `byRole`). As a defensive backstop, any grantee
  present in `byRole` but missing from the order list is appended in sorted order
  so a grant can never be silently dropped.

Because all four object types (table/sequence/schema/function ACLs) funnel through
the single `tableACLs` store and the single `relaclTextLockedFor` core, this one
change fixes grantee ordering uniformly for `relacl`, `proacl`, and `nspacl`.

This corrects ordering **only** for the grantee aclitems. The pre-existing choice
to render the owner entry first (whereas PG's `acldefault` for a function lists
PUBLIC before the owner) is unchanged; that owner/PUBLIC ordering difference is
invisible to pg_dump (both are part of `acldefault` and cancel out in the diff),
and grantees still land after the defaults in both goopg and PG.

## Oracle

- `src/backend/utils/adt/acl.c` — `aclupdate` (append-on-new-grantee),
  `acldefault` (owner/PUBLIC default array).
- Behavior verified against `./postgres/local_install` PG 18.3:
  - reverse-order table grant → `{postgres=…,og_role_z=r/…,og_role_a=r/…}`
  - PUBLIC-then-named table grant → `{postgres=…,=r/…,named_role=a/…}`
  - PUBLIC-then-grantee function grant → `{=X/…,postgres=X/…,grantee_fn=X/…}`
    (grantee last; owner/PUBLIC ordering is PG's function default, pg_dump-invisible).

## Tests / gates

- `internal/catalog/relacl_test.go`: four existing unit tests updated — they had
  encoded the old `sort.Strings` (alphabetical) order, which the real-PG
  verification above proved wrong. `TestRelaclText` (grantee_role before
  another_role), `TestRelaclTextPublic` (PUBLIC before named_role),
  `TestProcACLText` / `TestProcACLGrantWithGrantOption` (grantee_fn last) now
  assert grant order. All catalog tests PASS.
- `internal/testport/pgdump_connsetup_test.go`: **new DU-002 slice 354** — a
  reverse-grant-order table (`GRANT … TO og_role_z` then `og_role_a`) whose two
  pg_dump `GRANT` lines must appear in z-before-a order (asserted via
  `strings.Index` positions), confirmed byte-identical vs real pg_dump 18.3.
  Slices 352/353 comments de-staled (they no longer rely on `sort.Strings`).
  `TestPort_PgDumpConnectionSetup` PASS.
- `internal/executor`, `internal/initdb` suites PASS; `go build ./...` clean.
- pgbench TPC-B smoke = pre-commit hook (no executor/planner hot-path touched;
  the change is confined to the catalog ACL store, exercised only by GRANT/REVOKE
  and pg_dump).

## Blast radius

Confined to the catalog ACL store. For any grant sequence issued in alphabetical
order (every prior fixture, and the common case), grant order == sorted order, so
the rendered relacl is byte-unchanged. The only behavioral change is for
non-alphabetical grant sequences, where goopg now matches real PostgreSQL.

## Still open under M0119-0004

- Column-level GRANT (`pg_attribute.attacl`, heap re-sync — entangled).
- TYPE/DOMAIN GRANT (`pg_type.typacl`) — `grant_ddl.go` bails on non-table/
  sequence objects, so typacl is unmodelled (new ACL surface).
- pg_dump 002–010 catalog parity (further slices).
- Extended-protocol commit-time deferral (architecturally entangled, see
  goopg_extended_protocol_autocommit memory).
