# 0119-0004 — `GRANT` to a quoting-required role name (`relacl` `putid`) round-trip in pg_dump (DU-002 slice 336)

Status: accepted
Milestone: M0119-0004 (pg_dump 002–010 TAP — catalog-view parity battery)
Date: 2026-06-30

## Problem

A `GRANT` whose grantee role name contains characters outside `[A-Za-z0-9_]`
(e.g. a hyphen, a space, or a multibyte character) must round-trip through
pg_dump:

```sql
CREATE ROLE "weird-role";
CREATE TABLE public.grant_q (a integer);
GRANT SELECT ON TABLE public.grant_q TO "weird-role";
```

PostgreSQL materializes the grant into `pg_class.relacl` as an `aclitem[]`. The
per-name renderer `putid()` (`src/backend/utils/adt/acl.c`) **double-quotes** any
name that is not "safe", where `is_safe_acl_char(c, /*is_getid=*/false)` admits
only ASCII alphanumerics and underscore (a high-bit/multibyte byte is unsafe on
output); an internal double quote is doubled. So the stored aclitem is:

```
{postgres=arwdDxtm/postgres,"weird-role"=r/postgres}
```

pg_dump's `getTables` reads `relacl` directly and parses it client-side in
`buildACLCommands` (`dumputils.c`); its `getid` tokenizer relies on those quotes
to read the whole grantee name, then re-emits the GRANT through `fmtId` (which
re-quotes), producing the byte-exact:

```sql
GRANT SELECT ON TABLE public.grant_q TO "weird-role";
```

goopg's slices 331–335 surfaced its in-memory ACL store into `relacl` via the
object-type-agnostic renderer `relaclTextLockedFor`, but rendered every grantee
**raw** (`grantee + "=" + letters + "/postgres"`). For `weird-role` that emits
`weird-role=r/postgres`; pg_dump's `getid` stops at the hyphen and reads the
grantee as `weird`, mis-rendering or dropping the GRANT.

### Reserved keywords already round-trip (no change)

`putid` quotes purely on the *character set*, not on keyword-ness or case
(`isalnum` accepts uppercase). A reserved-keyword role name such as `user` is
all-alnum, so PG stores it bare (`user=r/postgres`) and goopg already renders it
identically; pg_dump's `fmtId` adds the quotes client-side on output
(`TO "user";`). So the "reserved-keyword-named-role quoting" item tracked since
loop #53 needs **no goopg change** — the genuine gap is the special-character
case handled here.

## Fix (rendering-only)

`internal/catalog/catalog.go`:

- New `aclQuoteName(s string) string` reproduces `putid`: scan the bytes; if any
  is outside `[A-Za-z0-9_]`, wrap in double quotes and double every internal
  `"`; otherwise return verbatim. The empty string (the PUBLIC pseudo-grantee,
  slice 334) is "safe" (empty scan) → returned unchanged, so PUBLIC still renders
  as the empty grantee. A high-bit byte (multibyte UTF-8) is unsafe → quoted,
  matching `is_safe_acl_char`'s `IS_HIGHBIT_SET` → `is_getid` (false on output).
- `relaclTextLockedFor` wraps the computed `grantee` (after the
  `publicPseudoRole` → `""` mapping) in `aclQuoteName` before assembling the
  aclitem element.

No `grant_ddl.go` change: `GRANT … TO "weird-role"` already flows through
`tryRecordTableGrant` (`splitGrantList` trims the surrounding quotes; the stored
key is `weird-role`, already lowercase so the store's `ToLower` is a no-op).

## Blast radius

Zero. `aclQuoteName` is the identity function for every all-alnum/underscore
name and for the empty PUBLIC grantee, so every existing relacl/nspacl/sequence
projection (slices 331–335) is byte-identical. `HasTablePrivilege` and the
`truncate-conflict` enforcement path read the stored map key, never the rendered
text. Only a grantee name with a special character changes — from a broken
unquoted form to the correct PG-faithful quoted form.

## Limitation (follow-up)

`GrantTablePrivilegeWithGrantOption` lower-cases the stored role name, so a
*mixed-case* quoted role (`"WeirdRole"`) loses its case and round-trips as
`weirdrole`. Preserving the original case for quoted identifiers is a separate
case-folding fix (the lower-casing also affects `HasTablePrivilege` lookups) and
is deferred; this slice covers the special-character (case-insensitive) case,
which is the one that otherwise corrupts the dump.

## Gates

- `TestRelaclTextQuotedGrantee` (catalog): a hyphenated grantee renders
  `{postgres=arwdDxtm/postgres,"weird-role"=r/postgres}`; a plain
  alnum/underscore role plus PUBLIC coexist unquoted.
- `TestACLQuoteName` (catalog): direct `putid` unit cases — empty, owner,
  alnum/underscore, hyphen, space, internal-quote-doubling (`a"b` → `"a""b"`),
  multibyte (`café`).
- `TestPort_PgDumpConnectionSetup` **DU-002 slice 336**: `CREATE ROLE
  "weird-role"` + `GRANT SELECT ON TABLE public.grant_q TO "weird-role"` →
  `GRANT SELECT ON TABLE public.grant_q TO "weird-role";` byte-identical vs real
  pg_dump 18.3.
- `TestRelaclText` / `…GrantOption` / `…Sequence` / `…Public` /
  `TestNamespaceACLText` + catalog/server unit suites + `truncate-conflict`
  isolation PASS; `go build ./...` clean; pgbench smoke = pre-commit hook.

## Still open under M0119-0004

Column-level (`pg_attribute.attacl`, needs a heap re-sync driven from an executor
context the server-side GRANT recorder lacks) and database (`datacl`, only
dumped under `pg_dump --create`) GRANT projection; REVOKE-of-default modelling;
mixed-case quoted-role case preservation (above); extended-protocol commit-time
deferral (architecturally entangled — extended protocol is
auto-commit-per-statement).

## Oracle

- `src/backend/utils/adt/acl.c` — `putid`, `is_safe_acl_char` (name quoting).
- `src/bin/pg_dump/dumputils.c` — `buildACLCommands` / `getid` (client-side
  aclitem parse + `fmtId` re-quote).
- Compared byte-for-byte against `./postgres/local_install` pg_dump 18.3.
