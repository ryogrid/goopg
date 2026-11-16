# M0149\-0001h1a — large objects

**Status:** two slices landed 2026\-11\-17:

- slice 1: pg\_largeobject\_metadata, lo\_create / lo\_creat / lo\_unlink,
  has\_largeobject\_privilege and lo\_compat\_privileges;
- slice 2: pg\_largeobject data pages, the descriptor API, the bytea
  functions, and lo\_import / lo\_export.

GRANT / ALTER / COMMENT ON LARGE OBJECT are next.

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

## Slice 2 — data pages and descriptors \(largeobject\_data.go\)

- **Storage.** pg\_largeobject \(2613: `loid`, `pageno`, `data`\) is a real
  heap per database, like the metadata. Pages are LOBLKSIZE \(2048\)
  bytes or shorter. Missing pages and short pages read back as zeros. An
  object's size is the end of its last page \(inv\_getsize\).
- **Page writes:**
  - a page is replaced by an internal\-SQL DELETE followed by
    writeHeapRowCanonical, so rollback is MVCC;
  - catalogIndexDefs gains 2613 → 2683, so a PG node finds the pages
    through `pg_largeobject_loid_pn_index`;
  - inv\_write keeps the bytes outside the written range and grows the
    page to cover it;
  - inv\_truncate either resizes the page holding `len`, or deletes the
    first later page and inserts a page of `len % LOBLKSIZE` zero bytes
    \(even an empty one); then it drops every later page.
- **Descriptors** \(be\-fsstubs.c `cookies[]`\):
  - each connection has an array; a new descriptor takes the lowest free
    slot;
  - INV\_WRITE implies read; neither flag is 22023;
  - inv\_open checks existence \(42704\), then SELECT, and UPDATE as well
    for write \(42501\); lo\_compat\_privileges skips the privilege checks;
  - lowrite or lo\_truncate on a read\-only descriptor is 55000;
  - seeks use whence 0 / 1 / 2, reject a negative target \(22023\), and
    report 22003 when the int4 variants overflow.
- **End of transaction.** AtEOXact\_LargeObject is reproduced from
  `ReleaseAdvisoryTransactionLocks`. Every transaction\-end path already
  calls it with the connection identity: autocommit, COMMIT / ROLLBACK,
  chained transactions and the extended protocol.
  `transam.Transaction.Handle` is procNum\+1, stable per backend, so it
  cannot be used to tell transactions apart. Session end goes through
  `ReleaseAllAdvisoryLocks`. lo\_unlink also closes the session's
  descriptors on the object and deletes its pages.
- **bytea functions:** lo\_get / lo\_get\(oid, off, len\) \(a negative
  length is 22023\), lo\_put and lo\_from\_bytea.
- **Server files:** lo\_import / lo\_export are superuser\-only, matching
  system\_functions.sql's REVOKE EXECUTE … FROM public, and use
  errcode\_for\_file\_access codes.

**Probe** \(goopg vs PG 18.3, 80 statements\): identical. It covered:

- descriptor numbering across COMMIT / ROLLBACK, seek / tell / read / write,
  holes, page boundaries and truncation;
- rollback of lo\_put, unlink with an open descriptor, import / export,
  non\-superuser denials and read\-only transactions.

A PG cold start on the goopg directory reads and updates goopg\-written pages
through index 2683.

## Open

- A read\-only descriptor reads with the statement's snapshot, not the
  snapshot registered at lo\_open \(PG keeps that snapshot until close\).
- A large object created in the same statement is visible to a later call
  in that statement; PG's statement snapshot hides it.
- Bit strings have no `|` / `&` operators and no `bit → int4` cast. As a
  result, `x'20000'::int` and `x'20000' | x'40000'` fail, which blocks
  most large\-object steps in regress `privileges` and `largeobject`
  \(M0149\-0001h1a1\).
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
