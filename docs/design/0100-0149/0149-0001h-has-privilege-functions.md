# M0149\-0001h — the has\_\*\_privilege functions

**Status:** has\_database\_privilege landed 2026\-11\-17. The rest of the
family is M0149\-0001h1.

## Problem

None of acl.c's has\_\*\_privilege functions had an executor handler. pg\_proc
seeds them, so a call resolved by name and then failed with "function … does
not exist". psql `\l+` calls has\_database\_privilege, so the command failed
on goopg.

## has\_database\_privilege \(executor/has\_database\_privilege.go\)

A port of the six acl.c overloads.

- **Role:**
  - a name goes through get\_role\_oid\_or\_public: `public` is
    ACL\_ID\_PUBLIC, and an unknown name is 42704;
  - an OID is taken as given;
  - the two\-argument forms use the effective user.
- **Database:**
  - a name goes through get\_database\_oid \(unknown → 3D000\);
  - an OID is the SQL\-visible pg\_database.oid, mapped back by
    `catalog.DatabaseNameByDisplayOID`; an unknown OID returns NULL.
- **Privilege string:** convert\_database\_priv\_string. It is a
  comma\-separated, case\-insensitive list of CREATE / TEMPORARY / TEMP /
  CONNECT, each optionally WITH GRANT OPTION. Anything else is 22023.
- **Decision:** object\_aclcheck with ACLMASK\_ANY over aclmask's rules:
  - a superuser holds everything;
  - a role with the owner's privileges holds every grant option;
  - privileges come from the datacl entries whose grantee is PUBLIC, the
    role, or a role it has the privileges of;
  - a NULL datacl is acldefault\('d'\): owner CTc, PUBLIC Tc.

The ACL source is the same in\-memory store pg\_database.datacl renders from,
so `\l+` and the function cannot disagree.

## initdb's template datacl

PG's initdb runs `REVOKE CREATE,TEMPORARY ON DATABASE template1/template0
FROM public` \(initdb.c setup\_privileges\). Both templates therefore start
with `{=c/postgres,postgres=CTc/postgres}`. goopg wrote NULL. `templateDatacl`
now seeds the PG value, and the pg\_database heap reload restores it on
restart.

## Open

- M0149\-0001h1: the rest of the family \(table, schema, column, sequence,
  function, type, language, FDW, server, tablespace, parameter, large
  object\). Each has the same role / object / privilege\-string resolution
  over its own ACL text.

## Shared evaluator \(M0149\-0001h1\)

`has_privilege_common.go` holds acl.c's common machinery:

- `privilegeArgs` — role resolution;
- `parsePrivList` — convert\_\*\_priv\_string;
- `aclAnyHeld` — aclmask with ACLMASK\_ANY, including the predefined\-role
  arms \(pg\_read\_all\_data / pg\_write\_all\_data / pg\_maintain\).

Each object kind supplies an `aclObject`: owner, rendered ACL text, its
acldefault letters, all its letters, and implied privileges.
has\_table\_privilege \(regclass input; sequences use the sequence ACL\) and
has\_schema\_privilege are built on it. Regress privileges went 1242 → 993.
