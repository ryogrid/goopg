# M0149\-0001h — the has\_\*\_privilege functions

**Status:** has\_database / table / schema / column / any\_column /
sequence / function / type\_privilege landed 2026\-11\-17. The rest of the
family is M0149\-0001h1; large objects are M0149\-0001h1a.

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
    `catalog.DatabaseNameByDisplayOID`; an unknown OID returns NULL, except
    for a superuser \(see "Unknown OIDs" below\).
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

## Columns, sequences, functions and types \(M0149\-0001h1 slice 2\)

`has_privilege_objects.go` adds five more families on the same evaluator.

- **has\_column\_privilege** \(twelve overloads, 3 or 4 arguments\) follows
  column\_privilege\_check:
  - the table goes through regclass input \(42P01\) or is an OID;
  - the column is a name \(convert\_column\_name: unknown → 42703, a
    dropped column's `........pg.dropped.N........` name → NULL\) or an
    attnum \(int2\); system columns resolve to their negative attnums;
  - the column's attacl \(acldefault\('c'\) is empty; the table owner holds
    the grant options\) is checked first, then the table's relacl;
  - an unknown table OID or a missing / dropped attnum returns NULL.
- **has\_any\_column\_privilege**: the table privilege, else any non\-dropped
  column's attacl.
- **has\_sequence\_privilege**: the privilege string \(USAGE / SELECT /
  UPDATE\) is parsed before the name, as in acl.c; a relation that is not a
  sequence is 42809 `"%s" is not a sequence`.
- **has\_function\_privilege**: regprocedure input \(42883\) or an OID;
  user routines, user aggregates and the full builtin pg\_proc table
  \(`catalog.RegprocName`\) count as existing. acldefault\('f'\) gives EXECUTE
  to the owner and PUBLIC.
- **has\_type\_privilege**: regtype input \(42704\) or an OID. A user array
  type's ACL is its element type's \(pg\_type\_aclmask follows typelem\);
  built\-in types are owned by the bootstrap superuser. acldefault\('T'\)
  gives USAGE to the owner and PUBLIC.

### Unknown OIDs

object\_aclmask\_ext, pg\_namespace\_aclmask\_ext and pg\_type\_aclmask\_ext
test `superuser_arg(roleid)` *before* looking the object up, so for a
superuser `has_function_privilege(99999, 'execute')` is **true**, and only
other roles see is\_missing and get NULL. The same holds for schema and
database OIDs. pg\_class\_aclmask\_ext looks the tuple up first, so
relations, sequences and columns return NULL for everyone.
`missingObjectResult` encodes this; it also parses the privilege string
first, so a bad privilege is still 22023.

Probe \(PG 18.3 vs goopg, superuser and `SET ROLE`\): identical on every
result and error. Regress privileges 993 → 889.

### Residuals

- has\_largeobject\_privilege needs large objects, which goopg does not
  have \(`lo_create` does not exist\): M0149\-0001h1a.
- goopg renumbers attnums after DROP COLUMN \(no attisdropped row\), so a
  column's attnum and the dropped placeholder name differ from PG's.
- regprocedure input ignores the argument list and knows only the curated
  builtins \(`'abs(int)'::regprocedure` fails\); regtype input rejects
  `t[]` for a user type.
- has\_language / foreign\_data\_wrapper / server / tablespace /
  parameter\_privilege are still missing \(M0149\-0001h1\).
