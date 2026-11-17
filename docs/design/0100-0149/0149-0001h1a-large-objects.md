# M0149\-0001h1a — large objects

**Status:** two slices landed 2026\-11\-17:

- slice 1: pg\_largeobject\_metadata, lo\_create / lo\_creat / lo\_unlink,
  has\_largeobject\_privilege and lo\_compat\_privileges;
- slice 2: pg\_largeobject data pages, the descriptor API, the bytea
  functions, and lo\_import / lo\_export.

- slice 3: GRANT / REVOKE ON LARGE OBJECT and ALTER LARGE OBJECT … OWNER TO.

COMMENT ON LARGE OBJECT and ALTER DEFAULT PRIVILEGES … ON LARGE OBJECTS are
next.

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

## Slice 3 — GRANT / REVOKE and ALTER OWNER \(largeobject\_acl.go\)

- **Where the ACL lives.** A large object's ACL lives only in its lomacl.
  goopg's OID\-keyed ACL store \(relations, types, parameters\) cannot hold
  it, because large\-object OIDs are user\-chosen and may equal any other
  object's OID.
- **Parser.** `GRANT` / `REVOKE … ON LARGE OBJECT <oid>, …` is captured as a
  `LargeObjectACLChange` on CompatNoopStmt \(the field is tagged
  `dump:"omitempty"`, so no golden moved\). `GRANT OPTION FOR` and `CASCADE`
  are recorded. The server's GRANT fast path \(postmaster/query.go\) routes
  `ON LARGE OBJECT` to the executor.
