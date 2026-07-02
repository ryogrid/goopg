# DU-002 slice 352 — multi-grantee table (relacl) round-trip in pg_dump

Status: landed. Part of M0110-0001 / M0119-0004 (DU-002 pg_dump round-trip
slices). Test-only — NO engine change.

## What

Two distinct grantees on one table must each round-trip through pg_dump as their
own `GRANT` line:

```sql
CREATE TABLE public.multigrant_t(id int);
CREATE ROLE mg_role_a;
CREATE ROLE mg_role_b;
GRANT SELECT ON TABLE public.multigrant_t TO mg_role_a;
GRANT INSERT ON TABLE public.multigrant_t TO mg_role_b;
```

PostgreSQL 18.3 materializes `pg_class.relacl` as:

```
{postgres=arwdDxtm/postgres,mg_role_a=r/postgres,mg_role_b=a/postgres}
```

and `pg_dump --no-sync` emits exactly:

```sql
GRANT SELECT ON TABLE public.multigrant_t TO mg_role_a;
GRANT INSERT ON TABLE public.multigrant_t TO mg_role_b;
```

(captured from real PG 18.3 in `./postgres/local_install`).

## Why it already works

All prior GRANT slices (331+) handle the single-grantee case; the multi-grantee
fan-out needs no new engine code:

- Each `GRANT` is recorded independently under the OID-keyed `tableACLs` store
  via `GrantTablePrivilege`, one inner `map[priv]bool` per grantee role.
- `relaclTextLockedFor` renders the owner aclitem first, then iterates the
  grantee roles in `sort.Strings` order, emitting one `<grantee>=<letters>/postgres`
  item each. `mg_role_a` sorts before `mg_role_b`, which matches PostgreSQL's
  grant-order array here, so the relacl text is byte-identical.
- pg_dump's client-side `buildACLCommands` (`src/bin/pg_dump/dumputils.c`) walks
  the parsed aclitem array and emits a separate `GRANT <privs> ON TABLE … TO
  <grantee>;` per non-owner entry — it does not merge grantees, so two aclitems
  produce two GRANT lines.

The catalog multi-grantee deterministic-sort path is already unit-covered by the
two-grantee case in `TestRelaclText` (`internal/catalog/relacl_test.go`). This
slice adds only the end-to-end pg_dump round-trip fixture + assertion to
`TestPort_PgDumpConnectionSetup`, guarding the per-grantee fan-out against a
regression that would drop or merge a grantee's GRANT line.

## Note on ordering

`relaclTextLockedFor` sorts grantees alphabetically, whereas PostgreSQL stores
relacl in grant order. The two agree only when grants are issued in alphabetical
order (as here). The assertion uses `strings.Contains` per line, so it is robust
to line order regardless; the byte-identical claim above holds for the
alphabetical-grant-order fixture specifically.

## Tests

- `internal/testport/pgdump_connsetup_test.go` —
  `TestPort_PgDumpConnectionSetup` (slice 352 fixture + per-grantee GRANT line
  assertions). PASS.

## Gates

- `go build ./...` clean.
- `go test -run TestPort_PgDumpConnectionSetup ./internal/testport/` PASS.
- `go test ./internal/catalog/` PASS.
- pgbench CI-parity smoke (pre-commit hook).
