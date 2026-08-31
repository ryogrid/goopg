# 0119-0004ak — GRANT … TO PUBLIC (`relacl` empty grantee) round-trip in pg_dump (DU-002 slice 334)

Status: accepted

## Problem

A table-level grant to the PUBLIC pseudo-role —

```sql
GRANT SELECT ON TABLE public.grant_pub TO PUBLIC;
```

must survive a pg_dump/restore round-trip. PostgreSQL stores a grant to PUBLIC
with an **empty grantee** in `pg_class.relacl`: the aclitem renders as
`=r/postgres` (nothing before the `=`). pg_dump reads `relacl` and parses it
client-side in `buildACLCommands` (`src/bin/pg_dump/dumputils.c`); when
`parseAclItem` yields an empty grantee it emits the keyword `PUBLIC`:

```c
appendPQExpBufferStr(thissql, "TO ");
if (grantee->len == 0)
    appendPQExpBufferStr(thissql, "PUBLIC;\n");
else
    appendPQExpBuffer(thissql, "%s;\n", fmtId(grantee->data));
```

The owner's own entry (`postgres=arwdDxtm/postgres`) cancels against the
`acldefault('r', 10)` baseline, and tables grant nothing to PUBLIC by default,
so the diff is a single `GRANT SELECT ON TABLE public.grant_pub TO PUBLIC;`.

Slices 331–333 surfaced the goopg catalog ACL store into `relacl` for tables and
sequences, but rendered every grantee under its stored role name. A grant to
PUBLIC was recorded under the lower-cased role name `public`, so `relacl` would
have materialized `public=r/postgres` — pg_dump would then emit
`GRANT SELECT … TO public` (a quoted/named role that does not exist), not
`TO PUBLIC`. The grant was effectively mis-rendered on dump/restore.

## Fix (dump-fidelity only)

Render the reserved role name `public` as the empty grantee in the materialized
aclitem[]. PostgreSQL reserves `PUBLIC`, so no real role can be named `public`;
goopg already lower-cases the grantee at record time
(`GrantTablePrivilegeWithGrantOption`), so a `GRANT … TO PUBLIC` lands in the
shared relation ACL store under the key `public` with no server-side change.

`internal/catalog/catalog.go`:

- New `publicPseudoRole = "public"` constant.
- `relaclTextLockedFor` (the object-type-agnostic core shared by the table and
  sequence variants) maps a `public` role key to the empty grantee `""` when
  building the aclitem, so it renders `=<privs>/postgres` instead of
  `public=<privs>/postgres`.

No change to `internal/server/grant_ddl.go`: `GRANT SELECT ON TABLE … TO PUBLIC`
already flows through `tryRecordTableGrant` (the role list splits to `PUBLIC`,
lower-cased to `public` by the store). The grant-option `*` logic and the
sequence/table privilege orders are unchanged, so a `GRANT … TO PUBLIC WITH
GRANT OPTION` and a sequence grant to PUBLIC round-trip too.

`HasTablePrivilege` and the `truncate-conflict` enforcement path are untouched —
only the relacl *rendering* maps `public`→empty; the stored key is unchanged. A
relation with no PUBLIC grant projects exactly as before → zero blast radius.

## Why rendering-only is sufficient

pg_dump never invokes server-side `aclexplode`/`aclitemout`; it parses the
`relacl` aclitem[] text in the client. Projecting `=r/postgres` is the entire
contract — the client turns the empty grantee into `TO PUBLIC`.

## Gates

- `TestRelaclTextPublic` (catalog): `GRANT SELECT TO PUBLIC` →
  `{postgres=arwdDxtm/postgres,=r/postgres}`; a named grantee coexisting with
  PUBLIC keeps its name while PUBLIC stays the empty grantee.
- `TestPort_PgDumpConnectionSetup` **DU-002 slice 334**: `grant_pub` +
  `GRANT SELECT ON TABLE public.grant_pub TO PUBLIC` → real pg_dump 18.3
  (binary, run against the goopg server) emits
  `GRANT SELECT ON TABLE public.grant_pub TO PUBLIC;` — byte-identical.
- `TestRelaclText` / `TestRelaclTextGrantOption` / `TestRelaclTextSequence` +
  catalog/server + `truncate-conflict` isolation suites PASS; `go build ./...`
  clean; pgbench smoke = pre-commit hook.

## Still open under M0119-0004

Column-level (`pg_attribute.attacl`, heap re-sync) / schema (`nspacl`) / database
(`datacl`) GRANT projection; REVOKE-of-default modelling;
reserved-keyword-named-role quoting; extended-protocol commit-time deferral.