- **ExecGrant\_Largeobject port:**
  - resolution order is ExecuteGrantStmt's: objects \(42704\), grantees
    \(42704\), then privileges \(0LP01 "invalid privilege type %s for large
    object"\);
  - a NULL lomacl starts as acldefault\('L'\) = owner rw;
  - select\_best\_grantor: a role with the owner's privileges, or a
    superuser, grants as the owner;
  - restrict\_and\_check\_grant: with no grant option and no privilege, the
    error is "permission denied for large object large object %u". The
    doubled phrase is real: aclcheck\_error's format wraps
    ExecGrant\_Largeobject's objname. Partial grants and revokes are 01007 /
    01006 WARNINGs;
  - merge\_acl\_with\_grant / aclupdate key items by \(grantee, grantor\),
    drop emptied items, and on a lost grant option raise 2BP01 "dependent
    privileges exist" under RESTRICT or recurse under CASCADE.
- **ALTER LARGE OBJECT … OWNER TO.** A new grammar rule \(alter\_large\_object\_stmt,
  routed via routedCreatePairs\["alter"\]\["large"\]; legacy rejected
  every form\) ports AlterObjectOwner\_internal:
  - "must be owner of large object %u";
  - "must be able to SET ROLE \"%s\"";
  - aclnewowner carries the ACL over.
- **Row rewrite.** The row is rewritten with an internal\-SQL DELETE plus
  writeHeapRowCanonical. lomacl is written as a PG\-native \_aclitem blob
  \(encodeAclItemArrayText\); the string form stored the wrong element type,
  which PG's check\_acl rejects. The column is typed `aclitem[]` so the codec's
  catalog\-ACL arm applies, and the seq\-scan and index\-scan ACL render hooks
  now include lomacl.

**Probes \(goopg vs PG 18.3\):** identical for GRANT / REVOKE \(43
statements, including warnings, RESTRICT / CASCADE and rollback\) and for
ALTER OWNER \(22 statements\). A PG cold start reads the rewritten lomacl and
answers has\_largeobject\_privilege the same way.

## Slice 4: COMMENT ON LARGE OBJECT, PL/pgSQL condition matching, zero\-filled holes

- **COMMENT ON LARGE OBJECT** \(gram.y CommentStmt's `LARGE_P OBJECT_P
  NumericOnly` arm\) is a `comment_target` alternative and a routed comment
  kind. execCommentOn checks the object exists \(42704 "large object %u does
  not exist"\) and that the caller owns it \(check\_object\_ownership: 42501
  "must be owner of large object %u", waived by lo\_compat\_privileges\).
  It then stores the description under classoid 2613
  \(LargeObjectRelationId\), which is the key obj\_description\(oid,
  'pg\_largeobject'\) and psql `\lo_list` read. lo\_unlink drops the comment,
  as inv\_drop's performDeletion does.
- **PL/pgSQL exception conditions.** `cmd/gen-sqlstate` now also emits
  plerrcodes.h's table: every type\-E errcodes.txt entry that has a
  condition name, 251 entries, checked against the generated header.
  exceptionHandlerMatches is now exception\_matches\_conditions:
  - OTHERS matches everything except query\_canceled and assert\_failure;
  - a condition matches its exact code or, when it names a class \(xx000\),
    any code in that class;
  - a name stands for every code listed under it.

  `WHEN SQLSTATE 'xxxxx'` used to lose its string literal in the PL/pgSQL
  parser, leaving the bare word `SQLSTATE`, so it never matched. The parser
  now keeps the code \(`plpgsql.SQLStateConditionPrefix`\) and rejects a
  malformed one with "invalid SQLSTATE code".
  RAISE \<name\> takes the first entry of that name, as
  plpgsql\_recognize\_err\_condition does: reading\_sql\_data\_not\_permitted
  is 2F004, where the old hand\-written map said 2F002.
- **loread over a hole.** An lo\_truncate that extends an object writes only
  the last page. A read window that ends before that page found no pages and
  returned nothing. loRead now zero\-fills up to the object size, as
  inv\_read does.

## Slice 5: the fast\-path FunctionCall protocol \(libpq lo\_\*\)

- libpq's large\-object API resolves the lo\_\* function OIDs once
  \(fe\-lobj.c lo\_initialize, a pg\_proc query restricted to pg\_catalog\)
  and then calls them with PQfn: the FunctionCall \('F'\) message.
  - goopg's pg\_proc is a virtual view of selected builtins. The 13 functions
    lo\_initialize looks up are now appended to it, rendered from their
    pg\_proc.dat entries \(OIDs, argument types, strict, proparallel 'u'\).
  - internal/postmaster/fastpath.go answers 'F' \(fastpath.c
    HandleFunctionRequest\). goopg has no fmgr, so the call is lowered:
    1. resolve the OID with catalog.RegprocedureNameParts \(42883 "function
       with OID %u does not exist"\);
    2. decode the arguments \(parse\_fcall\_arguments: format codes, binary
       int2 / int4 / int8 / oid / bool / float / bytea / text, NULLs\) into
       typed literals;
    3. run `SELECT pg_catalog.f(...)` through the ordinary simple\-query
       path against a capture writer.

    That path supplies the implicit\-transaction and transaction\-block
    handling, the 25P02 refusal in an aborted block, snapshots, and
    error / notice reporting. The capture is then rewritten: notices and
    errors pass through, the result cell becomes the FunctionCallResponse
    \('V'\), and the captured ReadyForQuery closes the exchange.
  - The result is encoded with the function's prorettype
    \(SendFunctionResult\), not the RowDescription. goopg advertises bytea and
    void columns as text on the wire \(M0149\-0001h1a3\), and using that type
    returned loread's `\x…` text as data. libpq's lo\_export loop then
    never saw an empty read.
- **Found along the way:**
  - exprType typed every `pg_catalog.f\(...\)` call as the text fallback,
    because its switch keyed on the qualified name. It now strips the
    qualifier, as the executor already did.
  - lo\_get over a 4 GB hole zero\-filled the whole range \(23 GB RSS\).
    lo\_get\_fragment\_internal's MaxAllocSize check \(54000 "large object
    read request is too large"\) is ported, and loRead no longer
    preallocates the caller's full request.
  - Untyped literals passed to lowrite / lo\_put / lo\_from\_bytea now go
    through byteain \(`E'\\\\xdeadbeef'`\).
- Regress largeobject passes \(0 / 496\), and so does
  TestPort\_RegressSuite/largeobject. The Go harness now exports
  PG\_ABS\_BUILDDIR, as pg\_regress does. opr\_sanity 733→717.

## Slice 6: catalogs PG revokes from PUBLIC \(M0149\-0001h1a4\)

The privileges regress case reads pg\_largeobject as an unprivileged role,
expecting "permission denied for table pg\_largeobject". goopg granted SELECT
on every system relation \(standing in for PG's initdb\-time PUBLIC grants\),
including the six catalogs PG explicitly revokes: pg\_authid, pg\_largeobject,
pg\_statistic, pg\_statistic\_ext\_data, pg\_user\_mapping and pg\_subscription.
Any role could therefore read pg\_authid's password hashes.

dmlPrivilegePermittedAs now treats these six like user tables: an explicit
grant, the owner, a superuser, or a member of pg\_read\_all\_data
\(pg\_class\_aclmask\_ext\) can read them. They are matched by OID, or by
name in pg\_catalog for virtual ones. valuesOp, which serves virtual catalogs
such as pg\_authid, applies the same check.

## Slice 7: RAISE ... USING \(M0149\-0001h1a2\)

Found through regress plpgsql's raise\_test, which large\-object work drove
into. The PL/pgSQL parser kept everything after RAISE as raw text, and only a
condition\-name RAISE looked at `USING MESSAGE = '<literal>'`. It now ports
pl\_gram.y stmt\_raise: an optional format and its arguments, then a
top\-level USING list of `name = expr` options, `SQLSTATE 'xxxxx'` as a
condition, and bare `RAISE;`.

execRaise ports exec\_stmt\_raise:
- ERRCODE resolves a SQLSTATE or a condition name;
- an option given twice is 42601, and a NULL option is 22004;
- the default message is the condition name, else the SQLSTATE;
- DETAIL and HINT are carried. Notices go out inline through a new
  Context.NoticeFieldsFlush, so they keep their order among plain notices.

RAISE errors no longer carry a query position: PG reports them through the
PL/pgSQL error context, never as a caret in the calling statement.

## Slice 8: ALTER DEFAULT PRIVILEGES ... ON LARGE OBJECTS \(M0149\-0001h1a6\)

- pg\_default\_acl entries for defaclobjtype 'L' are now stored and
  rendered. The privileges are SELECT 'r' and UPDATE 'w', the owner baseline
  is "rw", and there is no PUBLIC world default. Before this, the executor
  accepted and dropped them, because large objects had no storage.
- lo\_create stamps get\_user\_default\_acl\(OBJECT\_LARGEOBJECT, owner,
  InvalidOid\) into lomacl \(loDefaultACLText\): the owner's global entry, or
  NULL when there is none or it equals acldefault\('L', owner\). goopg's
  renderer writes a global entry's implicit owner item and grantors as
  postgres; for another owner they are re\-pointed at the owner.
- pg\_default\_acl changes are now transactional. ALTER DEFAULT PRIVILEGES
  snapshots each entry it touches \(InMemory.SnapshotACLEntry\) onto the
  session's catalog\-undo list \(RecordCatalogUndo\), and ProcessRollbackUndos
  restores them newest first. Without this, the privileges case's
  rolled\-back `GRANT ALL ON LARGE OBJECTS` leaked into the largeobject case,
  where it granted PUBLIC UPDATE on an object.

## Open

- A read\-only descriptor reads with the statement's snapshot, not the
  snapshot registered at lo\_open \(PG keeps that snapshot until close\).
- A large object created in the same statement is visible to a later call
  in that statement; PG's statement snapshot hides it.
- Bit strings have no `|` / `&` operators and no `bit → int4` cast. As a
  result, `x'20000'::int` and `x'20000' | x'40000'` fail, which blocks
  most large\-object steps in regress `privileges` and `largeobject`
  \(M0149\-0001h1a1\).
- The pg\_shdepend owner / ACL role dependencies of large objects and of
  pg\_default\_acl rows.
- The fast path is a lowering to SQL, not an fmgr call. Binary arguments and
  results cover only the types listed above \(others are 0A000\), and
  log\_statement / log\_duration do not print PG's "fastpath function call"
  lines.
- Comments \(all kinds\) live in an in\-memory map. They are neither
  transactional nor per\-database, so a large object's comment key
  \(2613, oid\) is shared across databases.
- An unknown PL/pgSQL condition name is accepted and simply never matches.
  PG rejects it when the function is compiled \("unrecognized exception
  condition"\).
- pg\_typeof does not evaluate its argument, so a volatile argument
  \(`pg_typeof(lo_create(0))`\) has no side effect.
- goopg FSM forks carry no page checksum; PG warns and zeroes them
  \(M0149\-0001g finding\).
