# M0149\-0001h1a — large objects

**Status:** slice 1 \(pg\_largeobject\_metadata, lo\_create / lo\_creat /
lo\_unlink, has\_largeobject\_privilege, lo\_compat\_privileges\) landed
2026\-11\-17. Data pages and the descriptor API are the next slices.

## Problem

goopg had no large objects. `lo_create` and the other `lo_*` functions did not
exist, and pg\_largeobject\_metadata was an empty virtual table. Regress
`privileges` and `largeobject` depend on them, and so do psql's `\lo_*`
commands and pg\_dump's getBlobs.

## PG's design

- pg\_largeobject\_metadata \(2995: `oid`, `lomowner`, `lomacl`\) says a large
  object exists, who owns it and its ACL. pg\_largeobject \(2613: `loid`,
  `pageno`, `data`\) holds the data in LOBLKSIZE pages.
- Both are ordinary MVCC heaps \(catalog/pg\_largeobject.c,
  storage/large\_object/inv\_api.c\). So an object created or unlinked by an
  aborted transaction leaves no trace, and visibility follows the snapshot.
- lo\_create \(be-fsstubs.c → inv\_create → LargeObjectCreate\):
  - OID 0 asks for a new OID \(GetNewOidWithIndex\);
  - an OID that is in use violates pg\_largeobject\_metadata\_oid\_index
    \(23505\);
  - the owner is the current user;
  - lomacl is get\_user\_default\_acl\('L'\).
- lo\_unlink:
  - 42704 when the object does not exist;
  - 42501 "must be owner of large object %u" unless the user has the owner's
    privileges or lo\_compat\_privileges is on;
  - inv\_drop then deletes the metadata row and every data page.
- has\_largeobject\_privilege \(acl.c has\_lo\_priv\_byid\):
  - a missing object returns NULL, even for a superuser, because
    LargeObjectExistsWithSnapshot runs before any privilege test;
  - lo\_compat\_privileges answers true;
  - otherwise pg\_largeobject\_aclmask\_snapshot applies: superuser, owner,
    then lomacl, with acldefault\('L', owner) = owner `rw` and nothing for
    PUBLIC;
  - privileges are SELECT `r` and UPDATE `w`.

## goopg's design

The goal is PG's storage, not an in\-memory registry.

- initdb.loadSystemCatalogsIfPresentForDB registers pg\_largeobject\_metadata
  per database as a **real heap table**, as it does pg\_type and
  pg\_attribute. `RegisterRealTable` replaces the virtual placeholder that the
  in\-memory catalog \(no heap\) still uses.
- **Insert:** lo\_create writes its row with writeHeapRowCanonical:
  - xmin is the writer XID, so a savepoint's sub\-XID works too;
  - the insert is logged as XLOG\_HEAP\_INSERT;
  - catalogIndexDefs gains 2995 → 2996, so maintainCatalogIndexes adds the
    pg\_largeobject\_metadata\_oid\_index entry. A PG node's syscache
    lookups and its uniqueness check find goopg's objects through it.
- **Read and delete:** these run as internal SQL on that table
  \(`internalCatalogQuery`, with the session role's table ACL bypassed as
  backend catalog access is\). So they use the executor's ordinary snapshot
  visibility, and an aborted lo\_unlink is undone by MVCC.
- **Why not stampCatalogRows for delete:** it sets HEAP\_XMAX\_COMMITTED
  immediately, which is not transactional — an aborted lo\_unlink would still
  hide the row.
- lo\_compat\_privileges is a registered PGC\_SUSET bool \(default off\).

Probe \(goopg vs PG 18.3\), identical on every result:

- create, duplicate, rollback, owner / PUBLIC / role privileges, compat mode;
- non\-superuser create and unlink, and read\-only transactions;
- across databases, and after a restart;
- PG cold start on the goopg directory: the index scan finds the objects,
  has\_largeobject\_privilege agrees, and lo\_create\(existing\) is 23505.

## Open

- pg\_largeobject data and the descriptor API: lo\_open / loread / lowrite /
  lo\_lseek / lo\_tell / lo\_truncate / lo\_close, lo\_get / lo\_put /
  lo\_from\_bytea, lo\_import / lo\_export, and per\-transaction descriptors.
- GRANT / REVOKE ON LARGE OBJECT \(parsed as a no\-op today\), ALTER LARGE
  OBJECT … OWNER TO, COMMENT ON LARGE OBJECT, ALTER DEFAULT PRIVILEGES … ON
  LARGE OBJECTS \(get\_user\_default\_acl\), and the role dependencies.
- goopg's pg\_proc exposes only curated builtins. libpq's lo\_initialize
  therefore fails "cannot determine OID of function lo\_open", which breaks
  psql `\lo_unlink`.
- pg\_typeof does not evaluate its argument, so a volatile argument
  \(`pg_typeof(lo_create(0))`\) has no side effect.
- goopg FSM forks carry no page checksum; PG warns and zeroes them
  \(M0149\-0001g finding\).
