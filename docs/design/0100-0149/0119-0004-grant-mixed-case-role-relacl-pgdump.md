# 0119-0004 — `GRANT` to a case-significant (mixed-case quoted) role name round-trip in pg_dump (DU-002 slice 337)

Status: accepted
Milestone: M0119-0004 (pg_dump 002–010 TAP — catalog-view parity battery)
Date: 2026-06-30

## Problem

A `GRANT` whose grantee was created with a case-significant (double-quoted)
name must round-trip through pg_dump:

```sql
CREATE ROLE "MixedCase";
CREATE TABLE public.grant_mc (a integer);
GRANT SELECT ON TABLE public.grant_mc TO "MixedCase";
```

PostgreSQL role names are case-significant when double-quoted, and `aclitemout`
renders the role's **true name** in `pg_class.relacl`. `MixedCase` is all-alnum,
so `putid()` (`src/backend/utils/adt/acl.c`) leaves it **bare** in the aclitem —
quoting keys on the *character set*, not on case (`isalnum` accepts uppercase):

```
{postgres=arwdDxtm/postgres,MixedCase=r/postgres}
```

pg_dump's `getTables` reads `relacl`, parses it client-side in `buildACLCommands`
(`dumputils.c`), and re-emits the GRANT through `fmtId`, which **does** quote a
mixed-case identifier (it is not a valid bare identifier), producing the
byte-exact:

```sql
GRANT SELECT ON TABLE public.grant_mc TO "MixedCase";
```

goopg's slice 331+ ACL store (`InMemory.tableACLs`) keys privileges by the
**lower-cased** role name so `HasTablePrivilege` lookups stay case-insensitive
(M0118-0008 `truncate-conflict`). `relaclTextLockedFor` therefore rendered the
lower-cased key `mixedcase`, and pg_dump emitted `TO mixedcase` — a *different,
nonexistent* role. This was the deferred "mixed-case quoted-role case
preservation" limitation recorded by slice 336.

## Fix (rendering-only)

`internal/catalog/catalog.go`:

- New field `roleACLDisplay map[string]string` (lower-cased role name → the exact
  case it was spelled in the GRANT). PostgreSQL stores the role's true name in
  `relacl`; this map lets goopg recover it while keeping the privilege store
  case-folded for lookups.
- `GrantTablePrivilegeWithGrantOption` captures the trimmed original spelling
  *before* lower-casing and records `roleACLDisplay[lower] = original` only when
  it actually differs from the lower-cased key (the common all-lowercase case
  needs no override → zero new entries for existing fixtures).
- `relaclTextLockedFor` resolves the lower-cased ACL key through `roleACLDisplay`
  to recover the original case, **after** the `publicPseudoRole` → `""` mapping
  (so a `GRANT … TO PUBLIC` recorded under the reserved key `public` still maps to
  the empty grantee, never to a display name) and **before** `aclQuoteName`.

The mixed-case-plus-special-character case composes for free: `"Weird-Role"` is
both case-preserved (display map) and double-quoted (`aclQuoteName`, slice 336)
→ `"Weird-Role"=r/postgres`.

No `grant_ddl.go` change: `GRANT … TO "MixedCase"` already flows through
`tryRecordTableGrant` on the original-case statement (`handleQuery` passes the
raw `matchable`, not the normalized SQL), and `splitGrantList` trims the
surrounding quotes, so `GrantTablePrivilegeWithGrantOption` receives the exact
spelling `MixedCase`.

## Blast radius

Zero for every existing path. `roleACLDisplay` gains an entry only when a
grantee's spelling differs from its lower-case (no all-lowercase fixture records
one), and the lookup is consulted only in the rendering path. `HasTablePrivilege`
and the `truncate-conflict` enforcement read the lower-cased store key unchanged,
so case-insensitive privilege checks still match (`mixedcase`, `MIXEDCASE`, and
`MixedCase` all resolve). The PUBLIC pseudo-grantee branch runs first, so PUBLIC
is never re-cased.

### Note on `CREATE ROLE` case-folding

goopg's `normalizeCompatSQL` lower-cases unquoted *and* double-quoted identifiers,
so the role registry stores `mixedcase` for `CREATE ROLE "MixedCase"`. This does
not affect the round-trip: pg_dump (without `--globals`/`pg_dumpall`) does not
emit `CREATE ROLE`, and `relacl` carries the grantee name as *text*, parsed
directly by pg_dump — no OID→`pg_authid` resolution is involved. The display name
recorded from the GRANT statement (which `handleQuery` keeps in original case) is
the authoritative source. Faithful case-significant identifier folding cluster-
wide remains a separate, larger change and is out of scope here.

## Gates

- `TestRelaclTextMixedCaseGrantee` (catalog): `GRANT … TO MixedCase` renders
  `{postgres=arwdDxtm/postgres,MixedCase=r/postgres}` (bare, case-preserved);
  `HasTablePrivilege` resolves `mixedcase`/`MIXEDCASE`; mixed case + hyphen
  composes to `"Weird-Role"=r/postgres`.
- `TestRelaclText{,QuotedGrantee,GrantOption,Sequence,Public}` /
  `TestACLQuoteName` / `TestNamespaceACLText` / `TestRoleOIDRegistry` (catalog)
  unchanged-PASS (no all-lowercase fixture records a display entry).
- `TestPort_PgDumpConnectionSetup` **DU-002 slice 337**: `CREATE ROLE
  "MixedCase"` + `GRANT SELECT ON TABLE public.grant_mc TO "MixedCase"` →
  `GRANT SELECT ON TABLE public.grant_mc TO "MixedCase";` byte-identical vs real
  pg_dump 18.3.
- catalog + server unit suites + `truncate-conflict` isolation PASS;
  `go build ./...` clean; pgbench smoke = pre-commit hook.

## Still open under M0119-0004

Column-level (`pg_attribute.attacl`, needs a heap re-sync driven from an executor
context the server-side GRANT recorder lacks) and database (`datacl`, only dumped
under `pg_dump --create`) GRANT projection; REVOKE-of-default modelling;
extended-protocol commit-time deferral (architecturally entangled — extended
protocol is auto-commit-per-statement).

## Oracle

- `src/backend/utils/adt/acl.c` — `putid`, `is_safe_acl_char` (name quoting keys
  on character set, not case).
- `src/bin/pg_dump/dumputils.c` — `buildACLCommands` / `getid` (client-side
  aclitem parse + `fmtId` re-quote of a mixed-case identifier).
- Compared byte-for-byte against `./postgres/local_install` pg_dump 18.3.
