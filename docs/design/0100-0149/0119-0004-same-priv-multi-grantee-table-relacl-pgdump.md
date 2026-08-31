# DU-002 slice 353 — same-privilege multi-grantee table (relacl) round-trip in pg_dump

Status: landed. Part of M0110-0001 / M0119-0004 (DU-002 pg_dump round-trip
slices). Test-only — NO engine change.

## What

Two grantees granted the **same** privilege on one table must each round-trip
through pg_dump as their own `GRANT` line — PostgreSQL never merges grantees into
a single `GRANT … TO a, b;`, even when the privilege sets are byte-identical:

```sql
CREATE TABLE public.samegrant_t(id int);
CREATE ROLE sg_role_a;
CREATE ROLE sg_role_b;
GRANT SELECT ON TABLE public.samegrant_t TO sg_role_a;
GRANT SELECT ON TABLE public.samegrant_t TO sg_role_b;
```

PostgreSQL 18.3 materializes `pg_class.relacl` as:

```
{postgres=arwdDxtm/postgres,sg_role_a=r/postgres,sg_role_b=r/postgres}
```

and `pg_dump --no-sync` emits exactly:

```sql
GRANT SELECT ON TABLE public.samegrant_t TO sg_role_a;
GRANT SELECT ON TABLE public.samegrant_t TO sg_role_b;
```

(captured from real PG 18.3 in `./postgres/local_install`).

## Why a separate slice from 352

Slice 352 covered two grantees with **differing** privileges (SELECT vs INSERT).
The same-privilege case is the most tempting target for a (wrong) grantee-merge
optimization — `GRANT SELECT TO a, b;` reads as a natural collapse — so it gets
its own guard with an explicit negative assertion against the merged form
(`TO sg_role_a, sg_role_b`).

## Why it already works

No new engine code is needed:

- Each `GRANT` is recorded independently under the OID-keyed `tableACLs` store
  via `GrantTablePrivilege`, one inner `map[priv]bool` per grantee role. Two
  grantees with the same privilege letter still occupy two distinct map entries.
- `relaclTextLockedFor` renders the owner aclitem first, then iterates the
  grantee roles in `sort.Strings` order, emitting one
  `<grantee>=<letters>/postgres` item each. `sg_role_a` sorts before `sg_role_b`,
  matching PostgreSQL's grant-order array here, so the relacl text is
  byte-identical.
- pg_dump's client-side `buildACLCommands` (`src/bin/pg_dump/dumputils.c`) walks
  the parsed aclitem array and emits a separate `GRANT <privs> ON TABLE … TO
  <grantee>;` per non-owner entry — it never merges grantees, so two aclitems
  produce two GRANT lines regardless of whether their privilege sets match.

## Note on ordering

`relaclTextLockedFor` sorts grantees alphabetically, whereas PostgreSQL stores
relacl in grant order. The two agree only when grants are issued in alphabetical
order (as here). The assertions use `strings.Contains` per line, so they are
robust to line order regardless; the byte-identical claim above holds for the
alphabetical-grant-order fixture specifically.

## Tests

- `internal/testport/pgdump_connsetup_test.go` —
  `TestPort_PgDumpConnectionSetup` (slice 353 fixture + both-grantee GRANT line
  assertions + negative assertion against the merged form). PASS.

## Gates

- `go build ./...` clean.
- `go test -run TestPort_PgDumpConnectionSetup ./internal/testport/` PASS.
- `go test ./internal/catalog/` PASS.
- pgbench CI-parity smoke (pre-commit hook).
